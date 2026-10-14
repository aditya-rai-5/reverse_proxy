package ratelimit

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Config defines the configuration for token bucket rate limiting.
type Config struct {
	RequestsPerMinute int           `yaml:"requests_per_minute"`
	Burst             int           `yaml:"burst"`
	TTL               time.Duration `yaml:"ttl,omitempty"`
	CleanupInterval   time.Duration `yaml:"cleanup_interval,omitempty"`
}

// Defaults applies default values for zero fields.
func (c *Config) Defaults() {
	if c.RequestsPerMinute > 0 && c.Burst <= 0 {
		c.Burst = 1
	}
	if c.TTL <= 0 {
		c.TTL = 3 * time.Minute
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = 1 * time.Minute
	}
}

// Enabled returns true if rate limiting is active.
func (c *Config) Enabled() bool {
	return c != nil && c.RequestsPerMinute > 0
}

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter manages per-IP rate limiters using the token bucket algorithm.
type Limiter struct {
	mu              sync.Mutex
	clients         map[string]*client
	rate            rate.Limit
	burst           int
	requestsPerMin  int
	ttl             time.Duration
	cleanupInterval time.Duration
	stopCh          chan struct{}
	stopOnce        sync.Once
}

// New creates a new Limiter with the given configuration.
func New(cfg Config) *Limiter {
	cfg.Defaults()

	var r rate.Limit
	if cfg.RequestsPerMinute > 0 {
		r = rate.Limit(float64(cfg.RequestsPerMinute) / 60.0)
	} else {
		r = rate.Inf
	}

	l := &Limiter{
		clients:         make(map[string]*client),
		rate:            r,
		burst:           cfg.Burst,
		requestsPerMin:  cfg.RequestsPerMinute,
		ttl:             cfg.TTL,
		cleanupInterval: cfg.CleanupInterval,
		stopCh:          make(chan struct{}),
	}

	go l.cleanupLoop()

	return l
}

// Allow reports whether a request from the given IP address is permitted under the rate limit.
func (l *Limiter) Allow(ip string) bool {
	if l == nil || l.rate == rate.Inf {
		return true
	}
	limiter := l.getClient(ip)
	return limiter.Allow()
}

// AllowRequest extracts the client IP from the HTTP request and checks if it is permitted.
func (l *Limiter) AllowRequest(r *http.Request) bool {
	if l == nil {
		return true
	}
	ip := ExtractClientIP(r)
	return l.Allow(ip)
}

// Middleware returns an HTTP middleware handler that wraps next, enforcing per-IP rate limits.
func (l *Limiter) Middleware(next http.Handler, onRejected ...func()) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.AllowRequest(r) {
			if len(onRejected) > 0 && onRejected[0] != nil {
				onRejected[0]()
			}
			w.Header().Set("Retry-After", l.RetryAfterHeader())
			http.Error(w, "429 Too Many Requests: rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Stop shuts down the background cleanup goroutine.
func (l *Limiter) Stop() {
	if l == nil {
		return
	}
	l.stopOnce.Do(func() {
		close(l.stopCh)
	})
}

// ClientCount returns the number of active tracked client IPs.
func (l *Limiter) ClientCount() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

// RequestsPerMinute returns the configured requests per minute.
func (l *Limiter) RequestsPerMinute() int {
	if l == nil {
		return 0
	}
	return l.requestsPerMin
}

// Burst returns the configured burst capacity.
func (l *Limiter) Burst() int {
	if l == nil {
		return 0
	}
	return l.burst
}

// RetryAfterHeader calculates the Retry-After header duration in seconds.
func (l *Limiter) RetryAfterHeader() string {
	if l == nil || l.requestsPerMin <= 0 {
		return "1"
	}
	secs := int(math.Ceil(60.0 / float64(l.requestsPerMin)))
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// CleanupStale removes tracked client IPs that have not been seen within the given ttl of now.
// Returns the number of clients removed.
func (l *Limiter) CleanupStale(now time.Time, ttl time.Duration) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	removed := 0
	for ip, c := range l.clients {
		if now.Sub(c.lastSeen) > ttl {
			delete(l.clients, ip)
			removed++
		}
	}
	return removed
}

func (l *Limiter) cleanupLoop() {
	ticker := time.NewTicker(l.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case now := <-ticker.C:
			l.CleanupStale(now, l.ttl)
		}
	}
}

func (l *Limiter) getClient(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	c, exists := l.clients[ip]
	if !exists {
		limiter := rate.NewLimiter(l.rate, l.burst)
		l.clients[ip] = &client{
			limiter:  limiter,
			lastSeen: now,
		}
		return limiter
	}

	c.lastSeen = now
	return c.limiter
}

// ExtractClientIP extracts the client IP address from request headers or RemoteAddr.
func ExtractClientIP(r *http.Request) string {
	if r == nil {
		return "127.0.0.1"
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		ip := strings.TrimSpace(parts[0])
		if ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		ip := strings.TrimSpace(xri)
		if ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "127.0.0.1"
}

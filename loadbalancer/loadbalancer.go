package loadbalancer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash/fnv"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/circuitbreaker"
	"github.com/aditya-rai-5/reverse_proxy/config"
)

// ErrNoHealthyBackend is returned when every backend is unhealthy or unavailable.
var ErrNoHealthyBackend = errors.New("no healthy backends available")

// backend wraps a parsed URL with its health state, weight, in-flight counters, and circuit breaker.
type backend struct {
	url               *url.URL
	healthy           atomic.Bool
	weight            int
	currentWeight     int64
	activeConnections atomic.Int64
	id                string
	cb                *circuitbreaker.CircuitBreaker
}

func (be *backend) isAvailable(excluded map[string]bool) bool {
	if excluded != nil && excluded[be.url.String()] {
		return false
	}
	return be.healthy.Load() && be.cb.AllowRequest()
}

// Result is returned by Select and includes the selected URL, an optional
// Done cleanup function (e.g. for decrementing active connections), and an
// optional HTTP Cookie to be set in the response (for sticky sessions).
type Result struct {
	URL    *url.URL
	Done   func()
	Cookie *http.Cookie
}

// Option configures a LoadBalancer.
type Option func(*LoadBalancer)

// WithAlgorithm sets the load balancing strategy.
func WithAlgorithm(algo string) Option {
	return func(lb *LoadBalancer) {
		lb.algorithm = algo
	}
}

// WithCookieName sets the cookie name used for sticky sessions.
func WithCookieName(name string) Option {
	return func(lb *LoadBalancer) {
		lb.cookieName = name
	}
}

// WithCircuitBreaker sets the circuit breaker configuration for backends.
func WithCircuitBreaker(cfg circuitbreaker.Config) Option {
	return func(lb *LoadBalancer) {
		lb.cbConfig = cfg
	}
}

// LoadBalancer distributes requests across backends using configurable algorithms:
// Round-Robin, Weighted Round-Robin, Least Connections, IP Hash, and Sticky Sessions,
// with integrated Circuit Breaker protection.
type LoadBalancer struct {
	backends   []*backend
	algorithm  string
	cookieName string
	cbConfig   circuitbreaker.Config
	counter    atomic.Uint64
	mu         sync.Mutex // guards currentWeight calculations in weighted round-robin
}

// New creates a LoadBalancer from the given config backends and optional configuration options.
// All backends start as healthy so traffic flows immediately on startup.
func New(backends []config.Backend, opts ...Option) (*LoadBalancer, error) {
	bs := make([]*backend, 0, len(backends))

	for _, b := range backends {
		u, err := url.Parse(b.URL)
		if err != nil {
			return nil, err
		}
		w := b.Weight
		if w <= 0 {
			w = 1
		}
		be := &backend{
			url:    u,
			weight: w,
			id:     backendID(u),
		}
		be.healthy.Store(true)
		bs = append(bs, be)
	}

	lb := &LoadBalancer{
		backends:   bs,
		algorithm:  config.AlgorithmRoundRobin,
		cookieName: "SERVERID",
		cbConfig: circuitbreaker.Config{
			FailureThreshold: 5,
			Cooldown:         10 * time.Second,
		},
	}

	for _, opt := range opts {
		opt(lb)
	}

	// Normalize algorithm
	if norm, err := config.NormalizeAlgorithm(lb.algorithm); err == nil {
		lb.algorithm = norm
	}

	if lb.cookieName == "" {
		lb.cookieName = "SERVERID"
	}

	for _, be := range bs {
		be.cb = circuitbreaker.New(lb.cbConfig)
	}

	return lb, nil
}

// Select routes a request to an appropriate backend according to the configured algorithm.
func (lb *LoadBalancer) Select(req *http.Request) (*Result, error) {
	return lb.SelectExcluding(req, nil)
}

// SelectExcluding routes a request to an appropriate backend, excluding any URLs present in excluded.
func (lb *LoadBalancer) SelectExcluding(req *http.Request, excluded map[string]bool) (*Result, error) {
	switch lb.algorithm {
	case config.AlgorithmWeightedRoundRobin:
		return lb.nextWeightedRoundRobin(excluded)
	case config.AlgorithmLeastConn:
		return lb.nextLeastConn(excluded)
	case config.AlgorithmIPHash:
		return lb.nextIPHash(req, excluded)
	case config.AlgorithmSticky:
		return lb.nextSticky(req, excluded)
	case config.AlgorithmRoundRobin:
		fallthrough
	default:
		return lb.nextRoundRobin(excluded)
	}
}

// Next returns the next healthy backend URL using the configured algorithm.
// If the selected algorithm provides a cleanup function (such as Least Connections),
// Next calls it immediately.
func (lb *LoadBalancer) Next() (*url.URL, error) {
	res, err := lb.Select(nil)
	if err != nil {
		return nil, err
	}
	if res.Done != nil {
		res.Done()
	}
	return res.URL, nil
}

func (lb *LoadBalancer) nextRoundRobin(excluded map[string]bool) (*Result, error) {
	n := uint64(len(lb.backends))
	if n == 0 {
		return nil, ErrNoHealthyBackend
	}

	start := lb.counter.Add(1) - 1
	for i := uint64(0); i < n; i++ {
		be := lb.backends[(start+i)%n]
		if be.isAvailable(excluded) {
			return &Result{URL: be.url}, nil
		}
	}

	return nil, ErrNoHealthyBackend
}

// nextWeightedRoundRobin implements Smooth Weighted Round-Robin (Nginx algorithm).
func (lb *LoadBalancer) nextWeightedRoundRobin(excluded map[string]bool) (*Result, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	var totalWeight int64
	var chosen *backend

	for _, be := range lb.backends {
		if !be.isAvailable(excluded) {
			continue
		}
		be.currentWeight += int64(be.weight)
		totalWeight += int64(be.weight)

		if chosen == nil || be.currentWeight > chosen.currentWeight {
			chosen = be
		}
	}

	if chosen == nil {
		return nil, ErrNoHealthyBackend
	}

	chosen.currentWeight -= totalWeight
	return &Result{URL: chosen.url}, nil
}

// nextLeastConn routes to the healthy backend with the lowest active in-flight requests.
func (lb *LoadBalancer) nextLeastConn(excluded map[string]bool) (*Result, error) {
	n := len(lb.backends)
	if n == 0 {
		return nil, ErrNoHealthyBackend
	}

	var chosen *backend
	minConns := int64(math.MaxInt64)
	start := int(lb.counter.Add(1)-1) % n

	for i := 0; i < n; i++ {
		idx := (start + i) % n
		be := lb.backends[idx]
		if !be.isAvailable(excluded) {
			continue
		}
		conns := be.activeConnections.Load()
		if conns < minConns {
			minConns = conns
			chosen = be
		}
	}

	if chosen == nil {
		return nil, ErrNoHealthyBackend
	}

	chosen.activeConnections.Add(1)
	done := func() {
		chosen.activeConnections.Add(-1)
	}

	return &Result{
		URL:  chosen.url,
		Done: done,
	}, nil
}

// nextIPHash hashes the client IP address to deterministically pin client requests to a backend.
func (lb *LoadBalancer) nextIPHash(req *http.Request, excluded map[string]bool) (*Result, error) {
	var healthy []*backend
	for _, be := range lb.backends {
		if be.isAvailable(excluded) {
			healthy = append(healthy, be)
		}
	}

	if len(healthy) == 0 {
		return nil, ErrNoHealthyBackend
	}

	ip := extractClientIP(req)
	h := fnv.New32a()
	_, _ = h.Write([]byte(ip))
	hashVal := h.Sum32()

	chosen := healthy[hashVal%uint32(len(healthy))]
	return &Result{URL: chosen.url}, nil
}

// nextSticky uses a session cookie to maintain affinity to the chosen backend.
func (lb *LoadBalancer) nextSticky(req *http.Request, excluded map[string]bool) (*Result, error) {
	if req != nil {
		cookie, err := req.Cookie(lb.cookieName)
		if err == nil && cookie.Value != "" {
			for _, be := range lb.backends {
				if be.id == cookie.Value {
					if be.isAvailable(excluded) {
						return &Result{URL: be.url}, nil
					}
					// Previously assigned backend is unavailable or excluded; re-elect below
					break
				}
			}
		}
	}

	// No valid cookie or sticky server is unavailable: choose via round-robin
	res, err := lb.nextRoundRobin(excluded)
	if err != nil {
		return nil, err
	}

	// Find the chosen backend's id to construct the cookie
	var chosenID string
	for _, be := range lb.backends {
		if be.url.String() == res.URL.String() {
			chosenID = be.id
			break
		}
	}

	res.Cookie = &http.Cookie{
		Name:     lb.cookieName,
		Value:    chosenID,
		Path:     "/",
		HttpOnly: true,
	}

	return res, nil
}

// RecordSuccess records a successful request for the given backend URL.
func (lb *LoadBalancer) RecordSuccess(u *url.URL) {
	if u == nil {
		return
	}
	for _, be := range lb.backends {
		if be.url.String() == u.String() {
			be.cb.RecordSuccess()
			return
		}
	}
}

// RecordFailure records a failed request (e.g. 502, timeout, dial failure) for the given backend URL.
func (lb *LoadBalancer) RecordFailure(u *url.URL) {
	if u == nil {
		return
	}
	for _, be := range lb.backends {
		if be.url.String() == u.String() {
			be.cb.RecordFailure()
			return
		}
	}
}

// CircuitBreakerState returns the circuit breaker state of the backend at index idx.
func (lb *LoadBalancer) CircuitBreakerState(idx int) circuitbreaker.State {
	return lb.backends[idx].cb.State()
}

// ConsecutiveFailures returns the consecutive failure count of the backend at index idx.
func (lb *LoadBalancer) ConsecutiveFailures(idx int) int {
	return lb.backends[idx].cb.ConsecutiveFailures()
}

// MarkHealthy sets the health state of the backend at the given index.
func (lb *LoadBalancer) MarkHealthy(idx int, healthy bool) {
	lb.backends[idx].healthy.Store(healthy)
	if healthy {
		lb.backends[idx].cb.RecordSuccess()
	}
}

// IsHealthy reports the current health state of the backend at index idx.
func (lb *LoadBalancer) IsHealthy(idx int) bool {
	return lb.backends[idx].healthy.Load()
}

// BackendURL returns the URL of the backend at index idx.
func (lb *LoadBalancer) BackendURL(idx int) *url.URL {
	return lb.backends[idx].url
}

// Len returns the total number of configured backends.
func (lb *LoadBalancer) Len() int {
	return len(lb.backends)
}

// Algorithm returns the configured load balancing algorithm.
func (lb *LoadBalancer) Algorithm() string {
	return lb.algorithm
}

// Weight returns the configured weight of the backend at index idx.
func (lb *LoadBalancer) Weight(idx int) int {
	return lb.backends[idx].weight
}

// ActiveConnections returns the current active in-flight request count for the backend at index idx.
func (lb *LoadBalancer) ActiveConnections(idx int) int64 {
	return lb.backends[idx].activeConnections.Load()
}

func backendID(u *url.URL) string {
	h := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(h[:8])
}

func extractClientIP(r *http.Request) string {
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

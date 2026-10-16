package router

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/circuitbreaker"
	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
	"github.com/aditya-rai-5/reverse_proxy/ratelimit"
)

type Entry struct {
	Path         string
	StripPrefix  string
	LB           *loadbalancer.LoadBalancer
	Retries      int
	RetryMethods []string
	Timeout      time.Duration
	RateLimiter  *ratelimit.Limiter
	rewriteRe    *regexp.Regexp
	replacement  string
}

// CanRetry requires route opt-in and a single nonempty Idempotency-Key for write
// methods.
func (e *Entry) CanRetry(req *http.Request) bool {
	if req == nil {
		return false
	}
	for _, method := range e.RetryMethods {
		if method != req.Method {
			continue
		}
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			return true
		default:
			keys := req.Header.Values("Idempotency-Key")
			return len(keys) == 1 && strings.TrimSpace(keys[0]) != ""
		}
	}
	return false
}

// Next selects the next healthy backend from the entry's load balancer.
func (e *Entry) Next() (*url.URL, error) {
	return e.LB.Next()
}

// Select routes a request through the entry's load balancer according to its algorithm.
func (e *Entry) Select(req *http.Request) (*loadbalancer.Result, error) {
	return e.LB.Select(req)
}

// SelectExcluding routes a request through the entry's load balancer, excluding any URLs in excluded.
func (e *Entry) SelectExcluding(req *http.Request, excluded map[string]bool) (*loadbalancer.Result, error) {
	return e.LB.SelectExcluding(req, excluded)
}

// RewritePath applies configured prefix stripping and regex rewriting to requestPath.
// The returned path is guaranteed to begin with '/'.
func (e *Entry) RewritePath(requestPath string) string {
	p := requestPath

	// 1. Strip prefix if configured
	if e.StripPrefix != "" && strings.HasPrefix(p, e.StripPrefix) {
		p = strings.TrimPrefix(p, e.StripPrefix)
		if p == "" || p[0] != '/' {
			p = "/" + p
		}
	}

	// 2. Regex rewrite if configured
	if e.rewriteRe != nil {
		p = e.rewriteRe.ReplaceAllString(p, e.replacement)
		if p == "" || p[0] != '/' {
			p = "/" + p
		}
	}

	return p
}

type Router struct {
	routes []Entry
}

func New(cfg *config.Config) (*Router, error) {
	entries := make([]Entry, 0, len(cfg.Routes))

	for _, route := range cfg.Routes {
		// Also validate/default programmatic configs that bypass config.Load.
		retryMethods, err := config.NormalizeRetryMethods(route.RetryMethods)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.Path, err)
		}
		opts := []loadbalancer.Option{
			loadbalancer.WithAlgorithm(route.Algorithm),
			loadbalancer.WithCookieName(route.CookieName),
		}
		if route.CircuitBreaker != nil {
			opts = append(opts, loadbalancer.WithCircuitBreaker(circuitbreaker.Config{
				FailureThreshold: route.CircuitBreaker.FailureThreshold,
				Cooldown:         route.CircuitBreaker.Cooldown,
			}))
		}

		lb, err := loadbalancer.New(route.Backends, opts...)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.Path, err)
		}

		var rl *ratelimit.Limiter
		if route.RateLimit != nil && route.RateLimit.Enabled() {
			rl = ratelimit.New(ratelimit.Config{
				RequestsPerMinute: route.RateLimit.RequestsPerMinute,
				Burst:             route.RateLimit.Burst,
			})
		}

		entry := Entry{
			Path:         route.Path,
			StripPrefix:  route.StripPrefix,
			LB:           lb,
			Retries:      route.Retries,
			RetryMethods: retryMethods,
			Timeout:      route.Timeout,
			RateLimiter:  rl,
		}

		if route.Rewrite != nil {
			re, err := regexp.Compile(route.Rewrite.Pattern)
			if err != nil {
				return nil, fmt.Errorf("route %q: invalid rewrite pattern: %w", route.Path, err)
			}
			entry.rewriteRe = re
			entry.replacement = route.Rewrite.Replacement
		}

		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].Path) > len(entries[j].Path)
	})

	return &Router{routes: entries}, nil
}

func (r *Router) Match(requestPath string) (*Entry, error) {
	for i := range r.routes {
		if matchesPath(requestPath, r.routes[i].Path) {
			return &r.routes[i], nil
		}
	}
	return nil, fmt.Errorf("no route matched for path: %s", requestPath)
}

func (r *Router) Entries() []Entry {
	return r.routes
}

func matchesPath(requestPath, routePath string) bool {
	if !strings.HasPrefix(requestPath, routePath) {
		return false
	}
	if routePath == "/" || len(requestPath) == len(routePath) {
		return true
	}
	if strings.HasSuffix(routePath, "/") {
		return true
	}
	return requestPath[len(routePath)] == '/'
}

package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

// Hop-by-hop headers which should not be forwarded per RFC 7230.
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// Handler is the top-level HTTP handler for the reverse proxy.
type Handler struct {
	router    *router.Router
	transport http.RoundTripper
}

// New creates a Handler backed by the given router using standard production transport.
func New(r *router.Router) *Handler {
	return NewWithTransport(r, &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	})
}

// NewWithTransport creates a Handler with a custom RoundTripper (useful for testing).
func NewWithTransport(r *router.Router, transport http.RoundTripper) *Handler {
	return &Handler{
		router:    r,
		transport: transport,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, err := h.router.Match(r.URL.Path)
	if err != nil {
		log.Printf("[PROXY] no route for %s: %v", r.URL.Path, err)
		http.Error(w, "502 Bad Gateway: no upstream route", http.StatusBadGateway)
		return
	}

	if entry.RateLimiter != nil && !entry.RateLimiter.AllowRequest(r) {
		log.Printf("[PROXY] rate limit exceeded for route %s from %s", entry.Path, r.RemoteAddr)
		w.Header().Set("Retry-After", entry.RateLimiter.RetryAfterHeader())
		http.Error(w, "429 Too Many Requests: rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var bodyBytes []byte
	if r.Body != nil {
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			log.Printf("[PROXY] error reading request body: %v", err)
			http.Error(w, "400 Bad Request: failed to read request body", http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
	}

	maxAttempts := 1 + entry.Retries
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	excluded := make(map[string]bool)
	var lastErr error
	var lastStatusCode int

	for attempt := 0; attempt < maxAttempts; attempt++ {
		result, err := entry.SelectExcluding(r, excluded)
		if err != nil {
			if errors.Is(err, loadbalancer.ErrNoHealthyBackend) {
				if attempt == 0 {
					log.Printf("[PROXY] all backends unhealthy for %s", r.URL.Path)
					http.Error(w, "503 Service Unavailable: all backends are unhealthy", http.StatusServiceUnavailable)
					return
				}
				break
			}
			log.Printf("[PROXY] load balancer error for %s: %v", r.URL.Path, err)
			http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
			return
		}

		target := result.URL
		excluded[target.String()] = true

		success, statusCode, err := h.forward(w, r, entry, result, bodyBytes)
		if success {
			entry.LB.RecordSuccess(target)
			return
		}

		entry.LB.RecordFailure(target)
		lastErr = err
		lastStatusCode = statusCode
		log.Printf("[PROXY] attempt %d on %s failed (status=%d, err=%v), retrying on next backend...",
			attempt+1, target.Host, statusCode, err)
	}

	log.Printf("[PROXY] all retry attempts failed for %s: lastErr=%v, lastStatus=%d", r.URL.Path, lastErr, lastStatusCode)
	if lastStatusCode == http.StatusGatewayTimeout {
		http.Error(w, "504 Gateway Timeout: all upstream backends timed out", http.StatusGatewayTimeout)
	} else {
		http.Error(w, "502 Bad Gateway: upstream error", http.StatusBadGateway)
	}
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request, entry *router.Entry, result *loadbalancer.Result, bodyBytes []byte) (bool, int, error) {
	if result.Done != nil {
		defer result.Done()
	}

	target := result.URL
	rewrittenPath := entry.RewritePath(r.URL.Path)

	ctx := r.Context()
	var cancel context.CancelFunc
	if entry.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, entry.Timeout)
		defer cancel()
	}

	outReq := r.Clone(ctx)
	outReq.URL.Scheme = target.Scheme
	outReq.URL.Host = target.Host
	outReq.URL.Path = rewrittenPath
	outReq.URL.RawPath = ""
	outReq.RequestURI = ""

	if len(bodyBytes) > 0 {
		outReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		outReq.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bodyBytes)), nil
		}
	} else {
		outReq.Body = nil
	}

	removeHopByHopHeaders(outReq.Header)

	outReq.Header.Set("X-Forwarded-Host", r.Host)
	outReq.Header.Set("X-Forwarded-For", r.RemoteAddr)

	log.Printf("[PROXY] %s %s → %s%s", outReq.Method, r.URL.Path, target.Host, outReq.URL.Path)

	resp, err := h.transport.RoundTrip(outReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return false, http.StatusGatewayTimeout, err
		}
		return false, http.StatusBadGateway, err
	}

	if resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout {
		_ = resp.Body.Close()
		return false, resp.StatusCode, fmt.Errorf("upstream returned HTTP %d", resp.StatusCode)
	}

	defer resp.Body.Close()

	if result.Cookie != nil {
		http.SetCookie(w, result.Cookie)
	}

	removeHopByHopHeaders(resp.Header)
	copyHeader(w.Header(), resp.Header)

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	return true, resp.StatusCode, nil
}

func removeHopByHopHeaders(h http.Header) {
	for _, header := range hopByHopHeaders {
		h.Del(header)
	}
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

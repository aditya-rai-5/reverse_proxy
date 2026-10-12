package proxy

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"

	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

// Handler is the top-level HTTP handler for the reverse proxy.
type Handler struct {
	router *router.Router
}

// New creates a Handler backed by the given router.
func New(r *router.Router) *Handler {
	return &Handler{router: r}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, err := h.router.Match(r.URL.Path)
	if err != nil {
		log.Printf("[PROXY] no route for %s: %v", r.URL.Path, err)
		http.Error(w, "502 Bad Gateway: no upstream route", http.StatusBadGateway)
		return
	}

	result, err := entry.Select(r)
	if err != nil {
		if errors.Is(err, loadbalancer.ErrNoHealthyBackend) {
			log.Printf("[PROXY] all backends unhealthy for %s", r.URL.Path)
			http.Error(w, "503 Service Unavailable: all backends are unhealthy", http.StatusServiceUnavailable)
			return
		}
		log.Printf("[PROXY] load balancer error for %s: %v", r.URL.Path, err)
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
		return
	}
	if result.Done != nil {
		defer result.Done()
	}
	if result.Cookie != nil {
		http.SetCookie(w, result.Cookie)
	}

	target := result.URL
	rewrittenPath := entry.RewritePath(r.URL.Path)

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Rewrite the request URL to point to the chosen backend.
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = rewrittenPath
			req.URL.RawPath = ""

			// Preserve the original Host header so backends can use it.
			req.Header.Set("X-Forwarded-Host", req.Host)

			// Pass the real client IP to the backend.
			req.Header.Set("X-Forwarded-For", r.RemoteAddr)

			log.Printf("[PROXY] %s %s → %s%s", req.Method, r.URL.Path, target.Host, req.URL.Path)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[PROXY] upstream error for %s: %v", r.URL.Path, err)
			http.Error(w, "502 Bad Gateway: upstream error", http.StatusBadGateway)
		},
	}

	proxy.ServeHTTP(w, r)
}

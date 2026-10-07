package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

type Handler struct {
	router *router.Router
}

func New(r *router.Router) *Handler {
	return &Handler{router: r}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	lb, err := h.router.Match(r.URL.Path)
	if err != nil {
		log.Printf("[PROXY] No route for %s: %v", r.URL.Path, err)
		http.Error(w, "502 Bad Gateway: no upstream route", http.StatusBadGateway)
		return
	}

	target := lb.Next()

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Rewrite the request URL to point to the chosen backend
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host

			// Preserve the original Host header so backends can use it
			req.Header.Set("X-Forwarded-Host", req.Host)

			// Pass the real client IP to the backend
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

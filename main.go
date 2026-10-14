package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/healthcheck"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/proxy"
	"github.com/aditya-rai-5/reverse_proxy/ratelimit"
	"github.com/aditya-rai-5/reverse_proxy/router"
	"golang.org/x/crypto/acme/autocert"
)

// buildHandler creates the proxy handler and applies the global rate limiting middleware if configured.
func buildHandler(cfg *config.Config, r *router.Router, observers ...proxy.RequestObserver) http.Handler {
	proxyHandler := proxy.NewWithTransportAndBodyLimit(r, nil, cfg.MaxRequestBodyBytes)
	if len(observers) > 0 {
		proxyHandler.SetRequestObserver(observers[0])
	}
	var handler http.Handler = proxyHandler
	if cfg.RateLimit != nil && cfg.RateLimit.Enabled() {
		rl := ratelimit.New(ratelimit.Config{
			RequestsPerMinute: cfg.RateLimit.RequestsPerMinute,
			Burst:             cfg.RateLimit.Burst,
		})
		var onRejected func()
		if len(observers) > 0 {
			if rejectionObserver, ok := observers[0].(interface{ ObserveRateLimitRejection(string) }); ok {
				onRejected = func() { rejectionObserver.ObserveRateLimitRejection("global") }
			}
		}
		handler = rl.Middleware(handler, onRejected)
	}
	return handler
}

func main() {
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	r, err := router.New(cfg)
	if err != nil {
		log.Fatalf("failed to build router: %v", err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start health checkers for every route's backends.
	var hcWg sync.WaitGroup
	for _, entry := range r.Entries() {
		hc := healthcheck.New(entry.LB, entry.Path, cfg.HealthCheck)
		hc.Start(ctx, &hcWg)
	}

	proxyMetrics := metrics.New(r)
	handler := buildHandler(cfg, r, proxyMetrics)

	mux := http.NewServeMux()
	mux.Handle("/metrics", proxyMetrics.Handler())
	mux.Handle("/", handler)
	mux.HandleFunc("/healthz", makeHealthzHandler(r, cfg))

	servers, err := newServers(cfg, mux)
	if err != nil {
		log.Fatalf("failed to configure listeners: %v", err)
	}
	for _, srv := range servers.all() {
		protocol := "HTTP"
		if srv.TLSConfig != nil {
			protocol = "HTTPS"
		}
		log.Printf("Reverse proxy %s listener on %s", protocol, srv.Addr)
		go func(srv *http.Server) {
			if err := serve(srv); err != nil && err != http.ErrServerClosed {
				log.Printf("server error on %s: %v", srv.Addr, err)
				cancel()
			}
		}(srv)
	}
	log.Printf("Health check: path=%s interval=%s timeout=%s",
		cfg.HealthCheck.Path, cfg.HealthCheck.Interval, cfg.HealthCheck.Timeout)

	<-ctx.Done()
	log.Println("Shutting down…")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, srv := range servers.all() {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown error for %s: %v", srv.Addr, err)
		}
	}

	hcWg.Wait()
	log.Println("All health checkers stopped. Bye!")
}

type serverPair struct {
	app         *http.Server
	redirect    *http.Server
	certManager *autocert.Manager
}

func (s *serverPair) all() []*http.Server {
	servers := []*http.Server{s.app}
	if s.redirect != nil {
		servers = append(servers, s.redirect)
	}
	return servers
}

func newServers(cfg *config.Config, handler http.Handler) (*serverPair, error) {
	if cfg.TLS == nil {
		return &serverPair{app: &http.Server{
			Addr:    fmt.Sprintf(":%d", cfg.Port),
			Handler: handler,
		}}, nil
	}

	tlsCfg := cfg.TLS
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	var manager *autocert.Manager
	if tlsCfg.AutoCert {
		manager = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(tlsCfg.CacheDir),
			HostPolicy: autocert.HostWhitelist(tlsCfg.Domains...),
			Email:      tlsCfg.Email,
		}
		serverTLS = manager.TLSConfig()
		serverTLS.MinVersion = tls.VersionTLS12
	} else {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate/key: %w", err)
		}
		serverTLS.Certificates = []tls.Certificate{cert}
	}

	redirectHandler := httpsRedirectHandler(tlsCfg.HTTPSPort)
	if manager != nil {
		// autocert's HTTP handler serves HTTP-01 challenges before delegating
		// normal requests to our HTTPS redirect handler.
		redirectHandler = manager.HTTPHandler(redirectHandler)
	}

	return &serverPair{
		app: &http.Server{
			Addr:      fmt.Sprintf(":%d", tlsCfg.HTTPSPort),
			Handler:   handler,
			TLSConfig: serverTLS,
		},
		redirect: &http.Server{
			Addr:    fmt.Sprintf(":%d", tlsCfg.HTTPPort),
			Handler: redirectHandler,
		},
		certManager: manager,
	}, nil
}

func serve(srv *http.Server) error {
	if srv.TLSConfig != nil {
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

func httpsRedirectHandler(httpsPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := *r.URL
		target.Scheme = "https"
		target.Host = httpsAuthority(r.Host, httpsPort)
		http.Redirect(w, r, target.String(), http.StatusPermanentRedirect)
	})
}

func httpsAuthority(hostport string, httpsPort int) string {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(hostport, "]"), "[")
	}
	if httpsPort == 443 {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(httpsPort))
}

// backendStatus is the JSON shape returned by /healthz.
type backendStatus struct {
	URL                 string `json:"url"`
	Healthy             bool   `json:"healthy"`
	Weight              int    `json:"weight,omitempty"`
	ActiveConnections   int64  `json:"active_connections,omitempty"`
	CircuitBreakerState string `json:"circuit_breaker,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
}

type rateLimitStatus struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	Burst             int `json:"burst"`
}

type routeStatus struct {
	Path      string           `json:"path"`
	Algorithm string           `json:"algorithm,omitempty"`
	Retries   int              `json:"retries,omitempty"`
	RateLimit *rateLimitStatus `json:"rate_limit,omitempty"`
	Backends  []backendStatus  `json:"backends"`
}

// makeHealthzHandler returns an HTTP handler that reports the health of every
// backend across all routes as a JSON document.
func makeHealthzHandler(r *router.Router, cfg ...*config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var routes []routeStatus
		allHealthy := true

		for _, entry := range r.Entries() {
			var bs []backendStatus
			for i := 0; i < entry.LB.Len(); i++ {
				healthy := entry.LB.IsHealthy(i)
				cbState := string(entry.LB.CircuitBreakerState(i))
				if !healthy || cbState == "open" {
					allHealthy = false
				}
				bs = append(bs, backendStatus{
					URL:                 entry.LB.BackendURL(i).String(),
					Healthy:             healthy,
					Weight:              entry.LB.Weight(i),
					ActiveConnections:   entry.LB.ActiveConnections(i),
					CircuitBreakerState: cbState,
					ConsecutiveFailures: entry.LB.ConsecutiveFailures(i),
				})
			}
			rs := routeStatus{
				Path:      entry.Path,
				Algorithm: entry.LB.Algorithm(),
				Retries:   entry.Retries,
				Backends:  bs,
			}
			if entry.RateLimiter != nil {
				rs.RateLimit = &rateLimitStatus{
					RequestsPerMinute: entry.RateLimiter.RequestsPerMinute(),
					Burst:             entry.RateLimiter.Burst(),
				}
			}
			routes = append(routes, rs)
		}

		w.Header().Set("Content-Type", "application/json")
		if !allHealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		resp := map[string]any{
			"status": func() string {
				if allHealthy {
					return "healthy"
				}
				return "degraded"
			}(),
			"routes": routes,
		}
		if len(cfg) > 0 && cfg[0] != nil && cfg[0].RateLimit != nil && cfg[0].RateLimit.Enabled() {
			resp["rate_limit"] = cfg[0].RateLimit
		}
		json.NewEncoder(w).Encode(resp)
	}
}

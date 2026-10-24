package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/healthcheck"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/proxy"
	"github.com/aditya-rai-5/reverse_proxy/ratelimit"
	"github.com/aditya-rai-5/reverse_proxy/router"
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
			ClientIPResolver:  r.ClientIPResolver(),
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

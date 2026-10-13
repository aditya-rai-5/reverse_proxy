package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/healthcheck"
	"github.com/aditya-rai-5/reverse_proxy/proxy"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func main() {
	// Load config from YAML file.
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Build the router (path → load balancer mappings).
	r, err := router.New(cfg)
	if err != nil {
		log.Fatalf("failed to build router: %v", err)
	}

	// Context that is cancelled on SIGINT / SIGTERM for clean shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Start health checkers for every route's backends.
	var hcWg sync.WaitGroup
	for _, entry := range r.Entries() {
		hc := healthcheck.New(entry.LB, entry.Path, cfg.HealthCheck)
		hc.Start(ctx, &hcWg)
	}

	// Create the proxy handler.
	handler := proxy.New(r)

	// Register routes on the default mux.
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	mux.HandleFunc("/healthz", makeHealthzHandler(r))

	// Start the HTTP server.
	addr := fmt.Sprintf(":%d", cfg.Port)
	srv := &http.Server{Addr: addr, Handler: mux}

	log.Printf("Reverse proxy listening on %s (health check: path=%s interval=%s timeout=%s)",
		addr, cfg.HealthCheck.Path, cfg.HealthCheck.Interval, cfg.HealthCheck.Timeout)

	// Run server in a goroutine so we can block on ctx cancellation below.
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Wait for shutdown signal.
	<-ctx.Done()
	log.Println("Shutting down…")

	// Give in-flight requests 10 s to complete.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}

	// Wait for all health-check goroutines to stop.
	hcWg.Wait()
	log.Println("All health checkers stopped. Bye!")
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

type routeStatus struct {
	Path      string          `json:"path"`
	Algorithm string          `json:"algorithm,omitempty"`
	Retries   int             `json:"retries,omitempty"`
	Backends  []backendStatus `json:"backends"`
}

// makeHealthzHandler returns an HTTP handler that reports the health of every
// backend across all routes as a JSON document.
func makeHealthzHandler(r *router.Router) http.HandlerFunc {
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
			routes = append(routes, routeStatus{
				Path:      entry.Path,
				Algorithm: entry.LB.Algorithm(),
				Retries:   entry.Retries,
				Backends:  bs,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		if !allHealthy {
			w.WriteHeader(http.StatusServiceUnavailable) // 503 so monitors can alert
		}
		json.NewEncoder(w).Encode(map[string]any{
			"status": func() string {
				if allHealthy {
					return "healthy"
				}
				return "degraded"
			}(),
			"routes": routes,
		})
	}
}

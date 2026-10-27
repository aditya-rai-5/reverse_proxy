package main

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/healthcheck"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

type generation struct {
	cfg        *config.Config
	handler    http.Handler
	management http.Handler
	cancel     context.CancelFunc
	probes     sync.WaitGroup
	requests   sync.WaitGroup
	cleanup    func()
}

func newGeneration(ctx context.Context, cfg *config.Config) (*generation, error) {
	r, err := router.New(cfg)
	if err != nil {
		return nil, err
	}
	m := metrics.New(r)
	handler, cleanup := buildManagedHandler(cfg, r, m)
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	mux.HandleFunc("/healthz", makeHealthzHandler(r, cfg))
	mux.HandleFunc("/readyz", readinessHandler(r))
	ctx, cancel := context.WithCancel(ctx)
	g := &generation{cfg: cfg, handler: handler, management: mux, cancel: cancel, cleanup: func() {
		cleanup()
		for _, entry := range r.Entries() {
			entry.RateLimiter.Stop()
		}
	}}
	for _, entry := range r.Entries() {
		healthcheck.New(entry.LB, entry.Path, cfg.HealthCheck).Start(ctx, &g.probes)
	}
	return g, nil
}

// runtimeHandler selects one complete configuration for each request.
type runtimeHandler struct {
	ctx     context.Context
	mu      sync.Mutex
	current *generation
	retired sync.WaitGroup
}

func newRuntime(ctx context.Context, cfg *config.Config) (*runtimeHandler, error) {
	g, err := newGeneration(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &runtimeHandler{ctx: ctx, current: g}, nil
}

func (r *runtimeHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.serveGeneration(w, req, false)
}

func (r *runtimeHandler) serveManagement(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/livez" {
		w.Write([]byte("alive\n"))
		return
	}
	if req.URL.Path == "/readyz" && r.ctx.Err() != nil {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	r.serveGeneration(w, req, true)
}

func (r *runtimeHandler) serveGeneration(w http.ResponseWriter, req *http.Request, management bool) {
	r.mu.Lock()
	g := r.current
	if g != nil {
		g.requests.Add(1)
	}
	r.mu.Unlock()
	if g == nil {
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	defer g.requests.Done()
	if management {
		g.management.ServeHTTP(w, req)
	} else {
		g.handler.ServeHTTP(w, req)
	}
}

// reload and close are serialized by the main signal loop.
func (r *runtimeHandler) reload(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old := r.current
	r.mu.Unlock()
	if old == nil {
		return fmt.Errorf("runtime is closed")
	}
	if cfg.Port != old.cfg.Port || !reflect.DeepEqual(cfg.TLS, old.cfg.TLS) || !reflect.DeepEqual(cfg.Management, old.cfg.Management) {
		return fmt.Errorf("listener, management, and TLS changes require a restart")
	}
	next, err := newGeneration(r.ctx, cfg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.current = next
	r.mu.Unlock()
	r.retire(old)
	return nil
}

func (r *runtimeHandler) retire(g *generation) {
	g.cancel()
	r.retired.Add(1)
	go func() {
		defer r.retired.Done()
		g.probes.Wait()
		g.requests.Wait()
		g.cleanup()
	}()
}

func (r *runtimeHandler) close() {
	r.mu.Lock()
	old := r.current
	r.current = nil
	r.mu.Unlock()
	if old != nil {
		r.retire(old)
	}
	r.retired.Wait()
}

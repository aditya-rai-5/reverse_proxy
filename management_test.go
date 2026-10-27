package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestManagementAccessUsesDirectPeer(t *testing.T) {
	handler := managementAccess(config.Management{AllowedCIDRs: []string{"127.0.0.0/8", "::1/128"}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for _, tc := range []struct {
		peer   string
		status int
	}{
		{"127.0.0.1:1234", 204}, {"[::1]:1234", 204}, {"[::ffff:127.0.0.1]:1234", 204},
		{"192.0.2.1:1234", 403}, {"bad-address", 403},
	} {
		req := httptest.NewRequest("GET", "/metrics", nil)
		req.RemoteAddr = tc.peer
		req.Header.Set("X-Forwarded-For", "127.0.0.1")
		req.Header.Set("Forwarded", "for=127.0.0.1")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("peer %s: got %d", tc.peer, w.Code)
		}
	}
}

func TestLivenessReadinessAndApplicationIsolation(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Path: "/api", Backends: []config.Backend{{URL: "http://one"}, {URL: "http://two"}}, CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Cooldown: time.Hour}},
		{Path: "/other", Backends: []config.Backend{{URL: "http://three"}}},
	}}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("/readyz", readinessHandler(r))
	mux.HandleFunc("/healthz", makeHealthzHandler(r, cfg))
	runtime := &runtimeHandler{ctx: ctx, current: &generation{handler: http.NotFoundHandler(), management: mux}}
	check := func(path string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		runtime.serveManagement(w, httptest.NewRequest("GET", path, nil))
		if w.Code != want {
			t.Fatalf("%s: got %d want %d", path, w.Code, want)
		}
	}
	check("/livez", 200)
	check("/readyz", 503) // No probe results yet.
	api, _ := r.Match("/api")
	other, _ := r.Match("/other")
	api.LB.MarkHealthy(0, true)
	api.LB.MarkHealthy(1, false)
	check("/readyz", 503) // Another route still has no usable backend.
	other.LB.MarkHealthy(0, true)
	check("/readyz", 200)  // One healthy replica is sufficient.
	check("/healthz", 503) // Detailed diagnostic remains degraded.
	api.LB.RecordFailure(api.LB.BackendURL(0))
	check("/readyz", 503)
	check("/livez", 200)
	api.LB.MarkHealthy(1, true)
	check("/readyz", 200)
	cancel()
	check("/readyz", 503)
	check("/livez", 200)
	for _, path := range []string{"/livez", "/readyz", "/healthz", "/metrics"} {
		w := httptest.NewRecorder()
		runtime.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("management endpoint exposed on application: %s", path)
		}
	}
}

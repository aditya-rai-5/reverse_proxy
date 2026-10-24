package healthcheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/circuitbreaker"
	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
)

func TestProbeHealthyBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	lb, err := loadbalancer.New([]config.Backend{{URL: server.URL}})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	cfg := config.HealthCheck{
		Path:     "/health",
		Interval: 100 * time.Millisecond,
		Timeout:  1 * time.Second,
	}

	checker := New(lb, "/test", cfg)

	// Run probe directly
	checker.probe(0, server.URL+"/health")

	if !lb.IsHealthy(0) {
		t.Errorf("Expected backend to be healthy after 200 OK")
	}
}

func TestProbeUnhealthyBackendHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	lb, err := loadbalancer.New([]config.Backend{{URL: server.URL}})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	cfg := config.HealthCheck{
		Path:     "/health",
		Interval: 100 * time.Millisecond,
		Timeout:  1 * time.Second,
	}

	checker := New(lb, "/test", cfg)

	checker.probe(0, server.URL+"/health")

	if lb.IsHealthy(0) {
		t.Errorf("Expected backend to be unhealthy after HTTP 500")
	}
}

func TestProbeUnreachableBackend(t *testing.T) {
	// Point to closed server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := server.URL
	server.Close()

	lb, err := loadbalancer.New([]config.Backend{{URL: closedURL}})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	cfg := config.HealthCheck{
		Path:     "/health",
		Interval: 100 * time.Millisecond,
		Timeout:  500 * time.Millisecond,
	}

	checker := New(lb, "/test", cfg)

	checker.probe(0, closedURL+"/health")

	if lb.IsHealthy(0) {
		t.Errorf("Expected backend to be unhealthy when unreachable")
	}
}

func TestProbeRecovery(t *testing.T) {
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	lb, err := loadbalancer.New([]config.Backend{{URL: server.URL}})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	cfg := config.HealthCheck{
		Path:     "/health",
		Interval: 50 * time.Millisecond,
		Timeout:  1 * time.Second,
	}

	checker := New(lb, "/test", cfg)

	// Step 1: Server is failing
	healthy.Store(false)
	checker.probe(0, server.URL+"/health")
	if lb.IsHealthy(0) {
		t.Fatalf("Expected backend to be marked unhealthy")
	}

	// Step 2: Server recovers
	healthy.Store(true)
	checker.probe(0, server.URL+"/health")
	if !lb.IsHealthy(0) {
		t.Fatalf("Expected backend to recover and be marked healthy")
	}
}

func TestHealthCheckerStartLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	lb, err := loadbalancer.New([]config.Backend{{URL: server.URL}})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	cfg := config.HealthCheck{
		Path:     "/health",
		Interval: 20 * time.Millisecond,
		Timeout:  500 * time.Millisecond,
	}

	checker := New(lb, "/test", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	checker.Start(ctx, &wg)

	// Wait briefly for initial probe to run
	time.Sleep(50 * time.Millisecond)

	if !lb.IsHealthy(0) {
		t.Errorf("Expected backend to be healthy after start")
	}

	// Cancel context and verify all probe goroutines stop
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Health check goroutines did not exit after context cancellation")
	}
}

func TestBuildProbeURL(t *testing.T) {
	parsed, err := url.Parse("http://localhost:8080")
	if err != nil {
		t.Fatalf("url.Parse error: %v", err)
	}

	probeURL := buildProbeURL(parsed, "/ping")
	expected := "http://localhost:8080/ping"
	if probeURL != expected {
		t.Errorf("buildProbeURL() = %q, expected %q", probeURL, expected)
	}
}

func TestProbeHealthyBackendPreservesOpenCircuit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	const failures = 3
	lb, err := loadbalancer.New([]config.Backend{{URL: server.URL}}, loadbalancer.WithCircuitBreaker(circuitbreaker.Config{
		FailureThreshold: failures,
		Cooldown:         time.Hour,
	}))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}
	for i := 0; i < failures; i++ {
		lb.RecordFailure(lb.BackendURL(0))
	}
	if got := lb.CircuitBreakerState(0); got != circuitbreaker.StateOpen {
		t.Fatalf("Expected open circuit before probe, got %s", got)
	}
	lb.MarkHealthy(0, false)

	checker := New(lb, "/test", config.HealthCheck{
		Path:    "/health",
		Timeout: time.Second,
	})
	checker.probe(0, server.URL+"/health")

	if !lb.IsHealthy(0) {
		t.Error("Expected backend to be probe-healthy after HTTP 200")
	}
	if got := lb.CircuitBreakerState(0); got != circuitbreaker.StateOpen {
		t.Errorf("Expected successful probe to preserve open circuit, got %s", got)
	}
	if got := lb.ConsecutiveFailures(0); got != failures {
		t.Errorf("Expected %d application failures to be preserved, got %d", failures, got)
	}
	if _, err := lb.Select(nil); !errors.Is(err, loadbalancer.ErrNoHealthyBackend) {
		t.Errorf("Expected open circuit to prevent selection before cooldown, got %v", err)
	}
}

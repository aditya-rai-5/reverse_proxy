package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxyForwarding(t *testing.T) {
	var receivedHost, receivedForwardedFor, receivedForwardedHost string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		receivedForwardedFor = r.Header.Get("X-Forwarded-For")
		receivedForwardedHost = r.Header.Get("X-Forwarded-Host")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response from backend"))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:     "/api",
				Backends: []config.Backend{{URL: backend.URL}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	if string(body) != "response from backend" {
		t.Errorf("Unexpected body: %q", string(body))
	}

	if receivedForwardedHost != "example.com" {
		t.Errorf("Expected X-Forwarded-Host 'example.com', got %q", receivedForwardedHost)
	}

	if receivedForwardedFor == "" {
		t.Errorf("Expected X-Forwarded-For to be set, got empty")
	}

	_ = receivedHost
}

func TestProxyNoRoute(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:     "/api",
				Backends: []config.Backend{{URL: "http://localhost:9001"}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "/unknown", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected status 502, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "502 Bad Gateway") {
		t.Errorf("Expected 502 message in body, got %q", string(body))
	}
}

func TestProxyAllBackendsUnhealthy(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:     "/api",
				Backends: []config.Backend{{URL: "http://localhost:9001"}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Mark the only backend as unhealthy
	entries := r.Entries()
	entries[0].LB.MarkHealthy(0, false)

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", res.StatusCode)
	}

	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "503 Service Unavailable") {
		t.Errorf("Expected 503 message in body, got %q", string(body))
	}
}

func TestProxyUpstreamError(t *testing.T) {
	// Point to a server that is immediately closed so connection fails
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	backendURL := backend.URL
	backend.Close() // closed immediately

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:     "/api",
				Backends: []config.Backend{{URL: backendURL}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("Expected status 502 upstream error, got %d", res.StatusCode)
	}
}

func TestProxyRoundRobin(t *testing.T) {
	var count1, count2 atomic.Int64

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path: "/",
				Backends: []config.Backend{
					{URL: backend1.URL},
					{URL: backend2.URL},
				},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/hello", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	if count1.Load() != 2 {
		t.Errorf("Expected backend1 to receive 2 requests, got %d", count1.Load())
	}
	if count2.Load() != 2 {
		t.Errorf("Expected backend2 to receive 2 requests, got %d", count2.Load())
	}
}

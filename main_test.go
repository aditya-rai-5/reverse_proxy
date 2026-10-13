package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestHealthzHandlerAllHealthy(t *testing.T) {
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

	handler := makeHealthzHandler(r)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	var data map[string]any
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data["status"] != "healthy" {
		t.Errorf("Expected status 'healthy', got %v", data["status"])
	}
}

func TestHealthzHandlerDegraded(t *testing.T) {
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

	// Mark backend unhealthy
	r.Entries()[0].LB.MarkHealthy(0, false)

	handler := makeHealthzHandler(r)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected status 503, got %d", res.StatusCode)
	}

	var data map[string]any
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data["status"] != "degraded" {
		t.Errorf("Expected status 'degraded', got %v", data["status"])
	}
}

func TestHealthzHandlerAlgorithmAndWeight(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:      "/heavy",
				Algorithm: "weighted_round_robin",
				Backends: []config.Backend{
					{URL: "http://localhost:9001", Weight: 2},
				},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := makeHealthzHandler(r)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler(rec, req)

	var data struct {
		Status string `json:"status"`
		Routes []struct {
			Path      string `json:"path"`
			Algorithm string `json:"algorithm"`
			Backends  []struct {
				URL               string `json:"url"`
				Healthy           bool   `json:"healthy"`
				Weight            int    `json:"weight"`
				ActiveConnections int64  `json:"active_connections"`
			} `json:"backends"`
		} `json:"routes"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data.Status != "healthy" {
		t.Errorf("Expected status 'healthy', got %v", data.Status)
	}
	if len(data.Routes) != 1 {
		t.Fatalf("Expected 1 route, got %d", len(data.Routes))
	}
	if data.Routes[0].Algorithm != config.AlgorithmWeightedRoundRobin {
		t.Errorf("Expected algorithm %q, got %q", config.AlgorithmWeightedRoundRobin, data.Routes[0].Algorithm)
	}
	if len(data.Routes[0].Backends) != 1 {
		t.Fatalf("Expected 1 backend, got %d", len(data.Routes[0].Backends))
	}
	if data.Routes[0].Backends[0].Weight != 2 {
		t.Errorf("Expected backend weight 2, got %d", data.Routes[0].Backends[0].Weight)
	}
}

func TestHealthzHandlerCircuitBreakerOpen(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path: "/",
				Backends: []config.Backend{
					{URL: "http://localhost:9001"},
				},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	// Trip circuit breaker by recording 5 failures
	u := r.Entries()[0].LB.BackendURL(0)
	for i := 0; i < 5; i++ {
		r.Entries()[0].LB.RecordFailure(u)
	}

	handler := makeHealthzHandler(r)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503 Service Unavailable when circuit breaker is open, got %d", rec.Code)
	}

	var data map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data["status"] != "degraded" {
		t.Errorf("Expected status 'degraded', got %v", data["status"])
	}
}

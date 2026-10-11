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

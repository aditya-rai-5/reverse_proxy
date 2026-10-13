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

func TestBuildHandlerRateLimiting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("backend response"))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		RateLimit: &config.RateLimit{
			RequestsPerMinute: 60,
			Burst:             2,
		},
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

	handler := buildHandler(cfg, r)

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/data", nil)
		req.RemoteAddr = "192.168.1.100:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d: expected 200 OK, got %d", i, rec.Code)
		}
	}

	req3 := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req3.RemoteAddr = "192.168.1.100:5678"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("Request 3: expected 429 Too Many Requests, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After header on rate limited response")
	}

	reqOther := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	reqOther.RemoteAddr = "10.0.0.99:1234"
	recOther := httptest.NewRecorder()
	handler.ServeHTTP(recOther, reqOther)

	if recOther.Code != http.StatusOK {
		t.Fatalf("Other IP: expected 200 OK, got %d", recOther.Code)
	}
}

func TestHealthzHandlerRateLimit(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		RateLimit: &config.RateLimit{
			RequestsPerMinute: 60,
			Burst:             10,
		},
		Routes: []config.Route{
			{
				Path: "/api",
				RateLimit: &config.RateLimit{
					RequestsPerMinute: 120,
					Burst:             20,
				},
				Backends: []config.Backend{{URL: "http://localhost:9001"}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := makeHealthzHandler(r, cfg)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	var data struct {
		Status    string `json:"status"`
		RateLimit struct {
			RequestsPerMinute int `json:"requests_per_minute"`
			Burst             int `json:"burst"`
		} `json:"rate_limit"`
		Routes []struct {
			Path      string `json:"path"`
			RateLimit *struct {
				RequestsPerMinute int `json:"requests_per_minute"`
				Burst             int `json:"burst"`
			} `json:"rate_limit"`
		} `json:"routes"`
	}

	if err := json.NewDecoder(rec.Body).Decode(&data); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if data.RateLimit.RequestsPerMinute != 60 || data.RateLimit.Burst != 10 {
		t.Errorf("Expected top-level rate limit 60/10, got %d/%d", data.RateLimit.RequestsPerMinute, data.RateLimit.Burst)
	}

	if len(data.Routes) != 1 || data.Routes[0].RateLimit == nil {
		t.Fatal("Expected route rate limit info")
	}
	if data.Routes[0].RateLimit.RequestsPerMinute != 120 || data.Routes[0].RateLimit.Burst != 20 {
		t.Errorf("Expected route rate limit 120/20, got %d/%d", data.Routes[0].RateLimit.RequestsPerMinute, data.Routes[0].RateLimit.Burst)
	}
}

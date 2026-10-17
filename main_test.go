package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

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

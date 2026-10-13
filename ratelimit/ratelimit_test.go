package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiterBurstAndRejection(t *testing.T) {
	rl := New(Config{
		RequestsPerMinute: 60,
		Burst:             3,
	})
	defer rl.Stop()

	clientIP := "192.168.1.10"

	for i := 1; i <= 3; i++ {
		if !rl.Allow(clientIP) {
			t.Fatalf("Request %d within burst was unexpectedly rejected", i)
		}
	}

	if rl.Allow(clientIP) {
		t.Fatal("Request 4 beyond burst was unexpectedly allowed")
	}
}

func TestRateLimiterPerIPIsolation(t *testing.T) {
	rl := New(Config{
		RequestsPerMinute: 60,
		Burst:             2,
	})
	defer rl.Stop()

	ip1 := "10.0.0.1"
	ip2 := "10.0.0.2"

	if !rl.Allow(ip1) || !rl.Allow(ip1) {
		t.Fatal("IP1 burst requests should be allowed")
	}
	if rl.Allow(ip1) {
		t.Fatal("IP1 request beyond burst should be rejected")
	}

	if !rl.Allow(ip2) {
		t.Fatal("IP2 should be allowed even if IP1 is exhausted")
	}
	if !rl.Allow(ip2) {
		t.Fatal("IP2 second request should be allowed")
	}
	if rl.Allow(ip2) {
		t.Fatal("IP2 third request should be rejected")
	}
}

func TestRateLimiterMiddleware(t *testing.T) {
	rl := New(Config{
		RequestsPerMinute: 60,
		Burst:             2,
	})
	defer rl.Stop()

	var downstreamCalls atomic.Int64
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downstreamCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	handler := rl.Middleware(next)

	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.RemoteAddr = "192.168.1.50:1234"
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Errorf("Request 1: expected 200 OK, got %d", rec1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.RemoteAddr = "192.168.1.50:5678"
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("Request 2: expected 200 OK, got %d", rec2.Code)
	}

	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req3.RemoteAddr = "192.168.1.50:9999"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3: expected 429 Too Many Requests, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After header on 429 response")
	}
	if rec3.Body.String() != "429 Too Many Requests: rate limit exceeded\n" {
		t.Errorf("Unexpected body: %q", rec3.Body.String())
	}

	if downstreamCalls.Load() != 2 {
		t.Errorf("Expected downstream to be called 2 times, got %d", downstreamCalls.Load())
	}
}

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		xri        string
		expected   string
	}{
		{
			name:       "RemoteAddr with port",
			remoteAddr: "192.168.1.1:12345",
			expected:   "192.168.1.1",
		},
		{
			name:       "RemoteAddr IPv6 with port",
			remoteAddr: "[::1]:54321",
			expected:   "::1",
		},
		{
			name:       "RemoteAddr plain IP without port",
			remoteAddr: "10.0.0.1",
			expected:   "10.0.0.1",
		},
		{
			name:       "X-Forwarded-For single IP",
			remoteAddr: "127.0.0.1:8000",
			xff:        "203.0.113.195",
			expected:   "203.0.113.195",
		},
		{
			name:       "X-Forwarded-For multiple IPs (take first)",
			remoteAddr: "127.0.0.1:8000",
			xff:        "203.0.113.195, 70.41.3.18, 150.172.238.178",
			expected:   "203.0.113.195",
		},
		{
			name:       "X-Real-IP takes precedence over RemoteAddr",
			remoteAddr: "127.0.0.1:8000",
			xri:        "198.51.100.4",
			expected:   "198.51.100.4",
		},
		{
			name:       "X-Forwarded-For takes precedence over X-Real-IP",
			remoteAddr: "127.0.0.1:8000",
			xff:        "203.0.113.10",
			xri:        "198.51.100.4",
			expected:   "203.0.113.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.xri != "" {
				req.Header.Set("X-Real-IP", tt.xri)
			}

			ip := ExtractClientIP(req)
			if ip != tt.expected {
				t.Errorf("ExtractClientIP() = %q, expected %q", ip, tt.expected)
			}
		})
	}

	if ip := ExtractClientIP(nil); ip != "127.0.0.1" {
		t.Errorf("ExtractClientIP(nil) = %q, expected '127.0.0.1'", ip)
	}
}

func TestCleanupStaleClients(t *testing.T) {
	rl := New(Config{
		RequestsPerMinute: 60,
		Burst:             5,
	})
	defer rl.Stop()

	rl.Allow("1.1.1.1")
	rl.Allow("2.2.2.2")
	rl.Allow("3.3.3.3")

	if rl.ClientCount() != 3 {
		t.Fatalf("Expected 3 clients, got %d", rl.ClientCount())
	}

	futureTime := time.Now().Add(5 * time.Minute)
	removed := rl.CleanupStale(futureTime, 3*time.Minute)

	if removed != 3 {
		t.Errorf("Expected 3 clients removed, got %d", removed)
	}
	if rl.ClientCount() != 0 {
		t.Errorf("Expected 0 clients after cleanup, got %d", rl.ClientCount())
	}
}

func TestRateLimiterConcurrency(t *testing.T) {
	rl := New(Config{
		RequestsPerMinute: 6000,
		Burst:             100,
	})
	defer rl.Stop()

	var wg sync.WaitGroup
	const routines = 20
	const requestsPerRoutine = 50

	for i := 0; i < routines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ip := "192.168.1." + string(rune('0'+(id%5)))
			for j := 0; j < requestsPerRoutine; j++ {
				_ = rl.Allow(ip)
			}
		}(i)
	}

	wg.Wait()
}

func TestNilLimiter(t *testing.T) {
	var rl *Limiter

	if !rl.Allow("127.0.0.1") {
		t.Error("nil limiter Allow() should return true")
	}
	if !rl.AllowRequest(nil) {
		t.Error("nil limiter AllowRequest() should return true")
	}
	if rl.ClientCount() != 0 {
		t.Errorf("Expected 0 ClientCount for nil limiter, got %d", rl.ClientCount())
	}
	if rl.RequestsPerMinute() != 0 {
		t.Errorf("Expected 0 RequestsPerMinute for nil limiter, got %d", rl.RequestsPerMinute())
	}
	if rl.Burst() != 0 {
		t.Errorf("Expected 0 Burst for nil limiter, got %d", rl.Burst())
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := rl.Middleware(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("nil limiter Middleware should pass through, got %d", rec.Code)
	}
}

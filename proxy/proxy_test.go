package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

type observedRequest struct {
	route, backend string
	status         int
	duration       time.Duration
}

type testRequestObserver struct{ requests []observedRequest }

func (o *testRequestObserver) ObserveRequest(route, backend string, status int, duration time.Duration) {
	o.requests = append(o.requests, observedRequest{route: route, backend: backend, status: status, duration: duration})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyReportsRequestMetricsObservation(t *testing.T) {
	const backend = "http://backend.example"

	r, err := router.New(&config.Config{Routes: []config.Route{{
		Path: "/observed", Backends: []config.Backend{{URL: backend}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	observer := &testRequestObserver{}
	h := NewWithTransport(r, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("accepted")),
		}, nil
	}))
	h.SetRequestObserver(observer)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/observed/item", nil))

	if len(observer.requests) != 1 {
		t.Fatalf("expected one observation, got %d", len(observer.requests))
	}
	got := observer.requests[0]
	if got.route != "/observed" || got.backend != backend || got.status != http.StatusAccepted {
		t.Errorf("unexpected observation: %+v", got)
	}
	if got.duration <= 0 {
		t.Errorf("expected positive request duration, got %s", got.duration)
	}
}

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

func TestProxyStripPrefix(t *testing.T) {
	var receivedPath, receivedQuery string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("stripped"))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:        "/api",
				StripPrefix: "/api",
				Backends:    []config.Backend{{URL: backend.URL}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api/v1/users?status=active", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	if receivedPath != "/v1/users" {
		t.Errorf("Expected backend to receive path '/v1/users', got %q", receivedPath)
	}

	if receivedQuery != "status=active" {
		t.Errorf("Expected query 'status=active' to be preserved, got %q", receivedQuery)
	}
}

func TestProxyStripPrefixExactMatch(t *testing.T) {
	var receivedPath string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:        "/api",
				StripPrefix: "/api",
				Backends:    []config.Backend{{URL: backend.URL}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	if receivedPath != "/" {
		t.Errorf("Expected backend to receive '/', got %q", receivedPath)
	}
}

func TestProxyRewrite(t *testing.T) {
	var receivedPath, receivedQuery string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("rewritten"))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path: "/legacy",
				Rewrite: &config.RewriteRule{
					Pattern:     "^/legacy/(.*)$",
					Replacement: "/v2/$1",
				},
				Backends: []config.Backend{{URL: backend.URL}},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	req := httptest.NewRequest(http.MethodGet, "http://example.com/legacy/orders?page=2", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", res.StatusCode)
	}

	if receivedPath != "/v2/orders" {
		t.Errorf("Expected backend to receive rewritten path '/v2/orders', got %q", receivedPath)
	}

	if receivedQuery != "page=2" {
		t.Errorf("Expected query 'page=2' to be preserved, got %q", receivedQuery)
	}
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

func TestProxyWeightedRoundRobin(t *testing.T) {
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
				Path:      "/weighted",
				Algorithm: "weighted_round_robin",
				Backends: []config.Backend{
					{URL: backend1.URL, Weight: 2},
					{URL: backend2.URL, Weight: 1},
				},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	// Send 6 requests: expected ratio 2:1 -> 4 to backend1, 2 to backend2
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodGet, "/weighted", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d failed with status %d", i, rec.Code)
		}
	}

	if count1.Load() != 4 {
		t.Errorf("Expected backend1 to receive 4 requests, got %d", count1.Load())
	}
	if count2.Load() != 2 {
		t.Errorf("Expected backend2 to receive 2 requests, got %d", count2.Load())
	}
}

func TestProxyLeastConnections(t *testing.T) {
	var count1, count2 atomic.Int64
	started1 := make(chan struct{})
	unblock1 := make(chan struct{})

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		close(started1)
		<-unblock1 // hold request open until signaled
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
				Path:      "/api",
				Algorithm: "least_conn",
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

	// Launch first request asynchronously; it hits backend1 and stays in-flight
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/api/task1", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}()

	// Wait until backend1 is actively processing request 1
	select {
	case <-started1:
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for backend1 to start processing")
	}

	// While request 1 is in-flight on backend1, send request 2
	// Least connections MUST route to backend2 (0 in flight) rather than backend1 (1 in flight)
	req2 := httptest.NewRequest(http.MethodGet, "/api/task2", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("Request 2 failed with code %d", rec2.Code)
	}

	if count2.Load() != 1 {
		t.Errorf("Expected backend2 to handle concurrent request, got %d", count2.Load())
	}

	// Unblock backend1 and wait for first request to complete
	close(unblock1)
	wg.Wait()

	if count1.Load() != 1 {
		t.Errorf("Expected backend1 to handle 1 request, got %d", count1.Load())
	}
}

func TestProxyStickySessions(t *testing.T) {
	var count1, count2 atomic.Int64

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("b1"))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("b2"))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:       "/app",
				Algorithm:  "sticky",
				CookieName: "SERVERID",
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

	// 1. Initial request without cookie
	req1 := httptest.NewRequest(http.MethodGet, "/app/login", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("Initial request failed with status %d", rec1.Code)
	}

	// Should have Set-Cookie header
	cookies := rec1.Result().Cookies()
	var serverCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "SERVERID" {
			serverCookie = c
			break
		}
	}
	if serverCookie == nil {
		t.Fatal("Expected Set-Cookie: SERVERID in response")
	}

	b1First := count1.Load() == 1

	// 2. Subsequent 5 requests carrying the session cookie
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/app/profile", nil)
		req.AddCookie(serverCookie)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Subsequent request %d failed: %d", i, rec.Code)
		}
	}

	if b1First {
		// All 6 requests should have gone to backend1
		if count1.Load() != 6 {
			t.Errorf("Expected backend1 to receive all 6 requests, got %d", count1.Load())
		}
		if count2.Load() != 0 {
			t.Errorf("Expected backend2 to receive 0 requests, got %d", count2.Load())
		}
	} else {
		// All 6 requests should have gone to backend2
		if count2.Load() != 6 {
			t.Errorf("Expected backend2 to receive all 6 requests, got %d", count2.Load())
		}
		if count1.Load() != 0 {
			t.Errorf("Expected backend1 to receive 0 requests, got %d", count1.Load())
		}
	}
}

func TestProxyIPHash(t *testing.T) {
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
				Path:      "/ip-test",
				Algorithm: "ip_hash",
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

	// Client IP 192.168.1.100 sends 5 requests with differing source ports
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/ip-test", nil)
		req.RemoteAddr = fmt.Sprintf("192.168.1.100:%d", 10000+i)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d failed", i)
		}
	}

	// Either backend1 got 5 or backend2 got 5 (all pinned)
	c1 := count1.Load()
	c2 := count2.Load()
	if !((c1 == 5 && c2 == 0) || (c1 == 0 && c2 == 5)) {
		t.Errorf("Expected IP hash to pin all requests to one backend, got c1=%d, c2=%d", c1, c2)
	}
}

func TestProxyRetryOn502(t *testing.T) {
	var count1, count2 atomic.Int64

	// Backend 1 returns 502 Bad Gateway
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
	}))
	defer backend1.Close()

	// Backend 2 succeeds with 200 OK
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("recovered by backend 2"))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:    "/retry-502",
				Retries: 2,
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

	req := httptest.NewRequest(http.MethodGet, "/retry-502", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Client must receive HTTP 200 from backend 2
	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	if string(body) != "recovered by backend 2" {
		t.Errorf("Expected body 'recovered by backend 2', got %q", string(body))
	}

	// Backend 1 failed, Backend 2 handled the retry
	if count1.Load() != 1 {
		t.Errorf("Expected backend 1 to be called once, got %d", count1.Load())
	}
	if count2.Load() != 1 {
		t.Errorf("Expected backend 2 to handle retry, got %d", count2.Load())
	}
}

func TestProxyRetryOnDialFailure(t *testing.T) {
	var count2 atomic.Int64

	// Closed backend immediately triggers dial failure
	closedBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	closedURL := closedBackend.URL
	closedBackend.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("healthy backend"))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:    "/dial-retry",
				Retries: 1,
				Backends: []config.Backend{
					{URL: closedURL},
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

	req := httptest.NewRequest(http.MethodGet, "/dial-retry", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200 from retry, got %d", rec.Code)
	}

	if count2.Load() != 1 {
		t.Errorf("Expected backend 2 to handle retry, got %d", count2.Load())
	}
}

func TestProxyRetryWithRequestBody(t *testing.T) {
	var receivedBody string

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read body to simulate consuming stream, then fail
		_, _ = io.ReadAll(r.Body)
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		receivedBody = string(b)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("accepted"))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:    "/api/upload",
				Retries: 1,
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

	payload := `{"username":"alice","action":"create"}`
	req := httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	if receivedBody != payload {
		t.Errorf("Expected backend 2 to receive payload %q, got %q", payload, receivedBody)
	}
}

func TestProxyCircuitBreakerTripsAndSkips(t *testing.T) {
	var count1, count2 atomic.Int64

	// Backend 1 always returns 502
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		http.Error(w, "502 Bad Gateway", http.StatusBadGateway)
	}))
	defer backend1.Close()

	// Backend 2 succeeds
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:    "/cb-test",
				Retries: 1,
				CircuitBreaker: &config.CircuitBreakerConfig{
					FailureThreshold: 5,
					Cooldown:         10 * time.Second,
				},
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

	// Send 5 requests: each one attempts backend 1, fails with 502, then retries backend 2
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/cb-test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d failed with code %d", i+1, rec.Code)
		}
	}

	if count1.Load() != 5 {
		t.Errorf("Expected backend 1 to fail 5 times, got %d", count1.Load())
	}
	if count2.Load() != 5 {
		t.Errorf("Expected backend 2 to handle 5 retries, got %d", count2.Load())
	}

	// Backend 1's circuit breaker should now be OPEN!
	entries := r.Entries()
	if len(entries) != 1 {
		t.Fatal("Expected 1 route entry")
	}
	if entries[0].LB.CircuitBreakerState(0) != "open" {
		t.Errorf("Expected backend 1 circuit breaker to be open, got %s", entries[0].LB.CircuitBreakerState(0))
	}

	// 6th request: backend 1 must be SKIPPED completely!
	req6 := httptest.NewRequest(http.MethodGet, "/cb-test", nil)
	rec6 := httptest.NewRecorder()
	handler.ServeHTTP(rec6, req6)

	if rec6.Code != http.StatusOK {
		t.Fatalf("Request 6 failed with code %d", rec6.Code)
	}

	// count1 must STILL be 5 (backend 1 was skipped without sending traffic)
	if count1.Load() != 5 {
		t.Errorf("Expected backend 1 count to remain 5 (skipped), got %d", count1.Load())
	}
	// count2 must be 6 (handled request directly without waiting on backend 1)
	if count2.Load() != 6 {
		t.Errorf("Expected backend 2 count to be 6, got %d", count2.Load())
	}
}

func TestProxyTimeoutPerAttempt(t *testing.T) {
	var count1, count2 atomic.Int64

	// Backend 1 takes 100ms to respond
	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count1.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend1.Close()

	// Backend 2 responds immediately
	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count2.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("fast backend"))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path:    "/timeout-retry",
				Retries: 1,
				Timeout: 25 * time.Millisecond, // 25ms timeout causes backend 1 (100ms) to time out
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

	req := httptest.NewRequest(http.MethodGet, "/timeout-retry", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	if string(body) != "fast backend" {
		t.Errorf("Expected 'fast backend', got %q", string(body))
	}

	if count2.Load() != 1 {
		t.Errorf("Expected backend 2 to handle retry after timeout, got %d", count2.Load())
	}
}

func TestProxyRouteRateLimiting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("backend ok"))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Port: 8080,
		Routes: []config.Route{
			{
				Path: "/limited",
				RateLimit: &config.RateLimit{
					RequestsPerMinute: 60,
					Burst:             2,
				},
				Backends: []config.Backend{
					{URL: backend.URL},
				},
			},
		},
	}

	r, err := router.New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	handler := New(r)

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/limited/resource", nil)
		req.RemoteAddr = "203.0.113.10:4000"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Request %d from IP A: expected 200 OK, got %d", i, rec.Code)
		}
	}

	req3 := httptest.NewRequest(http.MethodGet, "/limited/resource", nil)
	req3.RemoteAddr = "203.0.113.10:5000"
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("Request 3 from IP A: expected 429 Too Many Requests, got %d", rec3.Code)
	}
	if rec3.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After header on 429 response")
	}

	reqB := httptest.NewRequest(http.MethodGet, "/limited/resource", nil)
	reqB.RemoteAddr = "198.51.100.20:4000"
	recB := httptest.NewRecorder()
	handler.ServeHTTP(recB, reqB)

	if recB.Code != http.StatusOK {
		t.Fatalf("Request from IP B: expected 200 OK, got %d", recB.Code)
	}
}

package loadbalancer

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/clientip"
	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestRoundRobin(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
		{URL: "http://server3"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	if lb.Len() != 3 {
		t.Errorf("Expected 3 backends, got %d", lb.Len())
	}

	expectedOrder := []string{"http://server1", "http://server2", "http://server3", "http://server1", "http://server2"}
	for i, expected := range expectedOrder {
		next, err := lb.Next()
		if err != nil {
			t.Fatalf("Request %d: unexpected error: %v", i+1, err)
		}
		if next.String() != expected {
			t.Errorf("Request %d: expected %s, got %s", i+1, expected, next.String())
		}
	}
}

func TestConcurrentRoundRobin(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	const workers = 100
	const requestsPerWorker = 10

	results := make(chan string, workers*requestsPerWorker)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < requestsPerWorker; j++ {
				next, err := lb.Next()
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				results <- next.String()
			}
		}()
	}

	wg.Wait()
	close(results)

	counts := make(map[string]int)
	for url := range results {
		counts[url]++
	}

	if counts["http://server1"] != 500 {
		t.Errorf("Expected server1 to get 500 requests, got %d", counts["http://server1"])
	}
	if counts["http://server2"] != 500 {
		t.Errorf("Expected server2 to get 500 requests, got %d", counts["http://server2"])
	}
}

func TestInvalidBackendURL(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://valid"},
		{URL: "://invalid-url"}, // This will fail url.Parse
	}

	_, err := New(backends)
	if err == nil {
		t.Error("Expected error for invalid URL, got nil")
	}
}

func TestHealthAwareSelection(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
		{URL: "http://server3"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// Initially all should be healthy
	for i := 0; i < 3; i++ {
		if !lb.IsHealthy(i) {
			t.Errorf("Backend %d expected to be healthy initially", i)
		}
	}

	// Mark server2 (index 1) as unhealthy
	lb.MarkHealthy(1, false)
	if lb.IsHealthy(1) {
		t.Errorf("Backend 1 expected to be unhealthy")
	}

	// Next() should now only return server1 and server3
	for i := 0; i < 6; i++ {
		next, err := lb.Next()
		if err != nil {
			t.Fatalf("Unexpected error from Next(): %v", err)
		}
		if next.String() == "http://server2" {
			t.Fatalf("Unhealthy server2 was selected on iteration %d", i)
		}
	}
}

func TestAllBackendsUnhealthy(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	lb.MarkHealthy(0, false)
	lb.MarkHealthy(1, false)

	_, err = lb.Next()
	if !errors.Is(err, ErrNoHealthyBackend) {
		t.Fatalf("Expected ErrNoHealthyBackend, got %v", err)
	}
}

func TestBackendRecovery(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// Mark both down
	lb.MarkHealthy(0, false)
	lb.MarkHealthy(1, false)

	_, err = lb.Next()
	if !errors.Is(err, ErrNoHealthyBackend) {
		t.Fatalf("Expected ErrNoHealthyBackend, got %v", err)
	}

	// Server2 recovers
	lb.MarkHealthy(1, true)

	next, err := lb.Next()
	if err != nil {
		t.Fatalf("Expected success after recovery, got error: %v", err)
	}
	if next.String() != "http://server2" {
		t.Fatalf("Expected recovered server2 to be selected, got %s", next.String())
	}
}

func TestBackendURL(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1:9001"},
		{URL: "http://server2:9002"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	if lb.BackendURL(0).String() != "http://server1:9001" {
		t.Errorf("Expected http://server1:9001, got %s", lb.BackendURL(0).String())
	}
	if lb.BackendURL(1).String() != "http://server2:9002" {
		t.Errorf("Expected http://server2:9002, got %s", lb.BackendURL(1).String())
	}
}

func TestWeightedRoundRobinRatioAndInterleaving(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1", Weight: 2},
		{URL: "http://server2", Weight: 1},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmWeightedRoundRobin))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	if lb.Algorithm() != config.AlgorithmWeightedRoundRobin {
		t.Errorf("Expected algorithm %q, got %q", config.AlgorithmWeightedRoundRobin, lb.Algorithm())
	}
	if lb.Weight(0) != 2 || lb.Weight(1) != 1 {
		t.Errorf("Expected weights 2 and 1, got %d and %d", lb.Weight(0), lb.Weight(1))
	}

	// Smooth WRR interleaves: with weights 2 and 1, the 3-cycle is: server1, server2, server1
	expectedFirst3 := []string{"http://server1", "http://server2", "http://server1"}
	for i, expected := range expectedFirst3 {
		res, err := lb.Select(nil)
		if err != nil {
			t.Fatalf("Request %d failed: %v", i, err)
		}
		if res.URL.String() != expected {
			t.Errorf("Request %d: expected %s, got %s", i, expected, res.URL.String())
		}
	}

	// Run 300 total requests (including the 3 above = 297 more)
	counts := make(map[string]int)
	counts["http://server1"] = 2
	counts["http://server2"] = 1

	for i := 0; i < 297; i++ {
		res, err := lb.Select(nil)
		if err != nil {
			t.Fatalf("Iteration %d failed: %v", i, err)
		}
		counts[res.URL.String()]++
	}

	if counts["http://server1"] != 200 {
		t.Errorf("Expected server1 to receive 200 requests, got %d", counts["http://server1"])
	}
	if counts["http://server2"] != 100 {
		t.Errorf("Expected server2 to receive 100 requests, got %d", counts["http://server2"])
	}
}

func TestWeightedRoundRobinHealthFailover(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://heavy", Weight: 3},
		{URL: "http://light", Weight: 1},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmWeightedRoundRobin))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// Mark heavy server unhealthy
	lb.MarkHealthy(0, false)

	// All traffic must go to light server
	for i := 0; i < 5; i++ {
		res, err := lb.Select(nil)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if res.URL.String() != "http://light" {
			t.Errorf("Expected traffic to route to light server, got %s", res.URL.String())
		}
	}

	// Recover heavy server
	lb.MarkHealthy(0, true)

	counts := make(map[string]int)
	for i := 0; i < 400; i++ {
		res, err := lb.Select(nil)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		counts[res.URL.String()]++
	}

	if counts["http://heavy"] != 300 {
		t.Errorf("Expected recovered heavy server to get 300 requests, got %d", counts["http://heavy"])
	}
	if counts["http://light"] != 100 {
		t.Errorf("Expected light server to get 100 requests, got %d", counts["http://light"])
	}
}

func TestConcurrentWeightedRoundRobin(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://srv1", Weight: 3},
		{URL: "http://srv2", Weight: 1},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmWeightedRoundRobin))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	const workers = 50
	const perWorker = 20 // 1000 total

	results := make(chan string, workers*perWorker)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				res, err := lb.Select(nil)
				if err != nil {
					t.Errorf("Select error: %v", err)
					return
				}
				results <- res.URL.String()
			}
		}()
	}

	wg.Wait()
	close(results)

	counts := make(map[string]int)
	for u := range results {
		counts[u]++
	}

	if counts["http://srv1"] != 750 {
		t.Errorf("Expected srv1 to get 750 requests, got %d", counts["http://srv1"])
	}
	if counts["http://srv2"] != 250 {
		t.Errorf("Expected srv2 to get 250 requests, got %d", counts["http://srv2"])
	}
}

func TestLeastConnections(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmLeastConn))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// First request: picks server1 (0 in flight)
	res1, err := lb.Select(nil)
	if err != nil {
		t.Fatalf("Select 1 failed: %v", err)
	}
	if res1.Done == nil {
		t.Fatal("Expected Done callback to be non-nil for least_conn")
	}

	// Server 1 should now have 1 active connection, Server 2 has 0
	if lb.ActiveConnections(0) != 1 {
		t.Errorf("Expected backend 0 to have 1 active connection, got %d", lb.ActiveConnections(0))
	}
	if lb.ActiveConnections(1) != 0 {
		t.Errorf("Expected backend 1 to have 0 active connections, got %d", lb.ActiveConnections(1))
	}

	// Second request: must route to server2 (which has 0 in flight, less than server1's 1)
	res2, err := lb.Select(nil)
	if err != nil {
		t.Fatalf("Select 2 failed: %v", err)
	}
	if res2.URL.String() != "http://server2" {
		t.Errorf("Expected Select 2 to choose server2, got %s", res2.URL.String())
	}
	if lb.ActiveConnections(1) != 1 {
		t.Errorf("Expected backend 1 to have 1 active connection, got %d", lb.ActiveConnections(1))
	}

	// Complete request 1: decrement server1 active connections back to 0
	res1.Done()
	if lb.ActiveConnections(0) != 0 {
		t.Errorf("Expected backend 0 active connections to be 0 after Done(), got %d", lb.ActiveConnections(0))
	}

	// Third request: server1 has 0, server2 has 1 -> must pick server1!
	res3, err := lb.Select(nil)
	if err != nil {
		t.Fatalf("Select 3 failed: %v", err)
	}
	if res3.URL.String() != "http://server1" {
		t.Errorf("Expected Select 3 to choose server1, got %s", res3.URL.String())
	}

	// Complete all remaining requests
	res2.Done()
	res3.Done()

	if lb.ActiveConnections(0) != 0 || lb.ActiveConnections(1) != 0 {
		t.Errorf("Expected active connections to return to 0, got %d and %d",
			lb.ActiveConnections(0), lb.ActiveConnections(1))
	}
}

func TestLeastConnectionsHealthAware(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmLeastConn))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// Hold 5 connections on server2
	for i := 0; i < 5; i++ {
		lb.backends[1].activeConnections.Add(1)
	}

	// Mark server1 unhealthy
	lb.MarkHealthy(0, false)

	// Even though server2 has 5 active connections and server1 has 0, server1 is unhealthy
	// so least_conn must route to server2!
	res, err := lb.Select(nil)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if res.URL.String() != "http://server2" {
		t.Errorf("Expected healthy server2 to be chosen, got %s", res.URL.String())
	}
	res.Done()
}

func TestIPHashDeterministic(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
		{URL: "http://server3"},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmIPHash))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	// Requests from the same IP must always route to the same backend
	reqA1 := httptest.NewRequest(http.MethodGet, "/", nil)
	reqA1.RemoteAddr = "192.168.1.100:1234"

	resA1, err := lb.Select(reqA1)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	// Send from same IP but different port (e.g. new ephemeral port)
	reqA2 := httptest.NewRequest(http.MethodGet, "/", nil)
	reqA2.RemoteAddr = "192.168.1.100:5678"

	resA2, err := lb.Select(reqA2)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	if resA1.URL.String() != resA2.URL.String() {
		t.Errorf("IP Hash routed same IP to different backends: %s vs %s",
			resA1.URL.String(), resA2.URL.String())
	}

	// Repeat 20 times for consistency
	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.168.1.100:9999"
		res, err := lb.Select(req)
		if err != nil {
			t.Fatalf("Iteration %d failed: %v", i, err)
		}
		if res.URL.String() != resA1.URL.String() {
			t.Fatalf("IP Hash was not deterministic on iteration %d: expected %s, got %s",
				i, resA1.URL.String(), res.URL.String())
		}
	}
}

func TestIPHashXForwardedFor(t *testing.T) {
	resolver, err := clientip.NewResolver([]string{"10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	backends := []config.Backend{{URL: "http://server1"}, {URL: "http://server2"}, {URL: "http://server3"}}
	for _, trusted := range []bool{false, true} {
		opts := []Option{WithAlgorithm(config.AlgorithmIPHash)}
		if trusted {
			opts = append(opts, WithClientIPResolver(resolver))
		}
		lb, err := New(backends, opts...)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 32; i++ {
			client := fmt.Sprintf("198.51.100.%d", i)
			direct := httptest.NewRequest("GET", "/", nil)
			direct.RemoteAddr = "10.0.0.5:1234"
			if trusted {
				direct.RemoteAddr = client + ":1234"
			}
			want, err := lb.Select(direct)
			if err != nil {
				t.Fatal(err)
			}
			forwarded := httptest.NewRequest("GET", "/", nil)
			forwarded.RemoteAddr = "10.0.0.5:5678"
			forwarded.Header.Set("X-Forwarded-For", "1.2.3.4, "+client+", 10.0.0.2")
			forwarded.Header.Set("X-Real-IP", client)
			got, err := lb.Select(forwarded)
			if err != nil {
				t.Fatal(err)
			}
			if got.URL.String() != want.URL.String() {
				t.Fatalf("trusted=%v client=%s: got %s, want %s", trusted, client, got.URL, want.URL)
			}
		}
	}
}

func TestIPHashHealthFailover(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends, WithAlgorithm(config.AlgorithmIPHash))
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"

	res, err := lb.Select(req)
	if err != nil {
		t.Fatalf("Initial select failed: %v", err)
	}

	chosenURL := res.URL.String()
	chosenIdx := 0
	otherIdx := 1
	if chosenURL == "http://server2" {
		chosenIdx = 1
		otherIdx = 0
	}

	lb.MarkHealthy(chosenIdx, false)

	resAfterFail, err := lb.Select(req)
	if err != nil {
		t.Fatalf("Failover select failed: %v", err)
	}
	expectedOtherURL := lb.BackendURL(otherIdx).String()
	if resAfterFail.URL.String() != expectedOtherURL {
		t.Errorf("Expected failover to %s, got %s", expectedOtherURL, resAfterFail.URL.String())
	}

	lb.MarkHealthy(chosenIdx, true)
	resAfterRecovery, err := lb.Select(req)
	if err != nil {
		t.Fatalf("Recovery select failed: %v", err)
	}
	if resAfterRecovery.URL.String() != chosenURL {
		t.Errorf("Expected return to originally hashed backend %s, got %s",
			chosenURL, resAfterRecovery.URL.String())
	}
}

func TestStickySessionsCookieLifecycle(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends,
		WithAlgorithm(config.AlgorithmSticky),
		WithCookieName("MY_COOKIE"),
	)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	res1, err := lb.Select(req1)
	if err != nil {
		t.Fatalf("First request failed: %v", err)
	}

	if res1.Cookie == nil {
		t.Fatal("Expected Cookie to be generated on initial request")
	}
	if res1.Cookie.Name != "MY_COOKIE" {
		t.Errorf("Expected cookie name 'MY_COOKIE', got %q", res1.Cookie.Name)
	}
	if res1.Cookie.Value == "" {
		t.Errorf("Expected non-empty cookie value")
	}

	assignedBackendURL := res1.URL.String()
	sessionCookie := res1.Cookie

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(sessionCookie)

		res, err := lb.Select(req)
		if err != nil {
			t.Fatalf("Sticky request %d failed: %v", i, err)
		}
		if res.URL.String() != assignedBackendURL {
			t.Errorf("Request %d drifted to %s, expected sticky %s",
				i, res.URL.String(), assignedBackendURL)
		}
		if res.Cookie != nil {
			t.Errorf("Request %d: Cookie should not be re-set when already valid", i)
		}
	}

	assignedIdx := 0
	otherIdx := 1
	if assignedBackendURL == "http://server2" {
		assignedIdx = 1
		otherIdx = 0
	}
	lb.MarkHealthy(assignedIdx, false)

	reqFailover := httptest.NewRequest(http.MethodGet, "/", nil)
	reqFailover.AddCookie(sessionCookie)

	resFailover, err := lb.Select(reqFailover)
	if err != nil {
		t.Fatalf("Failover request failed: %v", err)
	}

	expectedOtherURL := lb.BackendURL(otherIdx).String()
	if resFailover.URL.String() != expectedOtherURL {
		t.Errorf("Expected failover to %s, got %s", expectedOtherURL, resFailover.URL.String())
	}
	if resFailover.Cookie == nil {
		t.Fatal("Expected updated Cookie to be set on failover")
	}
	if resFailover.Cookie.Value == sessionCookie.Value {
		t.Errorf("Expected new cookie value for new backend, got same %s", resFailover.Cookie.Value)
	}

	newCookie := resFailover.Cookie
	reqSubsequent := httptest.NewRequest(http.MethodGet, "/", nil)
	reqSubsequent.AddCookie(newCookie)

	resSubsequent, err := lb.Select(reqSubsequent)
	if err != nil {
		t.Fatalf("Subsequent request failed: %v", err)
	}
	if resSubsequent.URL.String() != expectedOtherURL {
		t.Errorf("Expected stickiness to new backend %s, got %s", expectedOtherURL, resSubsequent.URL.String())
	}
}

func TestAllAlgorithmsAllUnhealthy(t *testing.T) {
	algos := []string{
		config.AlgorithmRoundRobin,
		config.AlgorithmWeightedRoundRobin,
		config.AlgorithmLeastConn,
		config.AlgorithmIPHash,
		config.AlgorithmSticky,
	}

	for _, algo := range algos {
		t.Run(algo, func(t *testing.T) {
			backends := []config.Backend{
				{URL: "http://server1", Weight: 2},
				{URL: "http://server2", Weight: 1},
			}

			lb, err := New(backends, WithAlgorithm(algo))
			if err != nil {
				t.Fatalf("Failed to create load balancer: %v", err)
			}

			lb.MarkHealthy(0, false)
			lb.MarkHealthy(1, false)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			_, err = lb.Select(req)
			if !errors.Is(err, ErrNoHealthyBackend) {
				t.Errorf("Expected ErrNoHealthyBackend for algo %s, got %v", algo, err)
			}
		})
	}
}

func TestCircuitBreakerTripsAfter5ConsecutiveFailures(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	server1URL := lb.BackendURL(0)

	for i := 1; i <= 4; i++ {
		lb.RecordFailure(server1URL)
		if lb.ConsecutiveFailures(0) != i {
			t.Errorf("Expected %d failures, got %d", i, lb.ConsecutiveFailures(0))
		}
		if lb.CircuitBreakerState(0) != "closed" {
			t.Errorf("Expected closed state, got %s", lb.CircuitBreakerState(0))
		}
	}

	lb.RecordFailure(server1URL)
	if lb.CircuitBreakerState(0) != "open" {
		t.Fatalf("Expected open state after 5 failures, got %s", lb.CircuitBreakerState(0))
	}

	for i := 0; i < 5; i++ {
		res, err := lb.Select(nil)
		if err != nil {
			t.Fatalf("Select %d failed: %v", i, err)
		}
		if res.URL.String() != "http://server2" {
			t.Errorf("Expected server2 to be selected because server1 is tripped, got %s", res.URL.String())
		}
	}

	lb.RecordSuccess(server1URL)
	if lb.CircuitBreakerState(0) != "closed" {
		t.Errorf("Expected closed state after success, got %s", lb.CircuitBreakerState(0))
	}
	if lb.ConsecutiveFailures(0) != 0 {
		t.Errorf("Expected 0 failures after success, got %d", lb.ConsecutiveFailures(0))
	}
}

func TestSelectExcluding(t *testing.T) {
	backends := []config.Backend{
		{URL: "http://server1"},
		{URL: "http://server2"},
	}

	lb, err := New(backends)
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}

	excluded := map[string]bool{"http://server1": true}
	res, err := lb.SelectExcluding(nil, excluded)
	if err != nil {
		t.Fatalf("SelectExcluding failed: %v", err)
	}
	if res.URL.String() != "http://server2" {
		t.Errorf("Expected server2 when server1 is excluded, got %s", res.URL.String())
	}

	excluded["http://server2"] = true
	_, err = lb.SelectExcluding(nil, excluded)
	if !errors.Is(err, ErrNoHealthyBackend) {
		t.Errorf("Expected ErrNoHealthyBackend when all backends excluded, got %v", err)
	}
}

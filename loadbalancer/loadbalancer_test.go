package loadbalancer

import (
	"errors"
	"sync"
	"testing"

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

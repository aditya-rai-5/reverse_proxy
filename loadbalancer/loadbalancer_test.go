package loadbalancer

import (
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
		next := lb.Next()
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
				results <- lb.Next().String()
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

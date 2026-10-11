package loadbalancer

import (
	"errors"
	"net/url"
	"sync/atomic"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

// ErrNoHealthyBackend is returned by Next when every backend is unhealthy.
var ErrNoHealthyBackend = errors.New("no healthy backends available")

// backend wraps a parsed URL with its health state.
type backend struct {
	url     *url.URL
	healthy atomic.Bool
}

// LoadBalancer distributes requests across backends using round-robin,
// skipping any backend currently marked as unhealthy.
type LoadBalancer struct {
	backends []*backend
	counter  atomic.Uint64
}

// New creates a LoadBalancer from the given config backends.
// All backends start as healthy so traffic flows immediately on startup.
func New(backends []config.Backend) (*LoadBalancer, error) {
	bs := make([]*backend, 0, len(backends))

	for _, b := range backends {
		u, err := url.Parse(b.URL)
		if err != nil {
			return nil, err
		}
		be := &backend{url: u}
		be.healthy.Store(true) // optimistic: assume healthy until proven otherwise
		bs = append(bs, be)
	}

	return &LoadBalancer{backends: bs}, nil
}

// Next returns the next healthy backend using round-robin selection.
// If all backends are unhealthy it falls back to round-robin across all
// backends (fail-open) so the proxy degrades gracefully rather than
// dropping all traffic.
func (lb *LoadBalancer) Next() (*url.URL, error) {
	n := uint64(len(lb.backends))

	// One full cycle to find a healthy backend.
	start := lb.counter.Add(1) - 1
	for i := uint64(0); i < n; i++ {
		be := lb.backends[(start+i)%n]
		if be.healthy.Load() {
			return be.url, nil
		}
	}

	// Fail-open: all backends unhealthy — return ErrNoHealthyBackend so the
	// caller can decide (proxy returns 503).
	return nil, ErrNoHealthyBackend
}

// MarkHealthy sets the health state of the backend at the given index.
func (lb *LoadBalancer) MarkHealthy(idx int, healthy bool) {
	lb.backends[idx].healthy.Store(healthy)
}

// IsHealthy reports the current health state of the backend at index idx.
func (lb *LoadBalancer) IsHealthy(idx int) bool {
	return lb.backends[idx].healthy.Load()
}

// BackendURL returns the URL of the backend at index idx.
func (lb *LoadBalancer) BackendURL(idx int) *url.URL {
	return lb.backends[idx].url
}

// Len returns the total number of configured backends.
func (lb *LoadBalancer) Len() int {
	return len(lb.backends)
}

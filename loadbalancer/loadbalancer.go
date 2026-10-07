package loadbalancer

import (
	"net/url"
	"sync/atomic"
	"github.com/aditya-rai-5/reverse_proxy/config"
)

type LoadBalancer struct {
	backends []*url.URL
	counter  atomic.Uint64
}

func New(backends []config.Backend) (*LoadBalancer, error) {
	urls := make([]*url.URL, 0, len(backends))

	for _, b := range backends {
		u, err := url.Parse(b.URL)
		if err != nil {
			return nil, err
		}
		urls = append(urls, u)
	}

	return &LoadBalancer{backends: urls}, nil
}

func (lb *LoadBalancer) Next() *url.URL {
	idx := lb.counter.Add(1) - 1
	return lb.backends[idx%uint64(len(lb.backends))]
}

func (lb *LoadBalancer) Len() int {
	return len(lb.backends)
}

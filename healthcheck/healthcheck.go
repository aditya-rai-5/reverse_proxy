package healthcheck

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
)

type Checker struct {
	lb        *loadbalancer.LoadBalancer
	routePath string
	cfg       config.HealthCheck
	client    *http.Client
}

func New(lb *loadbalancer.LoadBalancer, routePath string, cfg config.HealthCheck) *Checker {
	return &Checker{
		lb:        lb,
		routePath: routePath,
		cfg:       cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Checker) Start(ctx context.Context, wg *sync.WaitGroup) {
	for i := 0; i < c.lb.Len(); i++ {
		i := i // capture loop variable
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.runProbe(ctx, i)
		}()
	}
}

func (c *Checker) runProbe(ctx context.Context, idx int) {
	backendURL := c.lb.BackendURL(idx)
	probeURL := buildProbeURL(backendURL, c.cfg.Path)

	log.Printf("[HEALTH] starting probe for route=%q backend=%s probe=%s interval=%s",
		c.routePath, backendURL, probeURL, c.cfg.Interval)

	c.probe(idx, probeURL)

	ticker := time.NewTicker(c.cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("[HEALTH] stopping probe for route=%q backend=%s", c.routePath, backendURL)
			return
		case <-ticker.C:
			c.probe(idx, probeURL)
		}
	}
}

func (c *Checker) probe(idx int, probeURL string) {
	backendURL := c.lb.BackendURL(idx)

	resp, err := c.client.Get(probeURL) //nolint:noctx // context is implicit via client.Timeout
	if err != nil {
		if c.lb.IsHealthy(idx) {
			log.Printf("[HEALTH] ✗ route=%q backend=%s UNHEALTHY: %v", c.routePath, backendURL, err)
		}
		c.lb.MarkHealthy(idx, false)
		return
	}
	resp.Body.Close()

	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	wasHealthy := c.lb.IsHealthy(idx)

	c.lb.MarkHealthy(idx, ok)

	switch {
	case ok && !wasHealthy:
		log.Printf("[HEALTH] ✓ route=%q backend=%s RECOVERED (HTTP %d)", c.routePath, backendURL, resp.StatusCode)
	case !ok && wasHealthy:
		log.Printf("[HEALTH] ✗ route=%q backend=%s UNHEALTHY (HTTP %d)", c.routePath, backendURL, resp.StatusCode)
	}
}

func buildProbeURL(base *url.URL, path string) string {
	return fmt.Sprintf("%s://%s%s", base.Scheme, base.Host, path)
}

// Package healthcheck provides background health probing for proxy backends.
//
// Each Checker runs a goroutine per backend that periodically sends an HTTP GET
// to <backendURL><probePath>. A 2xx response marks the backend healthy;
// any error or non-2xx marks it unhealthy.
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

// Checker runs periodic health probes against every backend in a LoadBalancer.
type Checker struct {
	lb        *loadbalancer.LoadBalancer
	routePath string // only used for log messages
	cfg       config.HealthCheck
	client    *http.Client
}

// New creates a Checker for the given load balancer.
// routePath is only used in log lines so you can tell which route it belongs to.
func New(lb *loadbalancer.LoadBalancer, routePath string, cfg config.HealthCheck) *Checker {
	return &Checker{
		lb:        lb,
		routePath: routePath,
		cfg:       cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// Don't follow redirects — a redirect from a health endpoint is
			// itself suspicious and should be treated as unhealthy.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Start launches a background probe goroutine for every backend.
// It returns immediately; all goroutines are stopped when ctx is cancelled.
// The WaitGroup wg is incremented for each goroutine so callers can block on
// clean shutdown if desired.
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

// runProbe continuously probes backend[idx] until ctx is done.
func (c *Checker) runProbe(ctx context.Context, idx int) {
	backendURL := c.lb.BackendURL(idx)
	probeURL := buildProbeURL(backendURL, c.cfg.Path)

	log.Printf("[HEALTH] starting probe for route=%q backend=%s probe=%s interval=%s",
		c.routePath, backendURL, probeURL, c.cfg.Interval)

	// Run an immediate check so the proxy doesn't blindly forward traffic on
	// startup before the first tick fires.
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

// probe performs a single HTTP GET and updates the backend's health flag.
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

// buildProbeURL joins the backend's scheme+host with the probe path.
func buildProbeURL(base *url.URL, path string) string {
	return fmt.Sprintf("%s://%s%s", base.Scheme, base.Host, path)
}

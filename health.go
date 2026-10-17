package main

import (
	"encoding/json"
	"net/http"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

// backendStatus is the JSON shape returned by /healthz.
type backendStatus struct {
	URL                 string `json:"url"`
	Healthy             bool   `json:"healthy"`
	Weight              int    `json:"weight,omitempty"`
	ActiveConnections   int64  `json:"active_connections,omitempty"`
	CircuitBreakerState string `json:"circuit_breaker,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
}

type rateLimitStatus struct {
	RequestsPerMinute int `json:"requests_per_minute"`
	Burst             int `json:"burst"`
}

type routeStatus struct {
	Path         string           `json:"path"`
	Algorithm    string           `json:"algorithm,omitempty"`
	Retries      int              `json:"retries"`
	RetryMethods []string         `json:"retry_methods"`
	RateLimit    *rateLimitStatus `json:"rate_limit,omitempty"`
	Backends     []backendStatus  `json:"backends"`
}

// makeHealthzHandler returns an HTTP handler that reports the health of every
// backend across all routes as a JSON document.
func makeHealthzHandler(r *router.Router, cfg ...*config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var routes []routeStatus
		allHealthy := true

		for _, entry := range r.Entries() {
			var bs []backendStatus
			for i := 0; i < entry.LB.Len(); i++ {
				healthy := entry.LB.IsHealthy(i)
				cbState := string(entry.LB.CircuitBreakerState(i))
				if !healthy || cbState == "open" {
					allHealthy = false
				}
				bs = append(bs, backendStatus{
					URL:                 entry.LB.BackendURL(i).String(),
					Healthy:             healthy,
					Weight:              entry.LB.Weight(i),
					ActiveConnections:   entry.LB.ActiveConnections(i),
					CircuitBreakerState: cbState,
					ConsecutiveFailures: entry.LB.ConsecutiveFailures(i),
				})
			}
			rs := routeStatus{
				Path:         entry.Path,
				Algorithm:    entry.LB.Algorithm(),
				Retries:      entry.Retries,
				RetryMethods: entry.RetryMethods,
				Backends:     bs,
			}
			if entry.RateLimiter != nil {
				rs.RateLimit = &rateLimitStatus{
					RequestsPerMinute: entry.RateLimiter.RequestsPerMinute(),
					Burst:             entry.RateLimiter.Burst(),
				}
			}
			routes = append(routes, rs)
		}

		w.Header().Set("Content-Type", "application/json")
		if !allHealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		resp := map[string]any{
			"status": func() string {
				if allHealthy {
					return "healthy"
				}
				return "degraded"
			}(),
			"routes": routes,
		}
		if len(cfg) > 0 && cfg[0] != nil && cfg[0].RateLimit != nil && cfg[0].RateLimit.Enabled() {
			resp["rate_limit"] = cfg[0].RateLimit
		}
		json.NewEncoder(w).Encode(resp)
	}
}

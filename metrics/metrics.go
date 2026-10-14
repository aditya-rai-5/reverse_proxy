// Package metrics provides Prometheus text-format metrics for the proxy.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/router"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type requestLabels struct {
	route, backend, status string
}

type histogram struct {
	buckets []uint64
	count   uint64
	sum     float64
}

// Metrics contains proxy counters, latency histograms, and backend health.
type Metrics struct {
	router              *router.Router
	mu                  sync.Mutex
	counts              map[requestLabels]uint64
	latency             map[requestLabels]*histogram
	rateLimitRejections map[string]uint64
}

func New(r *router.Router) *Metrics {
	return &Metrics{
		router:              r,
		counts:              make(map[requestLabels]uint64),
		latency:             make(map[requestLabels]*histogram),
		rateLimitRejections: make(map[string]uint64),
	}
}

// ObserveRequest records one completed proxied request.
func (m *Metrics) ObserveRequest(route, backend string, statusCode int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	if backend == "" {
		backend = "none"
	}
	labels := requestLabels{route: route, backend: backend, status: strconv.Itoa(statusCode)}
	seconds := duration.Seconds()
	if seconds < 0 {
		seconds = 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[labels]++
	h := m.latency[labels]
	if h == nil {
		h = &histogram{buckets: make([]uint64, len(durationBuckets))}
		m.latency[labels] = h
	}
	h.count++
	h.sum += seconds
	for i, upperBound := range durationBuckets {
		if seconds <= upperBound {
			h.buckets[i]++
		}
	}
}

// ObserveRateLimitRejection records a request rejected by either the global or
// a route-level rate limiter. Use "global" for the global limiter.
func (m *Metrics) ObserveRateLimitRejection(route string) {
	if route == "" {
		route = "unknown"
	}
	m.mu.Lock()
	m.rateLimitRejections[route]++
	m.mu.Unlock()
}

// Handler returns the Prometheus exposition endpoint. Backend health is
// sampled from the load balancers each time Prometheus scrapes this endpoint.
func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(m.exposition()))
	})
}

func (m *Metrics) exposition() string {
	var b strings.Builder
	fmt.Fprintln(&b, "# HELP http_requests_total Total number of HTTP requests handled by the proxy.")
	fmt.Fprintln(&b, "# TYPE http_requests_total counter")
	fmt.Fprintln(&b, "# HELP http_request_duration_seconds Time spent handling HTTP requests.")
	fmt.Fprintln(&b, "# TYPE http_request_duration_seconds histogram")

	m.mu.Lock()
	countKeys := make([]requestLabels, 0, len(m.counts))
	for k := range m.counts {
		countKeys = append(countKeys, k)
	}
	sort.Slice(countKeys, func(i, j int) bool { return lessLabels(countKeys[i], countKeys[j]) })
	for _, k := range countKeys {
		fmt.Fprintf(&b, "http_requests_total%s %d\n", formatLabels(k, ""), m.counts[k])
	}
	histograms := make(map[requestLabels]histogram, len(m.latency))
	for k, value := range m.latency {
		copyOfHistogram := *value
		copyOfHistogram.buckets = append([]uint64(nil), value.buckets...)
		histograms[k] = copyOfHistogram
	}
	rejections := make(map[string]uint64, len(m.rateLimitRejections))
	for route, count := range m.rateLimitRejections {
		rejections[route] = count
	}
	m.mu.Unlock()

	histKeys := make([]requestLabels, 0, len(histograms))
	for k := range histograms {
		histKeys = append(histKeys, k)
	}
	sort.Slice(histKeys, func(i, j int) bool { return lessLabels(histKeys[i], histKeys[j]) })
	for _, k := range histKeys {
		h := histograms[k]
		for i, upperBound := range durationBuckets {
			labels := formatLabels(k, strconv.FormatFloat(upperBound, 'f', -1, 64))
			fmt.Fprintf(&b, "http_request_duration_seconds_bucket%s %d\n", labels, h.buckets[i])
		}
		fmt.Fprintf(&b, "http_request_duration_seconds_bucket%s %d\n", formatLabels(k, "+Inf"), h.count)
		fmt.Fprintf(&b, "http_request_duration_seconds_sum%s %s\n", formatLabels(k, ""), strconv.FormatFloat(h.sum, 'g', -1, 64))
		fmt.Fprintf(&b, "http_request_duration_seconds_count%s %d\n", formatLabels(k, ""), h.count)
	}

	fmt.Fprintln(&b, "# HELP rate_limit_rejections_total Total number of requests rejected by rate limiters.")
	fmt.Fprintln(&b, "# TYPE rate_limit_rejections_total counter")
	rejectionRoutes := make([]string, 0, len(rejections))
	for route := range rejections {
		rejectionRoutes = append(rejectionRoutes, route)
	}
	sort.Strings(rejectionRoutes)
	for _, route := range rejectionRoutes {
		fmt.Fprintf(&b, "rate_limit_rejections_total{route=\"%s\"} %d\n", escape(route), rejections[route])
	}

	fmt.Fprintln(&b, "# HELP backend_healthy_gauge Whether a backend is healthy (1) or unhealthy (0).")
	fmt.Fprintln(&b, "# TYPE backend_healthy_gauge gauge")
	fmt.Fprintln(&b, "# HELP circuit_breaker_state Current circuit breaker state (one-hot: open, half-open, or closed).")
	fmt.Fprintln(&b, "# TYPE circuit_breaker_state gauge")
	fmt.Fprintln(&b, "# HELP circuit_breaker_trips_total Total times a backend circuit breaker has opened.")
	fmt.Fprintln(&b, "# TYPE circuit_breaker_trips_total counter")
	if m.router != nil {
		for _, entry := range m.router.Entries() {
			for i := 0; i < entry.LB.Len(); i++ {
				backend := entry.LB.BackendURL(i).String()
				healthy := 0
				if entry.LB.IsHealthy(i) {
					healthy = 1
				}
				labels := formatHealthLabels(entry.Path, backend)
				fmt.Fprintf(&b, "backend_healthy_gauge%s %d\n", labels, healthy)
				state := string(entry.LB.CircuitBreakerState(i))
				for _, candidate := range []string{"closed", "half-open", "open"} {
					value := 0
					if state == candidate {
						value = 1
					}
					fmt.Fprintf(&b, "circuit_breaker_state{route=\"%s\",backend=\"%s\",state=\"%s\"} %d\n", escape(entry.Path), escape(backend), escape(candidate), value)
				}
				fmt.Fprintf(&b, "circuit_breaker_trips_total%s %d\n", labels, entry.LB.CircuitBreakerTrips(i))
			}
		}
	}
	return b.String()
}

func lessLabels(a, b requestLabels) bool {
	if a.route != b.route {
		return a.route < b.route
	}
	if a.backend != b.backend {
		return a.backend < b.backend
	}
	return a.status < b.status
}

func formatLabels(labels requestLabels, le string) string {
	formatted := fmt.Sprintf(`{route="%s",backend="%s",status_code="%s"`, escape(labels.route), escape(labels.backend), escape(labels.status))
	if le != "" {
		formatted += fmt.Sprintf(`,le="%s"`, escape(le))
	}
	return formatted + "}"
}

func formatHealthLabels(route, backend string) string {
	return fmt.Sprintf(`{route="%s",backend="%s"}`, escape(route), escape(backend))
}

func escape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

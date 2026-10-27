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

type attemptLabels struct {
	route, backend, status, outcome string
}

type attemptMetric struct {
	count   uint64
	seconds float64
}

// Metrics contains proxy counters, latency histograms, and backend health.
type Metrics struct {
	router              *router.Router
	mu                  sync.Mutex
	counts              map[requestLabels]uint64
	latency             map[requestLabels]*histogram
	rateLimitRejections map[string]uint64
	attempts            map[attemptLabels]attemptMetric
}

func New(r *router.Router) *Metrics {
	return &Metrics{
		router:              r,
		counts:              make(map[requestLabels]uint64),
		latency:             make(map[requestLabels]*histogram),
		rateLimitRejections: make(map[string]uint64),
		attempts:            make(map[attemptLabels]attemptMetric),
	}
}

// ObserveAttempt records each backend attempt separately from the final request.
// Status zero means no upstream response headers were received.
func (m *Metrics) ObserveAttempt(route, backend string, status int, outcome string, duration time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := attemptLabels{route, backend, strconv.Itoa(status), outcome}
	value := m.attempts[key]
	value.count++
	value.seconds += max(0, duration.Seconds())
	m.attempts[key] = value
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
	attempts := make(map[attemptLabels]attemptMetric, len(m.attempts))
	for key, value := range m.attempts {
		attempts[key] = value
	}
	m.mu.Unlock()

	fmt.Fprintln(&b, "# HELP backend_attempts_total Explicit backend attempts, including retries.")
	fmt.Fprintln(&b, "# TYPE backend_attempts_total counter")
	fmt.Fprintln(&b, "# HELP backend_attempt_duration_seconds Time in each backend attempt, including response copying.")
	fmt.Fprintln(&b, "# TYPE backend_attempt_duration_seconds summary")
	keys := make([]attemptLabels, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.route != c.route {
			return a.route < c.route
		}
		if a.backend != c.backend {
			return a.backend < c.backend
		}
		if a.status != c.status {
			return a.status < c.status
		}
		return a.outcome < c.outcome
	})
	for _, key := range keys {
		value := attempts[key]
		labels := fmt.Sprintf(`{route="%s",backend="%s",status_code="%s",outcome="%s"}`, escape(key.route), escape(key.backend), escape(key.status), escape(key.outcome))
		fmt.Fprintf(&b, "backend_attempts_total%s %d\n", labels, value.count)
		fmt.Fprintf(&b, "backend_attempt_duration_seconds_count%s %d\n", labels, value.count)
		fmt.Fprintf(&b, "backend_attempt_duration_seconds_sum%s %g\n", labels, value.seconds)
	}

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

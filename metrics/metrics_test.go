package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestMetricsHandlerExposesCounterAndLatencyHistogram(t *testing.T) {
	const backend = "http://127.0.0.1:9001"
	cfg := &config.Config{Routes: []config.Route{{Path: "/api", Backends: []config.Backend{{URL: backend}}}}}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := New(r)
	m.ObserveRequest("/api", backend, http.StatusCreated, 120*time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics endpoint returned status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain; version=0.0.4") {
		t.Errorf("unexpected content type %q", got)
	}
	for _, want := range []string{
		`http_requests_total{route="/api",backend="http://127.0.0.1:9001",status_code="201"} 1`,
		`http_request_duration_seconds_bucket{route="/api",backend="http://127.0.0.1:9001",status_code="201",le="0.1"} 0`,
		`http_request_duration_seconds_bucket{route="/api",backend="http://127.0.0.1:9001",status_code="201",le="0.25"} 1`,
		`http_request_duration_seconds_bucket{route="/api",backend="http://127.0.0.1:9001",status_code="201",le="0.5"} 1`,
		`http_request_duration_seconds_bucket{route="/api",backend="http://127.0.0.1:9001",status_code="201",le="+Inf"} 1`,
		`http_request_duration_seconds_count{route="/api",backend="http://127.0.0.1:9001",status_code="201"} 1`,
		`http_request_duration_seconds_sum{route="/api",backend="http://127.0.0.1:9001",status_code="201"} 0.12`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output missing %q\n%s", want, body)
		}
	}
}

func TestMetricsHandlerReportsCurrentBackendHealth(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{{Path: "/api", Backends: []config.Backend{{URL: "http://127.0.0.1:9001"}, {URL: "http://127.0.0.1:9002"}}}}}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := New(r)
	serveMetrics := func() string {
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}
	body := serveMetrics()
	if !strings.Contains(body, `backend_healthy_gauge{route="/api",backend="http://127.0.0.1:9001"} 1`) {
		t.Fatalf("healthy backend gauge missing or incorrect:\n%s", body)
	}
	r.Entries()[0].LB.MarkHealthy(0, false)
	body = serveMetrics()
	if !strings.Contains(body, `backend_healthy_gauge{route="/api",backend="http://127.0.0.1:9001"} 0`) {
		t.Fatalf("unhealthy backend gauge missing or incorrect:\n%s", body)
	}
	if !strings.Contains(body, `backend_healthy_gauge{route="/api",backend="http://127.0.0.1:9002"} 1`) {
		t.Fatalf("second backend gauge missing:\n%s", body)
	}
}

func TestMetricsEscapesLabelValues(t *testing.T) {
	m := New(nil)
	m.ObserveRequest("/quote\"line\n", "backend\\name", http.StatusOK, time.Second)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := fmt.Sprintf(`route="%s",backend="%s"`, `/quote\"line\n`, `backend\\name`)
	if !strings.Contains(rec.Body.String(), want) {
		t.Errorf("escaped labels not found; wanted %q in:\n%s", want, rec.Body.String())
	}
}

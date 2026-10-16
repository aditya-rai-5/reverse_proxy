package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestConfiguredWriteRetriesWithMiddlewareAndMetrics(t *testing.T) {
	var firstCalls, secondCalls atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		firstCalls.Add(1)
		_, _ = io.Copy(io.Discard, req.Body)
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		secondCalls.Add(1)
		if req.Header.Get("Idempotency-Key") != "once" {
			t.Error("key lost in configured retry")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer second.Close()
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := fmt.Sprintf(`port: 8080
rate_limit:
  requests_per_minute: 1
  burst: 1
routes:
  - path: /charge
    retries: 1
    retry_methods: [POST]
    rate_limit:
      requests_per_minute: 1
      burst: 1
    backends:
      - url: %s
      - url: %s
`, first.URL, second.URL)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Entries()[0].RateLimiter.Stop()
	m := metrics.New(r)
	h := buildHandler(cfg, r, m)
	req := httptest.NewRequest(http.MethodPost, "/charge", strings.NewReader("charge=100"))
	req.Header.Set("Idempotency-Key", "once")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("status=%d first=%d second=%d", rec.Code, firstCalls.Load(), secondCalls.Load())
	}
	// A retry must not consume a second global or route-level rate-limit token.
	limited := httptest.NewRecorder()
	h.ServeHTTP(limited, req.Clone(req.Context()))
	if limited.Code != http.StatusTooManyRequests || firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Error("retry or rejection bypassed configured limits")
	}
	scrape := httptest.NewRecorder()
	m.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	want := fmt.Sprintf(`http_requests_total{route="/charge",backend="%s",status_code="201"} 1`, second.URL)
	if !strings.Contains(scrape.Body.String(), want) || !strings.Contains(scrape.Body.String(), `rate_limit_rejections_total{route="global"} 1`) {
		t.Errorf("unexpected metrics:\n%s", scrape.Body.String())
	}
	if r.Entries()[0].LB.ConsecutiveFailures(0) != 1 {
		t.Error("upstream failure not counted")
	}
}

func TestHealthzReportsEffectiveRetryMethods(t *testing.T) {
	cfg := &config.Config{Routes: []config.Route{
		{Path: "/read", Backends: []config.Backend{{URL: "http://read.example"}}},
		{Path: "/write", RetryMethods: []string{" post "}, Backends: []config.Backend{{URL: "http://write.example"}}},
		{Path: "/disabled", RetryMethods: []string{}, Backends: []config.Backend{{URL: "http://disabled.example"}}},
	}}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	makeHealthzHandler(r, cfg).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var data struct {
		Routes []routeStatus `json:"routes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"/read": {"GET", "HEAD", "OPTIONS"}, "/write": {"POST"}, "/disabled": {}}
	if len(data.Routes) != len(want) {
		t.Fatalf("routes=%v", data.Routes)
	}
	for _, route := range data.Routes {
		if !reflect.DeepEqual(route.RetryMethods, want[route.Path]) {
			t.Errorf("route %s methods=%v", route.Path, route.RetryMethods)
		}
	}
}

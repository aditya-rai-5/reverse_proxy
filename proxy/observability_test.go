package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

type observationTransport func(*http.Request) (*http.Response, error)

func (f observationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

func TestAttemptMetricsAndCompletionLog(t *testing.T) {
	for _, tc := range []struct {
		name, outcome    string
		firstStatus      int
		err              error
		broken, canceled bool
	}{
		{name: "retry", outcome: "upstream_error", firstStatus: 502},
		{name: "dial", outcome: "transport_error", err: errors.New("dial failure")},
		{name: "timeout", outcome: "timeout", err: context.DeadlineExceeded},
		{name: "cancel", outcome: "canceled", err: context.Canceled, canceled: true},
		{name: "response copy", outcome: "response_error", firstStatus: 200, broken: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := router.New(&config.Config{Routes: []config.Route{{Path: "/api", Retries: 1, Backends: []config.Backend{{URL: "http://one"}, {URL: "http://two"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			h := NewWithTransport(r, observationTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if tc.canceled {
						cancel()
					}
					if tc.err != nil {
						return nil, tc.err
					}
					var body io.ReadCloser = io.NopCloser(strings.NewReader("failed"))
					if tc.broken {
						body = brokenBody{}
					}
					return &http.Response{StatusCode: tc.firstStatus, Header: make(http.Header), Body: body}, nil
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			}))
			m := metrics.New(r)
			h.SetRequestObserver(m)
			var logs bytes.Buffer
			handler := CompletionLogging(h, slog.New(slog.NewJSONHandler(&logs, nil)))
			req := httptest.NewRequest("GET", "/api/secret?token=secret", nil).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			wantCalls := 2
			if tc.broken || tc.canceled {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("calls %d want %d", calls, wantCalls)
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatal(err)
			}
			if record["msg"] != "request_completed" || record["attempts"] != float64(wantCalls) || record["route"] != "/api" || record["bytes"] != float64(w.Body.Len()) {
				t.Fatalf("bad log: %s", logs.String())
			}
			if strings.Contains(logs.String(), "secret") {
				t.Fatal("request secrets logged")
			}
			scrape := httptest.NewRecorder()
			m.Handler().ServeHTTP(scrape, httptest.NewRequest("GET", "/metrics", nil))
			status := "0"
			if tc.firstStatus == 502 {
				status = "502"
			} else if tc.firstStatus == 200 {
				status = "200"
			}
			want := `backend_attempts_total{route="/api",backend="http://one",status_code="` + status + `",outcome="` + tc.outcome + `"} 1`
			if !strings.Contains(scrape.Body.String(), want) {
				t.Fatalf("missing %s in %s", want, scrape.Body.String())
			}
			if wantCalls == 2 && !strings.Contains(scrape.Body.String(), `backend_attempts_total{route="/api",backend="http://two",status_code="200",outcome="success"} 1`) {
				t.Fatal("missing successful retry")
			}
		})
	}
}

func TestCompletionLogForMiddlewareRejection(t *testing.T) {
	var logs bytes.Buffer
	h := CompletionLogging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "limited", 429) }), slog.New(slog.NewJSONHandler(&logs, nil)))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["status"] != float64(429) || record["attempts"] != float64(0) {
		t.Fatalf("%s", logs.String())
	}
}

func TestAttemptMetricsDuration(t *testing.T) {
	m := metrics.New(nil)
	m.ObserveAttempt("/api", "http://one", 200, "success", 125*time.Millisecond)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `backend_attempt_duration_seconds_sum{route="/api",backend="http://one",status_code="200",outcome="success"} 0.125`) {
		t.Fatal(w.Body.String())
	}
}

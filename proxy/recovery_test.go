package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxyRecoveryProbeLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		status      int
		failures    int
	}{
		{"success", "closed", http.StatusOK, 0},
		{"client error", "closed", http.StatusBadRequest, 0},
		{"500", "open", http.StatusInternalServerError, 2},
		{"503", "open", http.StatusServiceUnavailable, 2},
		{"599", "open", 599, 2},
		{"502", "open", http.StatusBadGateway, 2},
		{"504", "open", http.StatusGatewayTimeout, 2},
		{"transport", "open", http.StatusBadGateway, 2},
		{"timeout", "open", http.StatusGatewayTimeout, 2},
		{"cancel", "half-open", StatusClientClosedRequest, 1},
		{"response abort", "half-open", http.StatusOK, 1},
		{"panic", "half-open", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r, err := router.New(&config.Config{Routes: []config.Route{{
					Path: "/", Algorithm: "least_conn", Backends: []config.Backend{{URL: "http://backend"}},
					CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Cooldown: time.Second},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				lb := r.Entries()[0].LB
				lb.RecordFailure(lb.BackendURL(0))
				lb.MarkHealthy(0, true)
				time.Sleep(time.Second)
				m := metrics.New(r)
				scrape := func() {
					m.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/metrics", nil))
					if !lb.Ready() {
						t.Error("half-open state unexpectedly failed readiness")
					}
				}
				scrape()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var h *Handler
				calls := 0
				h = NewWithTransport(r, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if calls > 1 {
						return retryTestResponse(http.StatusOK, "recovered"), nil
					}
					scrape()
					// A request arriving while the recovery attempt is in flight must
					// not reach this backend, including after metrics/readiness reads.
					blocked := httptest.NewRecorder()
					h.ServeHTTP(blocked, httptest.NewRequest("GET", "/", nil))
					if blocked.Code != http.StatusServiceUnavailable || calls != 1 {
						t.Errorf("busy probe: status=%d backend calls=%d", blocked.Code, calls)
					}
					switch tc.name {
					case "transport":
						return nil, errors.New("connection reset")
					case "timeout":
						return nil, context.DeadlineExceeded
					case "cancel":
						cancel()
						return nil, req.Context().Err()
					case "response abort":
						resp := retryTestResponse(http.StatusOK, "")
						resp.Body = &brokenBody{}
						return resp, nil
					case "panic":
						panic("transport panic")
					default:
						return retryTestResponse(tc.status, "response"), nil
					}
				}))
				h.SetRequestObserver(m)
				w := httptest.NewRecorder()
				func() {
					defer func() {
						var want any
						if tc.name == "response abort" {
							want = http.ErrAbortHandler
						} else if tc.name == "panic" {
							want = "transport panic"
						}
						if got := recover(); got != want {
							t.Errorf("panic=%v want=%v", got, want)
						}
					}()
					h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
				}()
				if tc.status != 0 && w.Code != tc.status {
					t.Fatalf("status=%d want=%d", w.Code, tc.status)
				}
				if string(lb.CircuitBreakerState(0)) != tc.state || lb.ConsecutiveFailures(0) != tc.failures || lb.ActiveConnections(0) != 0 {
					t.Fatalf("state=%s failures=%d active=%d", lb.CircuitBreakerState(0), lb.ConsecutiveFailures(0), lb.ActiveConnections(0))
				}
				if tc.state == "open" {
					blocked := httptest.NewRecorder()
					h.ServeHTTP(blocked, httptest.NewRequest("GET", "/", nil))
					if blocked.Code != http.StatusServiceUnavailable || calls != 1 {
						t.Fatal("failed recovery probe bypassed cooldown")
					}
					time.Sleep(time.Second)
				}
				next := httptest.NewRecorder()
				h.ServeHTTP(next, httptest.NewRequest("GET", "/", nil))
				if next.Code != http.StatusOK || calls != 2 || lb.CircuitBreakerState(0) != "closed" {
					t.Fatalf("follow-up: status=%d calls=%d state=%s", next.Code, calls, lb.CircuitBreakerState(0))
				}
			})
		})
	}
}

func TestProxyForwardedStatusBreakerAccounting(t *testing.T) {
	for _, status := range []int{200, 400, 499, 500, 501, 503, 505, 599} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r, err := router.New(&config.Config{Routes: []config.Route{{
				Path: "/", Retries: 1,
				Backends:       []config.Backend{{URL: "http://first"}, {URL: "http://second"}},
				CircuitBreaker: &config.CircuitBreakerConfig{FailureThreshold: 1, Cooldown: time.Hour},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			h := NewWithTransport(r, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return retryTestResponse(status, "upstream response"), nil
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
			if w.Code != status || w.Body.String() != "upstream response" || calls != 1 {
				t.Fatalf("status=%d body=%q calls=%d", w.Code, w.Body.String(), calls)
			}
			wantState, wantFailures := "closed", 0
			if status >= 500 {
				wantState, wantFailures = "open", 1
			}
			lb := r.Entries()[0].LB
			if string(lb.CircuitBreakerState(0)) != wantState || lb.ConsecutiveFailures(0) != wantFailures {
				t.Fatalf("state=%s failures=%d; want %s, %d", lb.CircuitBreakerState(0), lb.ConsecutiveFailures(0), wantState, wantFailures)
			}
		})
	}
}

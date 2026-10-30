package loadbalancer

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/circuitbreaker"
	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestRecoveryCandidateScansDoNotReserveOtherBackends(t *testing.T) {
	for _, algorithm := range []string{"round_robin", "weighted_round_robin", "least_conn", "ip_hash", "sticky"} {
		t.Run(algorithm, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lb, err := New([]config.Backend{{URL: "http://one"}, {URL: "http://two"}}, WithAlgorithm(algorithm),
					WithCircuitBreaker(circuitbreaker.Config{FailureThreshold: 1, Cooldown: time.Second}))
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < lb.Len(); i++ {
					lb.RecordFailure(lb.BackendURL(i))
					lb.MarkHealthy(i, true)
				}
				time.Sleep(time.Second)
				for i := 0; i < 3; i++ {
					if !lb.Ready() || lb.CircuitBreakerState(0) != circuitbreaker.StateHalfOpen {
						t.Fatal("readiness/state reads consumed a probe")
					}
				}
				req := httptest.NewRequest("GET", "/", nil)
				excluded := map[string]bool{"http://unrelated": true}
				first, err := lb.SelectExcluding(req, excluded)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Done()
				// Sticky selection must also fall back when its cookie's probe is busy.
				req.AddCookie(&http.Cookie{Name: "SERVERID", Value: backendID(first.URL)})
				second, err := lb.SelectExcluding(req, excluded)
				if err != nil {
					t.Fatalf("unselected backend was reserved: %v", err)
				}
				defer second.Done()
				if first.URL.String() == second.URL.String() || len(excluded) != 1 {
					t.Fatal("selected busy backend or mutated caller exclusions")
				}
				if _, err := lb.Select(req); !errors.Is(err, ErrNoHealthyBackend) {
					t.Fatalf("expected both probes reserved, got %v", err)
				}
				first.Done()
				first.Done()
				next, err := lb.Select(req)
				if err != nil {
					t.Fatalf("cancellation leaked reservation: %v", err)
				}
				defer next.Done()
				if next.URL.String() != first.URL.String() {
					t.Fatal("selected still-reserved backend")
				}
				for i := 0; i < lb.Len(); i++ {
					want := int64(0)
					if algorithm == "least_conn" {
						want = 1
					}
					if lb.ActiveConnections(i) != want {
						t.Fatalf("backend %d: active=%d want=%d", i, lb.ActiveConnections(i), want)
					}
				}
			})
		})
	}
}

func TestHalfOpenAdmitsOneConcurrentRequest(t *testing.T) {
	for _, algorithm := range []string{"round_robin", "weighted_round_robin", "least_conn", "ip_hash", "sticky"} {
		t.Run(algorithm, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				lb, err := New([]config.Backend{{URL: "http://backend"}}, WithAlgorithm(algorithm),
					WithCircuitBreaker(circuitbreaker.Config{FailureThreshold: 1, Cooldown: time.Second}))
				if err != nil {
					t.Fatal(err)
				}
				lb.RecordFailure(lb.BackendURL(0))
				time.Sleep(time.Second)
				const requests = 32
				results := make(chan *Result, requests)
				start := make(chan struct{})
				for i := 0; i < requests; i++ {
					go func() {
						<-start
						result, err := lb.Select(nil)
						if err != nil && !errors.Is(err, ErrNoHealthyBackend) {
							t.Errorf("Select: %v", err)
						}
						results <- result
					}()
				}
				close(start)
				admitted := 0
				for i := 0; i < requests; i++ {
					if result := <-results; result != nil {
						admitted++
						if result.Done != nil {
							defer result.Done()
						}
					}
				}
				if admitted != 1 {
					t.Fatalf("admitted %d simultaneous recovery requests; want 1", admitted)
				}
			})
		})
	}
}

package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestConfiguredRetryBudgetLimitsAttempts(t *testing.T) {
	for _, budget := range []struct {
		name, setting string
		attempts      int
	}{
		{"omitted capped default", "", 3},
		{"explicit zero", "retries: 0", 1},
		{"explicit one", "retries: 1", 2},
		{"explicit above default cap", "retries: 3", 4},
	} {
		for _, failure := range []struct {
			name   string
			status int
			err    error
		}{
			{"502 response", http.StatusBadGateway, nil},
			{"504 response", http.StatusGatewayTimeout, nil},
			{"connection error", http.StatusBadGateway, errors.New("connection reset")},
			{"upstream timeout", http.StatusGatewayTimeout, context.DeadlineExceeded},
		} {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				t.Run(budget.name+"/"+failure.name+"/"+method, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "config.yaml")
					data := fmt.Sprintf(`port: 8080
routes:
  - path: /
    %s
    retry_methods: [GET, POST]
    backends:
      - url: http://one.example
      - url: http://two.example
      - url: http://three.example
      - url: http://four.example
`, budget.setting)
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
					if got := r.Entries()[0].Retries; got != budget.attempts-1 {
						t.Fatalf("router retries=%d, want %d", got, budget.attempts-1)
					}
					seen := make(map[string]bool)
					calls := 0
					h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
						calls++
						if seen[out.URL.Host] {
							t.Errorf("retried previously failed backend %s", out.URL.Host)
						}
						seen[out.URL.Host] = true
						if budget.attempts == 1 && out.GetBody != nil {
							t.Error("zero retry budget exposed transport replay")
						}
						body, err := io.ReadAll(out.Body)
						_ = out.Body.Close()
						if err != nil || string(body) != "payload" {
							t.Errorf("attempt %d body=%q error=%v", calls, body, err)
						}
						if out.Header.Get("Idempotency-Key") != "once" {
							t.Error("idempotency key lost")
						}
						if failure.err != nil {
							return nil, failure.err
						}
						return retryTestResponse(failure.status, "failed"), nil
					}))
					req := httptest.NewRequest(method, "/", strings.NewReader("payload"))
					req.Header.Set("Idempotency-Key", "once")
					req.GetBody = func() (io.ReadCloser, error) {
						return io.NopCloser(strings.NewReader("payload")), nil
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					if rec.Code != failure.status || calls != budget.attempts {
						t.Fatalf("status=%d calls=%d, want status=%d calls=%d", rec.Code, calls, failure.status, budget.attempts)
					}
					for i := 0; i < r.Entries()[0].LB.Len(); i++ {
						lb := r.Entries()[0].LB
						want := 0
						if seen[lb.BackendURL(i).Host] {
							want = 1
						}
						if got := lb.ConsecutiveFailures(i); got != want {
							t.Errorf("backend %d failures=%d, want %d", i, got, want)
						}
					}
				})
			}
		}
	}
}

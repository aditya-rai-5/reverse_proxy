package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func retryTestRouter(t *testing.T, methods []string, algorithm string) *router.Router {
	t.Helper()
	r, err := router.New(&config.Config{Routes: []config.Route{{
		Path: "/", Retries: 1, RetryMethods: methods, Algorithm: algorithm,
		Backends: []config.Backend{{URL: "http://first.example"}, {URL: "http://second.example"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func retryTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestProxyDefaultRetriesDoNotDuplicateWrites(t *testing.T) {
	for _, key := range []string{"", "key-without-route-opt-in"} {
		t.Run(key, func(t *testing.T) {
			var writes, secondCalls atomic.Int64
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				writes.Add(1)
				http.Error(w, "response failed after write", http.StatusBadGateway)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				secondCalls.Add(1)
				writes.Add(1)
				w.WriteHeader(http.StatusCreated)
			}))
			defer second.Close()
			r, err := router.New(&config.Config{Routes: []config.Route{{
				Path: "/", Retries: 1, Backends: []config.Backend{{URL: first.URL}, {URL: second.URL}},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/charge", strings.NewReader("charge=100"))
			if key != "" {
				req.Header.Set("Idempotency-Key", key)
			}
			rec := httptest.NewRecorder()
			New(r).ServeHTTP(rec, req)
			if rec.Code != http.StatusBadGateway || writes.Load() != 1 || secondCalls.Load() != 0 {
				t.Fatalf("status=%d writes=%d second backend calls=%d; want 502, 1, 0", rec.Code, writes.Load(), secondCalls.Load())
			}
		})
	}
}

func TestProxyExplicitWriteRetryUsesSharedIdempotencyKey(t *testing.T) {
	var mu sync.Mutex
	writes, calls := 0, 0
	seen := make(map[string]bool)
	var keys, bodies []string
	backend := func(fail bool) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			body, _ := io.ReadAll(req.Body)
			key := req.Header.Get("Idempotency-Key")
			mu.Lock()
			calls++
			keys = append(keys, key)
			bodies = append(bodies, string(body))
			if !seen[key] {
				seen[key] = true
				writes++
			}
			mu.Unlock()
			if fail {
				http.Error(w, "failed after deduplicated write", http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusCreated)
		}
	}
	first, second := httptest.NewServer(backend(true)), httptest.NewServer(backend(false))
	defer first.Close()
	defer second.Close()
	r, err := router.New(&config.Config{Routes: []config.Route{{
		Path: "/", Retries: 1, RetryMethods: []string{http.MethodPost},
		Backends: []config.Backend{{URL: first.URL}, {URL: second.URL}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/charge", strings.NewReader("charge=100"))
	req.Header.Set("Idempotency-Key", "charge-once")
	rec := httptest.NewRecorder()
	New(r).ServeHTTP(rec, req)
	mu.Lock()
	defer mu.Unlock()
	if rec.Code != http.StatusCreated || calls != 2 || writes != 1 {
		t.Fatalf("status=%d calls=%d writes=%d; want 201, 2, 1", rec.Code, calls, writes)
	}
	for i := range keys {
		if keys[i] != "charge-once" || bodies[i] != "charge=100" {
			t.Errorf("attempt %d: key=%q body=%q", i+1, keys[i], bodies[i])
		}
	}
}

func TestProxyConnectionIdempotencyKeyRetrySafety(t *testing.T) {
	for _, tc := range []struct {
		name       string
		connection []string
		stripped   bool
	}{
		{"normal", nil, false},
		{"unrelated token", []string{"X-Idempotency-Key"}, false},
		{"nominated", []string{"Idempotency-Key"}, true},
		{"mixed case", []string{"keep-alive, iDeMpOtEnCy-KeY"}, true},
		{"multiple lines", []string{"keep-alive", " \tIdempotency-Key , X-Other"}, true},
	} {
		for _, payload := range []string{"", "charge=100"} {
			t.Run(tc.name+"/"+payload, func(t *testing.T) {
				r := retryTestRouter(t, []string{http.MethodPost}, "")
				calls, writes := 0, 0
				seen := make(map[string]bool)
				h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
					calls++
					key := out.Header.Get("Idempotency-Key")
					wantKey := "charge-once"
					if tc.stripped {
						wantKey = ""
						if out.GetBody != nil || out.Body == nil || out.Body == http.NoBody {
							t.Error("ineligible write remains transport replayable")
						}
					}
					if key != wantKey || out.Header.Get("Connection") != "" {
						t.Errorf("unexpected forwarded headers: %v", out.Header)
					}
					body, err := io.ReadAll(out.Body)
					out.Body.Close()
					if err != nil || string(body) != payload {
						t.Errorf("body=%q err=%v", body, err)
					}
					// All backends share deduplication, but unkeyed writes always apply.
					if key == "" || !seen[key] {
						writes++
						seen[key] = true
					}
					if calls == 1 {
						return retryTestResponse(http.StatusBadGateway, "failed after write"), nil
					}
					return retryTestResponse(http.StatusCreated, "done"), nil
				}))
				req := httptest.NewRequest(http.MethodPost, "/charge", strings.NewReader(payload))
				req.Header.Set("Idempotency-Key", "charge-once")
				req.Header["Connection"] = tc.connection
				req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(payload)), nil }
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				wantCalls, wantStatus := 2, http.StatusCreated
				if tc.stripped {
					wantCalls, wantStatus = 1, http.StatusBadGateway
				}
				if calls != wantCalls || writes != 1 || rec.Code != wantStatus {
					t.Fatalf("calls=%d writes=%d status=%d; want %d, 1, %d", calls, writes, rec.Code, wantCalls, wantStatus)
				}
			})
		}
	}
}

func TestProxyClientKeyDoesNotEnableTransportRetry(t *testing.T) {
	var writes atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/warm" {
			w.Write([]byte("warm connection"))
			return
		}
		_, _ = io.Copy(io.Discard, req.Body)
		writes.Add(1)
		// Close after the side effect but before any response. On a reused
		// connection net/http would normally retry an empty keyed POST.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer backend.Close()
	r, err := router.New(&config.Config{Routes: []config.Route{{
		Path: "/", Retries: 1, Backends: []config.Backend{{URL: backend.URL}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	transport := defaultTransport()
	defer transport.CloseIdleConnections()
	h := NewWithTransport(r, transport)
	warm := httptest.NewRecorder()
	h.ServeHTTP(warm, httptest.NewRequest(http.MethodGet, "/warm", nil))
	if warm.Code != http.StatusOK {
		t.Fatalf("warmup status=%d", warm.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/charge", nil)
	req.Header.Set("Idempotency-Key", "key-without-opt-in")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || writes.Load() != 1 {
		t.Fatalf("status=%d writes=%d; transport must not replay protected POST", rec.Code, writes.Load())
	}
}

func TestProxyRetryPolicyAcrossFailureKinds(t *testing.T) {
	failures := []struct {
		name   string
		status int
		err    error
	}{
		{"502", http.StatusBadGateway, nil},
		{"504", http.StatusGatewayTimeout, nil},
		{"connection error", http.StatusBadGateway, errors.New("connection reset")},
		{"attempt timeout", http.StatusGatewayTimeout, context.DeadlineExceeded},
	}
	policies := []struct {
		name, method, key string
		methods           []string
		wantRetry         bool
	}{
		{"default GET", http.MethodGet, "", nil, true},
		{"default HEAD", http.MethodHead, "", nil, true},
		{"default OPTIONS", http.MethodOptions, "", nil, true},
		{"default POST", http.MethodPost, "", nil, false},
		{"default PATCH", http.MethodPatch, "", nil, false},
		{"default PUT", http.MethodPut, "", nil, false},
		{"default DELETE", http.MethodDelete, "", nil, false},
		{"key without opt-in", http.MethodPost, "once", nil, false},
		{"opt-in without key", http.MethodPost, "", []string{http.MethodPost}, false},
		{"opt-in blank key", http.MethodPost, " \t ", []string{http.MethodPost}, false},
		{"POST opt-in", http.MethodPost, "once", []string{http.MethodPost}, true},
		{"PATCH opt-in", http.MethodPatch, "once", []string{http.MethodPatch}, true},
		{"PUT opt-in", http.MethodPut, "once", []string{http.MethodPut}, true},
		{"DELETE opt-in", http.MethodDelete, "once", []string{http.MethodDelete}, true},
		{"GET removed by allowlist", http.MethodGet, "", []string{http.MethodPost}, false},
		{"empty allowlist", http.MethodGet, "", []string{}, false},
	}
	for _, policy := range policies {
		for _, failure := range failures {
			t.Run(policy.name+"/"+failure.name, func(t *testing.T) {
				r := retryTestRouter(t, policy.methods, "")
				calls := 0
				h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
					calls++
					if _, err := io.ReadAll(out.Body); err != nil {
						t.Fatal(err)
					}
					if calls == 1 {
						if failure.err != nil {
							return nil, failure.err
						}
						return retryTestResponse(failure.status, "failed"), nil
					}
					return retryTestResponse(http.StatusOK, "recovered"), nil
				}))
				req := httptest.NewRequest(policy.method, "/item", strings.NewReader("body"))
				if policy.key != "" {
					req.Header.Set("Idempotency-Key", policy.key)
				}
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				wantCalls, wantStatus := 1, failure.status
				if policy.wantRetry {
					wantCalls, wantStatus = 2, http.StatusOK
				}
				if calls != wantCalls || rec.Code != wantStatus {
					t.Fatalf("calls=%d status=%d; want %d, %d", calls, rec.Code, wantCalls, wantStatus)
				}
				if r.Entries()[0].LB.ConsecutiveFailures(0) != 1 {
					t.Error("real upstream failure was not counted")
				}
			})
		}
	}
}

func TestProxyCancellationDoesNotRetryOrBlameBackend(t *testing.T) {
	for _, algorithm := range []string{"round_robin", "weighted_round_robin", "least_conn", "ip_hash", "sticky"} {
		for _, failure := range []string{"transport", "502", "504"} {
			t.Run(algorithm+"/"+failure, func(t *testing.T) {
				r := retryTestRouter(t, nil, algorithm)
				lb := r.Entries()[0].LB
				// Existing failures must neither increment nor be erased on cancellation.
				for i := 0; i < lb.Len(); i++ {
					lb.RecordFailure(lb.BackendURL(i))
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
					calls++
					cancel()
					if failure == "transport" {
						return nil, out.Context().Err()
					}
					status := http.StatusBadGateway
					if failure == "504" {
						status = http.StatusGatewayTimeout
					}
					return retryTestResponse(status, "failed"), nil
				}))
				observer := &testRequestObserver{}
				h.SetRequestObserver(observer)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/item", nil).WithContext(ctx))
				if calls != 1 || rec.Code != StatusClientClosedRequest {
					t.Fatalf("calls=%d status=%d; want 1, 499", calls, rec.Code)
				}
				for i := 0; i < lb.Len(); i++ {
					if lb.ConsecutiveFailures(i) != 1 || lb.CircuitBreakerState(i) != "closed" || lb.ActiveConnections(i) != 0 {
						t.Errorf("backend %d: failures=%d state=%s active=%d", i, lb.ConsecutiveFailures(i), lb.CircuitBreakerState(i), lb.ActiveConnections(i))
					}
				}
				if len(observer.requests) != 1 || observer.requests[0].status != StatusClientClosedRequest {
					t.Fatalf("unexpected cancellation observations: %+v", observer.requests)
				}
			})
		}
	}
}

func TestProxyAlreadyCanceledRequestMakesNoAttempt(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline expired"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			wantStatus := StatusClientClosedRequest
			if deadline {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				wantStatus = http.StatusGatewayTimeout
			}
			defer cancel()
			cancel()
			calls := 0
			r := retryTestRouter(t, nil, "")
			h := NewWithTransport(r, roundTripperFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("must not run") }))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/item", nil).WithContext(ctx))
			if calls != 0 || rec.Code != wantStatus {
				t.Fatalf("calls=%d status=%d", calls, rec.Code)
			}
			for i := 0; i < r.Entries()[0].LB.Len(); i++ {
				if r.Entries()[0].LB.ConsecutiveFailures(i) != 0 {
					t.Error("canceled request changed breaker")
				}
			}
		})
	}
}

func TestProxyClientDeadlineStopsInFlightAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := retryTestRouter(t, nil, "least_conn")
	calls := 0
	h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
		calls++
		<-out.Context().Done()
		return nil, out.Context().Err()
	}))
	observer := &testRequestObserver{}
	h.SetRequestObserver(observer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/item", nil).WithContext(ctx))
	if calls != 1 || rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("calls=%d status=%d", calls, rec.Code)
	}
	lb := r.Entries()[0].LB
	for i := 0; i < lb.Len(); i++ {
		if lb.ConsecutiveFailures(i) != 0 || lb.ActiveConnections(i) != 0 {
			t.Errorf("backend %d changed on client deadline", i)
		}
	}
	if len(observer.requests) != 1 || observer.requests[0].status != http.StatusGatewayTimeout {
		t.Fatalf("observations=%+v", observer.requests)
	}
}

type retryTestReadCloser struct {
	read   func([]byte) (int, error)
	closed bool
}

func (r *retryTestReadCloser) Read(p []byte) (int, error) { return r.read(p) }
func (r *retryTestReadCloser) Close() error               { r.closed = true; return nil }

func TestProxyCancellationDuringBodyBuffering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &retryTestReadCloser{read: func([]byte) (int, error) { cancel(); return 0, context.Canceled }}
	r := retryTestRouter(t, []string{http.MethodPost}, "")
	h := NewWithTransport(r, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled body reached transport")
		return nil, nil
	}))
	req := httptest.NewRequest(http.MethodPost, "/charge", nil).WithContext(ctx)
	req.Body = body
	req.Header.Set("Idempotency-Key", "once")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != StatusClientClosedRequest || !body.closed {
		t.Fatalf("status=%d body closed=%v", rec.Code, body.closed)
	}
}

func TestProxyCancellationAfterResponseCommitDoesNotAppendError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := retryTestRouter(t, nil, "least_conn")
	lb := r.Entries()[0].LB
	lb.RecordFailure(lb.BackendURL(0))
	responseBody := &retryTestReadCloser{read: func(p []byte) (int, error) { cancel(); return copy(p, "partial"), io.EOF }}
	h := NewWithTransport(r, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		resp := retryTestResponse(http.StatusOK, "")
		resp.Body = responseBody
		return resp, nil
	}))
	observer := &testRequestObserver{}
	h.SetRequestObserver(observer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/item", nil).WithContext(ctx))
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" || !responseBody.closed {
		t.Fatalf("status=%d body=%q closed=%v", rec.Code, rec.Body.String(), responseBody.closed)
	}
	if lb.ConsecutiveFailures(0) != 1 || lb.ActiveConnections(0) != 0 {
		t.Error("cancellation changed breaker or leaked in-flight counter")
	}
	if len(observer.requests) != 1 || observer.requests[0].status != StatusClientClosedRequest {
		t.Fatalf("observations=%+v", observer.requests)
	}
}

func TestProxyIneligibleRequestIsNotBufferedOrTransportReplayable(t *testing.T) {
	for _, emptyBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "with body", true: "empty body"}[emptyBody], func(t *testing.T) {
			r := retryTestRouter(t, nil, "")
			readBeforeTransport, inTransport := false, false
			body := &retryTestReadCloser{read: func(p []byte) (int, error) { readBeforeTransport = !inTransport; return copy(p, "write"), io.EOF }}
			h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
				inTransport = true
				if out.GetBody != nil || out.Body == nil || out.Body == http.NoBody {
					t.Error("ineligible request remains transport replayable")
				}
				if out.Header.Get("Idempotency-Key") != "once" {
					t.Error("key was removed from first attempt")
				}
				data, err := io.ReadAll(out.Body)
				if err != nil {
					t.Fatal(err)
				}
				want := "write"
				if emptyBody {
					want = ""
				}
				if string(data) != want {
					t.Errorf("body=%q want=%q", data, want)
				}
				out.Body.Close()
				return retryTestResponse(http.StatusOK, "done"), nil
			}))
			req := httptest.NewRequest(http.MethodPost, "/charge", nil)
			if !emptyBody {
				req.Body = body
			}
			req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("write")), nil }
			req.Header.Set("Idempotency-Key", "once")
			h.ServeHTTP(httptest.NewRecorder(), req)
			if readBeforeTransport {
				t.Error("ineligible request buffered before transport")
			}
		})
	}
}

func TestProxyOversizedWriteStreamsOnceEvenWithOptIn(t *testing.T) {
	r := retryTestRouter(t, []string{http.MethodPost}, "")
	calls := 0
	reader := strings.NewReader("larger than the limit")
	body := &retryTestReadCloser{read: reader.Read}
	h := NewWithTransportAndBodyLimit(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
		calls++
		if out.GetBody != nil {
			t.Error("oversized body can be replayed")
		}
		data, err := io.ReadAll(out.Body)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		if string(data) != "larger than the limit" {
			t.Errorf("streamed body=%q", data)
		}
		return retryTestResponse(http.StatusBadGateway, "failed"), nil
	}), 4)
	req := httptest.NewRequest(http.MethodPost, "/charge", nil)
	req.Body = body
	req.Header.Set("Idempotency-Key", "once")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if calls != 1 || rec.Code != http.StatusBadGateway || !body.closed {
		t.Fatalf("calls=%d status=%d closed=%v", calls, rec.Code, body.closed)
	}
}

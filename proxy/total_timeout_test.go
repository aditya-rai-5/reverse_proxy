package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestTotalTimeoutAcrossAttempts(t *testing.T) {
	for _, tc := range []struct {
		name           string
		total, attempt time.Duration
		parent         bool
		wantCalls      int
	}{
		{"shared budget", 80 * time.Millisecond, 0, false, 2},
		{"attempt timeout retries", time.Second, 20 * time.Millisecond, false, 3},
		{"disabled", 0, 0, false, 3},
		{"earlier parent", time.Second, 0, true, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", TotalTimeout: tc.total, Timeout: tc.attempt, Retries: 2, Algorithm: "least_conn", Backends: []config.Backend{{URL: "http://one"}, {URL: "http://two"}, {URL: "http://three"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			var deadlines []time.Time
			var contexts []context.Context
			h := NewWithTransport(rt, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				contexts = append(contexts, r.Context())
				deadline, has := r.Context().Deadline()
				if tc.total == 0 && has {
					t.Error("disabled total deadline added a deadline")
				}
				deadlines = append(deadlines, deadline)
				if calls == 1 {
					return retryTestResponse(502, "retry"), nil
				}
				if tc.total == 0 {
					return retryTestResponse(502, "retry"), nil
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			}))
			req := httptest.NewRequest("GET", "/", nil)
			if tc.parent {
				ctx, cancel := context.WithTimeout(req.Context(), 40*time.Millisecond)
				defer cancel()
				req = req.WithContext(ctx)
			}
			observer := &testRequestObserver{}
			h.SetRequestObserver(observer)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			wantStatus := 504
			if tc.total == 0 {
				wantStatus = 502
			}
			if len(observer.requests) != 1 || observer.requests[0].status != wantStatus {
				t.Fatalf("observations=%+v", observer.requests)
			}
			if calls != tc.wantCalls || rec.Code != wantStatus {
				t.Fatalf("calls=%d status=%d", calls, rec.Code)
			}
			if tc.attempt == 0 && tc.total > 0 && !deadlines[0].Equal(deadlines[1]) {
				t.Fatal("retry reset total deadline")
			}
			lb := rt.Entries()[0].LB
			for i := 0; i < lb.Len(); i++ {
				if lb.ActiveConnections(i) != 0 {
					t.Fatal("leaked backend permit")
				}
			}
			if tc.total > 0 && tc.attempt == 0 && lb.ConsecutiveFailures(1) != 0 {
				t.Fatal("total deadline blamed backend")
			}
			if tc.total > 0 {
				for _, ctx := range contexts {
					if ctx.Err() == nil {
						t.Fatal("request context not cleaned up")
					}
				}
			}
		})
	}
}

func TestTotalTimeoutSlowUpload(t *testing.T) {
	for _, retries := range []int{0, 1} {
		t.Run(fmt.Sprint(retries), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body) }))
			defer upstream.Close()
			rt, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", TotalTimeout: 80 * time.Millisecond, Retries: retries, RetryMethods: []string{"POST"}, Backends: []config.Backend{{URL: upstream.URL}}}}})
			if err != nil {
				t.Fatal(err)
			}
			h := New(rt)
			defer h.CloseIdleConnections()
			downstream := httptest.NewServer(h)
			defer downstream.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(downstream.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprint(conn, "POST / HTTP/1.1\r\nHost: proxy\r\nContent-Length: 100\r\nIdempotency-Key: once\r\n\r\nx")
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 504 {
				t.Fatalf("status=%d", resp.StatusCode)
			}
		})
	}
}

func TestTotalTimeoutStreaming(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(fmt.Sprint(http2), func(t *testing.T) {
			stopped := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: hello\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer upstream.Close()
			rt, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", TotalTimeout: 100 * time.Millisecond, Backends: []config.Backend{{URL: upstream.URL}}}}})
			if err != nil {
				t.Fatal(err)
			}
			h := New(rt)
			defer h.CloseIdleConnections()
			downstream := httptest.NewUnstartedServer(h)
			downstream.EnableHTTP2 = http2
			if http2 {
				downstream.StartTLS()
			} else {
				downstream.Start()
			}
			defer downstream.Close()
			client := downstream.Client()
			client.Timeout = 2 * time.Second
			resp, err := client.Get(downstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				t.Fatal("expired stream completed successfully")
			}
			if resp.StatusCode != 200 || string(body) != "data: hello\n\n" {
				t.Fatalf("status=%d body=%q", resp.StatusCode, body)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("upstream not canceled")
			}
		})
	}
}

func TestTotalTimeoutSlowReader(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if r.Context().Err() != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	rt, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", TotalTimeout: 100 * time.Millisecond, Backends: []config.Backend{{URL: upstream.URL}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(rt)
	defer h.CloseIdleConnections()
	finished := make(chan struct{})
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); h.ServeHTTP(w, r) }))
	defer downstream.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(downstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(1024)
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: proxy\r\n\r\n")
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("slow reader held request beyond total deadline")
	}
}

func TestTotalTimeoutSuccessAndKeepAlive(t *testing.T) {
	rt, err := router.New(&config.Config{Routes: []config.Route{
		{Path: "/bounded", TotalTimeout: 100 * time.Millisecond, Backends: []config.Backend{{URL: "http://backend"}}},
		{Path: "/unbounded", Backends: []config.Backend{{URL: "http://backend"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	contexts := make(chan context.Context, 2)
	h := NewWithTransport(rt, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		contexts <- r.Context()
		return retryTestResponse(200, "ok"), nil
	}))
	downstream := httptest.NewServer(h)
	defer downstream.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(downstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(conn)
	for _, path := range []string{"/bounded", "/unbounded"} {
		fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: proxy\r\n\r\n", path)
		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || string(body) != "ok" {
			t.Fatalf("status=%d body=%q err=%v", resp.StatusCode, body, err)
		}
		ctx := <-contexts
		if path == "/bounded" {
			if ctx.Err() == nil {
				t.Fatal("successful request did not release deadline timer")
			}
			time.Sleep(150 * time.Millisecond)
		} else if _, ok := ctx.Deadline(); ok {
			t.Fatal("deadline leaked to next request")
		}
	}
}

func TestTotalTimeoutClientCancellation(t *testing.T) {
	rt := retryTestRouter(t, nil, "least_conn")
	entry, _ := rt.Match("/")
	entry.TotalTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	h := NewWithTransport(rt, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil).WithContext(ctx))
	if calls != 1 || rec.Code != 499 || entry.LB.ConsecutiveFailures(0) != 0 {
		t.Fatalf("calls=%d status=%d failures=%d", calls, rec.Code, entry.LB.ConsecutiveFailures(0))
	}
}

func TestTotalTimeoutStreamObservations(t *testing.T) {
	rt := retryTestRouter(t, nil, "")
	entry, _ := rt.Match("/")
	entry.TotalTimeout = 20 * time.Millisecond
	var body *deadlineResponseBody
	h := NewWithTransport(rt, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		body = &deadlineResponseBody{ctx: r.Context()}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	}))
	observer := &testRequestObserver{}
	h.SetRequestObserver(observer)
	var logs bytes.Buffer
	handler := CompletionLogging(h, slog.New(slog.NewJSONHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	func() {
		defer func() {
			if got := recover(); got != http.ErrAbortHandler {
				t.Errorf("panic=%v", got)
			}
		}()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	}()
	if !body.closed || rec.Body.Len() != 0 || rec.Code != 200 {
		t.Fatalf("closed=%v status=%d body=%q", body.closed, rec.Code, rec.Body.String())
	}
	if len(observer.requests) != 1 || observer.requests[0].status != 504 {
		t.Fatalf("observations=%+v", observer.requests)
	}
	var record map[string]any
	if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["outcome"] != "canceled" || record["status"] != float64(200) {
		t.Fatalf("log=%s", logs.String())
	}
}

type deadlineResponseBody struct {
	ctx    context.Context
	closed bool
}

func (b *deadlineResponseBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (b *deadlineResponseBody) Close() error             { b.closed = true; return nil }

package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxySSEStreaming(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		for _, contentType := range []string{"text/event-stream", "text/event-stream; charset=utf-8"} {
			t.Run(fmt.Sprintf("http2=%v/%s", http2, contentType), func(t *testing.T) {
				events := make(chan string)
				stopped := make(chan struct{})
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer close(stopped)
					w.Header().Set("Content-Type", contentType)
					w.(http.Flusher).Flush()
					for {
						select {
						case event := <-events:
							fmt.Fprint(w, event)
							w.(http.Flusher).Flush()
						case <-r.Context().Done():
							return
						}
					}
				}))
				defer upstream.Close()
				r, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", Backends: []config.Backend{{URL: upstream.URL}}}}})
				if err != nil {
					t.Fatal(err)
				}
				h := New(r)
				defer h.CloseIdleConnections()
				downstream := httptest.NewUnstartedServer(CompletionLogging(h, slog.New(slog.NewTextHandler(io.Discard, nil))))
				downstream.EnableHTTP2 = http2
				if http2 {
					downstream.StartTLS()
				} else {
					downstream.Start()
				}
				defer downstream.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, downstream.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := downstream.Client().Do(req)
				if err != nil {
					t.Fatalf("headers did not arrive before first event: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK || (resp.ProtoMajor == 2) != http2 {
					t.Fatalf("unexpected response: %s %s", resp.Proto, resp.Status)
				}
				for _, event := range []string{"data: first\n\n", "data: second\n\n"} {
					select {
					case events <- event:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					body := make([]byte, len(event))
					if _, err := io.ReadFull(resp.Body, body); err != nil {
						t.Fatalf("event did not arrive while upstream open: %v", err)
					}
					if string(body) != event {
						t.Fatalf("event = %q, want %q", body, event)
					}
				}
				cancel()
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("client cancellation did not stop upstream")
				}
			})
		}
	}
}

// Embedding only ResponseWriter intentionally hides optional interfaces.
type streamingTestWriter struct {
	http.ResponseWriter
	flushes  int
	failAt   int
	flushErr error
}

func (w *streamingTestWriter) FlushError() error {
	w.flushes++
	if w.flushes == w.failAt {
		return w.flushErr
	}
	return nil
}

func TestProxySSEFlushErrors(t *testing.T) {
	for _, tc := range []struct {
		name, contentType   string
		failAt, wantFlushes int
		flushErr            error
		wantAbort           bool
	}{
		{name: "ordinary response", contentType: "text/plain"},
		{name: "unsupported flush", contentType: "text/event-stream", failAt: 1, wantFlushes: 2, flushErr: http.ErrNotSupported},
		{name: "header flush failure", contentType: "text/event-stream", failAt: 1, wantFlushes: 1, flushErr: io.ErrClosedPipe, wantAbort: true},
		{name: "body flush failure", contentType: "text/event-stream", failAt: 2, wantFlushes: 2, flushErr: io.ErrClosedPipe, wantAbort: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := NewWithTransport(retryTestRouter(t, nil, ""), roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				resp := retryTestResponse(http.StatusOK, "data: hello\n\n")
				resp.Header.Set("Content-Type", tc.contentType)
				return resp, nil
			}))
			rec := httptest.NewRecorder()
			w := &streamingTestWriter{ResponseWriter: rec, failAt: tc.failAt, flushErr: tc.flushErr}
			handler := CompletionLogging(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var panicked any
			func() {
				defer func() { panicked = recover() }()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			}()
			if tc.wantAbort {
				if panicked != http.ErrAbortHandler {
					t.Fatalf("panic = %v, want ErrAbortHandler", panicked)
				}
			} else if panicked != nil {
				t.Fatalf("unexpected panic: %v", panicked)
			}
			if calls != 1 || w.flushes != tc.wantFlushes {
				t.Fatalf("calls=%d flushes=%d, want 1 and %d", calls, w.flushes, tc.wantFlushes)
			}
			wantBody := "data: hello\n\n"
			if tc.wantAbort && tc.failAt == 1 {
				wantBody = ""
			}
			if rec.Body.String() != wantBody {
				t.Fatalf("body=%q, want %q", rec.Body.String(), wantBody)
			}
		})
	}
}

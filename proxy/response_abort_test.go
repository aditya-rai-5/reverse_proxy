package proxy

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxyResponseCompletion(t *testing.T) {
	// Exceed server buffers so headers and body bytes reach the client before abort.
	payload := strings.Repeat("partial", 16*1024)
	for _, http2 := range []bool{false, true} {
		for _, fixed := range []bool{false, true} {
			for _, broken := range []bool{false, true} {
				t.Run(fmt.Sprintf("http2=%v/fixed=%v/broken=%v", http2, fixed, broken), func(t *testing.T) {
					var calls atomic.Int64
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if fixed {
							length := len(payload)
							if broken {
								length++
							}
							w.Header().Set("Content-Length", fmt.Sprint(length))
						}
						io.WriteString(w, payload)
						w.(http.Flusher).Flush()
						if broken {
							panic(http.ErrAbortHandler)
						}
					}))
					defer upstream.Close()
					r, err := router.New(&config.Config{Routes: []config.Route{{
						Path: "/", Retries: 1,
						Backends: []config.Backend{{URL: upstream.URL}},
					}}})
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
					resp, err := downstream.Client().Get(downstream.URL)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK || (resp.ProtoMajor == 2) != http2 {
						t.Fatalf("unexpected response: %s %s", resp.Proto, resp.Status)
					}
					body, err := io.ReadAll(resp.Body)
					if (err != nil) != broken {
						t.Fatalf("body read error = %v, want error=%v", err, broken)
					}
					if broken {
						if len(body) == 0 || !strings.HasPrefix(payload, string(body)) {
							t.Fatal("expected only a nonempty prefix of the upstream body")
						}
					} else if string(body) != payload {
						t.Fatal("successful response body changed")
					}
					if calls.Load() != 1 {
						t.Fatalf("upstream calls = %d, want 1", calls.Load())
					}
				})
			}
		}
	}
}

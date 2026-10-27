package proxy

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/metrics"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

// BenchmarkHTTP measures a local 1 KiB response with reused HTTP connections.
// Observed includes metrics and JSON encoding, with log output sent to io.Discard.
func BenchmarkHTTP(b *testing.B) {
	payload := strings.Repeat("x", 1024)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, payload) }))
	defer backend.Close()
	for _, mode := range []string{"direct", "proxy", "observed"} {
		b.Run(mode, func(b *testing.B) {
			url := backend.URL
			if mode != "direct" {
				r, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", Backends: []config.Backend{{URL: backend.URL}}}}})
				if err != nil {
					b.Fatal(err)
				}
				h := New(r)
				defer h.CloseIdleConnections()
				var handler http.Handler = h
				if mode == "observed" {
					h.SetRequestObserver(metrics.New(r))
					handler = CompletionLogging(h, slog.New(slog.NewJSONHandler(io.Discard, nil)))
				}
				server := httptest.NewServer(handler)
				defer server.Close()
				url = server.URL
			}
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.MaxIdleConnsPerHost = 16
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport}
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					resp, err := client.Get(url)
					if err != nil {
						b.Error(err)
						return
					}
					n, err := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					if err != nil || resp.StatusCode != 200 || n != int64(len(payload)) {
						b.Errorf("status=%d bytes=%d error=%v", resp.StatusCode, n, err)
						return
					}
				}
			})
		})
	}
}

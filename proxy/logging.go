package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type completionKey struct{}
type completion struct {
	route, backend, outcome string
	attempts                int
}

func completionFrom(r *http.Request) *completion {
	c, _ := r.Context().Value(completionKey{}).(*completion)
	return c
}

// CompletionLogging emits one JSON-compatible record for each completed request,
// including requests rejected by middleware. It omits raw paths, queries and headers.
func CompletionLogging(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		c := &completion{route: "unmatched", outcome: "no_attempt"}
		tracked := &completionWriter{statusResponseWriter: statusResponseWriter{ResponseWriter: w}}
		finished := false
		defer func() {
			if !finished {
				c.outcome = "aborted"
			}
			if r.Context().Err() != nil {
				c.outcome = "canceled"
			}
			logger.Info("request_completed", "method", r.Method, "route", c.route,
				"backend", c.backend, "status", tracked.statusCode(), "bytes", tracked.bytes,
				"duration_ms", float64(time.Since(started))/float64(time.Millisecond),
				"attempts", c.attempts, "outcome", c.outcome)
		}()
		next.ServeHTTP(tracked, r.WithContext(context.WithValue(r.Context(), completionKey{}, c)))
		finished = true
	})
}

type completionWriter struct {
	statusResponseWriter
	bytes int64
}

func (w *completionWriter) Write(p []byte) (int, error) {
	n, err := w.statusResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

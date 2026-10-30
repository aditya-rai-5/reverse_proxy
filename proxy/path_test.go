package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxyEscapedPaths(t *testing.T) {
	for _, tc := range []struct {
		name, input, strip, pattern, replacement, want string
	}{
		{"encoded slash", "/objects/a%2Fb?x=1", "", "", "", "/objects/a%2Fb?x=1"},
		{"lowercase escape", "/objects/a%2fb", "", "", "", "/objects/a%2fb"},
		{"ordinary slash", "/objects/a/b", "", "", "", "/objects/a/b"},
		{"percent and query", "/objects/a%252Fb?x=%2F+%25&x=2", "", "", "", "/objects/a%252Fb?x=%2F+%25&x=2"},
		{"escaped letter", "/objects/%61", "", "", "", "/objects/%61"},
		{"strip", "/api/a%2Fb?x=1", "/api", "", "", "/a%2Fb?x=1"},
		{"escaped prefix", "/%61pi/a%2Fb", "/api", "", "", "/a%2Fb"},
		{"escaped boundary", "/api%2Fa%2Fb", "/api", "", "", "/a%2Fb"},
		{"strip trailing slash", "/api/a%2Fb", "/api/", "", "", "/a%2Fb"},
		{"strip exact", "/%61pi", "/api", "", "", "/"},
		{"strip unmatched", "/objects/a%2Fb", "/api", "", "", "/objects/a%2Fb"},
		{"regex unmatched", "/objects/a%2Fb", "", "^/legacy/", "/v2/", "/objects/a%2Fb"},
		{"regex unchanged", "/objects/a%2Fb", "", "^(.*)$", "$1", "/objects/a%2Fb"},
		{"regex changed", "/legacy/a%2Fb?x=%2F", "", "^/legacy/(.*)$", "/v2/$1", "/v2/a/b?x=%2F"},
		{"strip and regex", "/api/legacy/a%2Fb", "/api", "^/legacy/(.*)$", "/v2/$1", "/v2/a/b"},
		{"replacement escaping", "/legacy/a%2Fb", "", "^/legacy/.*$", "/100% done", "/100%25%20done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route := config.Route{Path: "/", StripPrefix: tc.strip, Retries: 1, Backends: []config.Backend{{URL: "http://first.example"}, {URL: "http://second.example"}}}
			if tc.pattern != "" {
				route.Rewrite = &config.RewriteRule{Pattern: tc.pattern, Replacement: tc.replacement}
			}
			r, err := router.New(&config.Config{Routes: []config.Route{route}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
				calls++
				if got := out.URL.RequestURI(); got != tc.want {
					t.Errorf("URI = %q, want %q", got, tc.want)
				}
				if out.URL.RawPath != "" {
					decoded, err := url.PathUnescape(out.URL.RawPath)
					if err != nil || decoded != out.URL.Path {
						t.Errorf("inconsistent URL: %+v", out.URL)
					}
				}
				status := http.StatusOK
				if calls == 1 {
					status = http.StatusBadGateway
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody}, nil
			}))
			req := httptest.NewRequest(http.MethodGet, tc.input, nil)
			original := *req.URL
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK || calls != 2 {
				t.Fatalf("status=%d calls=%d", rec.Code, calls)
			}
			if *req.URL != original {
				t.Error("incoming URL mutated")
			}
		})
	}
}

func TestProxyEscapedPathOnWire(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, r.RequestURI)
	}))
	defer backend.Close()
	r, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", StripPrefix: "/api", Backends: []config.Backend{{URL: backend.URL}}}}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(r)
	defer h.CloseIdleConnections()
	mux := http.NewServeMux()
	mux.Handle("/", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/objects/a%2Fb?x=%2F", nil))
	if got, want := rec.Body.String(), "/objects/a%2Fb?x=%2F"; rec.Code != http.StatusOK || got != want {
		t.Fatalf("status=%d URI=%q, want %q", rec.Code, got, want)
	}
}

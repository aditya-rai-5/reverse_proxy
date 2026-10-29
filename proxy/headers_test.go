package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestProxyHeaderPolicy(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, trusted := range []bool{false, true} {
			t.Run(fmt.Sprintf("TLS=%v/trusted=%v", secure, trusted), func(t *testing.T) {
				cfg := &config.Config{Routes: []config.Route{{Path: "/", Backends: []config.Backend{{URL: "https://backend.example"}}}}}
				wantIP := "10.0.0.5"
				if trusted {
					cfg.TrustedProxies = []string{"10.0.0.5/32"}
					wantIP = "198.51.100.1"
				}
				r, err := router.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				// URL scheme and forwarding headers must not determine the scheme.
				req := httptest.NewRequest("GET", "https://public.example/", nil)
				req.TLS = nil
				wantScheme := "http"
				if secure {
					req.TLS = &tls.ConnectionState{}
					wantScheme = "https"
					req.URL.Scheme = "http"
				}
				req.RemoteAddr = "10.0.0.5:1234"
				req.Header = http.Header{
					"Connection": {"Connection, keep-alive, x-internal, X-Internal-Two, ,", "\tx-internal-three , X-Forwarded-Proto, X-Forwarded-For"},
					"X-Internal": {"secret"}, "X-Internal-Two": {"secret"}, "X-Internal-Three": {"secret"},
					"Keep-Alive": {"timeout=5"}, "Trailer": {"X-Request-Trailer"}, "Te": {"trailers"},
					"X-End-To-End":      {"preserve"},
					"X-Forwarded-For":   {"1.2.3.4, 198.51.100.1"},
					"X-Forwarded-Proto": {"attacker", "https, http"},
					"Forwarded":         {"for=1.2.3.4;proto=https"}, "X-Real-Ip": {"1.2.3.4"},
				}
				req.Trailer = http.Header{"X-Request-Trailer": {"secret"}}
				original := req.Header.Clone()
				h := NewWithTransport(r, roundTripperFunc(func(out *http.Request) (*http.Response, error) {
					for _, key := range []string{"Connection", "Keep-Alive", "X-Internal", "X-Internal-Two", "X-Internal-Three", "Trailer", "Te", "Forwarded", "X-Real-IP"} {
						if values := out.Header.Values(key); len(values) != 0 {
							t.Errorf("request leaked %s: %v", key, values)
						}
					}
					if len(out.Trailer) != 0 {
						t.Errorf("request trailers leaked: %v", out.Trailer)
					}
					if got := out.Header.Values("X-Forwarded-Proto"); !reflect.DeepEqual(got, []string{wantScheme}) {
						t.Errorf("scheme = %v, want %s", got, wantScheme)
					}
					if got := out.Header.Get("X-Forwarded-For"); got != wantIP {
						t.Errorf("client IP = %q, want %q", got, wantIP)
					}
					if out.Header.Get("X-End-To-End") != "preserve" {
						t.Error("request end-to-end header lost")
					}
					return &http.Response{StatusCode: 200, Header: http.Header{
						"Connection": {"x-upstream, X-Upstream-Two", " X-Upstream-Three, keep-alive"},
						"X-Upstream": {"secret"}, "X-Upstream-Two": {"secret"}, "X-Upstream-Three": {"secret"},
						"Keep-Alive": {"timeout=5"}, "Trailer": {"X-Response-Trailer"},
						"X-End-To-End": {"preserve"},
					}, Trailer: http.Header{"X-Response-Trailer": {"secret"}}, Body: io.NopCloser(strings.NewReader("response"))}, nil
				}))
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				for _, key := range []string{"Connection", "Keep-Alive", "X-Upstream", "X-Upstream-Two", "X-Upstream-Three", "Trailer", "X-Response-Trailer"} {
					if values := rec.Header().Values(key); len(values) != 0 {
						t.Errorf("response leaked %s: %v", key, values)
					}
				}
				if rec.Code != 200 || rec.Body.String() != "response" || rec.Header().Get("X-End-To-End") != "preserve" {
					t.Errorf("response changed: %v", rec)
				}
				if !reflect.DeepEqual(req.Header, original) || req.Trailer.Get("X-Request-Trailer") != "secret" {
					t.Error("incoming headers or trailers mutated")
				}
			})
		}
	}
}

func TestProxyDropsTrailersOverHTTP(t *testing.T) {
	for _, secure := range []bool{false, true} {
		for _, retries := range []int{0, 1} {
			t.Run(fmt.Sprintf("TLS=%v/retries=%d", secure, retries), func(t *testing.T) {
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					body, err := io.ReadAll(req.Body)
					if err != nil || string(body) != "upload" {
						t.Errorf("request body = %q, err = %v", body, err)
					}
					if len(req.Trailer) != 0 || req.Header.Get("Trailer") != "" {
						t.Errorf("request trailers leaked: %v %v", req.Header, req.Trailer)
					}
					wantScheme := "http"
					if secure {
						wantScheme = "https"
					}
					if got := req.Header.Values("X-Forwarded-Proto"); !reflect.DeepEqual(got, []string{wantScheme}) {
						t.Errorf("scheme = %v, want %s", got, wantScheme)
					}
					w.Header().Set("Trailer", "X-Response-Trailer")
					fmt.Fprint(w, "response")
					w.Header().Set("X-Response-Trailer", "secret")
					w.Header().Set(http.TrailerPrefix+"X-Late-Trailer", "secret")
				}))
				defer backend.Close()
				r, err := router.New(&config.Config{Routes: []config.Route{{Path: "/", Retries: retries, RetryMethods: []string{"POST"}, Backends: []config.Backend{{URL: backend.URL}}}}})
				if err != nil {
					t.Fatal(err)
				}
				h := New(r)
				defer h.CloseIdleConnections()
				var frontend *httptest.Server
				if secure {
					frontend = httptest.NewTLSServer(h)
				} else {
					frontend = httptest.NewServer(h)
				}
				defer frontend.Close()
				req, err := http.NewRequest("POST", frontend.URL, io.NopCloser(strings.NewReader("upload")))
				if err != nil {
					t.Fatal(err)
				}
				req.Trailer = http.Header{"X-Request-Trailer": {"secret"}}
				req.Header.Set("Idempotency-Key", "upload-key")
				req.Header.Add("X-Forwarded-Proto", "attacker")
				req.Header.Add("X-Forwarded-Proto", "https, http")
				resp, err := frontend.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || string(body) != "response" || resp.StatusCode != 200 {
					t.Fatalf("response = %d %q, err = %v", resp.StatusCode, body, err)
				}
				if len(resp.Trailer) != 0 || resp.Header.Get("Trailer") != "" {
					t.Errorf("response trailers leaked: %v %v", resp.Header, resp.Trailer)
				}
			})
		}
	}
}

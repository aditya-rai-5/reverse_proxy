package proxy

import (
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

func TestProxyLoadedStickyCookieAffinity(t *testing.T) {
	for _, name := range []string{"", "MY_SESSION", "!#$%&'*+-.^_`|~AZaz09"} {
		t.Run(fmt.Sprintf("name=%q", name), func(t *testing.T) {
			raw := fmt.Sprintf("port: 8080\nroutes:\n  - path: /\n    algorithm: sticky\n    cookie_name: %q\n    backends:\n      - url: http://one.example\n      - url: http://two.example\n", name)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
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
			h := NewWithTransport(r, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(req.URL.Host))}, nil
			}))
			first := httptest.NewRecorder()
			h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/", nil))
			cookies := first.Result().Cookies()
			wantName := name
			if wantName == "" {
				wantName = "SERVERID"
			}
			if first.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != wantName || cookies[0].Value == "" {
				t.Fatalf("status=%d cookies=%v, want successful response with %q cookie", first.Code, cookies, wantName)
			}
			for i := 0; i < 5; i++ {
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.AddCookie(cookies[0])
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || rec.Body.String() != first.Body.String() {
					t.Fatalf("request %d: status=%d backend=%q, want %q", i, rec.Code, rec.Body.String(), first.Body.String())
				}
			}
			// Without a cookie, the next selection must use the other backend.
			other := httptest.NewRecorder()
			h.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/", nil))
			if other.Code != http.StatusOK || other.Body.String() == first.Body.String() {
				t.Fatal("request without a cookie did not rotate to the other backend")
			}
		})
	}
}

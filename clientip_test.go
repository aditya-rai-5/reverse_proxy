package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func TestTrustedProxyRateLimits(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer backend.Close()
	for _, scope := range []string{"global", "route"} {
		for _, trust := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/trusted=%v", scope, trust), func(t *testing.T) {
				cfg := &config.Config{Routes: []config.Route{{Path: "/", Backends: []config.Backend{{URL: backend.URL}}}}}
				if trust {
					cfg.TrustedProxies = []string{"10.0.0.5/32"}
				}
				limit := &config.RateLimit{RequestsPerMinute: 1, Burst: 1}
				if scope == "global" {
					cfg.RateLimit = limit
				} else {
					cfg.Routes[0].RateLimit = limit
				}
				r, err := router.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range r.Entries() {
					if entry.RateLimiter != nil {
						t.Cleanup(entry.RateLimiter.Stop)
					}
				}
				h := buildHandler(cfg, r)
				// Changing the real client in a trusted chain creates a separate
				// bucket. Changing only the forged leftmost value never does.
				chains := []string{"1.2.3.4, 198.51.100.1", "1.2.3.5, 198.51.100.2", "1.2.3.6, 198.51.100.1"}
				for i, chain := range chains {
					req := httptest.NewRequest("GET", "/", nil)
					req.RemoteAddr = fmt.Sprintf("10.0.0.5:%d", 1000+i)
					req.Header.Set("X-Forwarded-For", chain)
					req.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i+1))
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					want := http.StatusTooManyRequests
					if i == 0 || (i == 1 && trust) {
						want = http.StatusOK
					}
					if rec.Code != want {
						t.Fatalf("request %d: got %d, want %d", i, rec.Code, want)
					}
				}
			})
		}
	}
}

func TestTrustedProxyForwardedHeaders(t *testing.T) {
	for _, tt := range []struct {
		name            string
		trusted         []string
		peer, xff, want string
	}{
		{"direct", nil, "198.51.100.1:1234", "1.2.3.4", "198.51.100.1"},
		{"trusted", []string{"10.0.0.5/32"}, "10.0.0.5:1234", "1.2.3.4, 198.51.100.1", "198.51.100.1"},
		{"malformed", []string{"10.0.0.5/32"}, "10.0.0.5:1234", "bad, 198.51.100.1", "10.0.0.5"},
		{"IPv6", nil, "[2001:db8::1]:1234", "1.2.3.4", "2001:db8::1"},
		{"invalid peer", nil, "bad", "1.2.3.4", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			received := make(chan http.Header, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				received <- req.Header.Clone()
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			cfg := &config.Config{TrustedProxies: tt.trusted, Routes: []config.Route{{Path: "/", Backends: []config.Backend{{URL: backend.URL}}}}}
			r, err := router.New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.peer
			req.Header.Set("X-Forwarded-For", tt.xff)
			req.Header.Set("X-Real-IP", "1.2.3.4")
			req.Header.Set("Forwarded", "for=1.2.3.4")
			rec := httptest.NewRecorder()
			buildHandler(cfg, r).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			headers := <-received
			if got := headers.Get("X-Forwarded-For"); got != tt.want {
				t.Fatalf("X-Forwarded-For = %q, want %q", got, tt.want)
			}
			for _, key := range []string{"Forwarded", "X-Real-IP"} {
				if _, exists := headers[key]; exists {
					t.Fatalf("unexpected %s: %v", key, headers[key])
				}
			}
			if req.Header.Get("X-Forwarded-For") != tt.xff {
				t.Fatal("incoming headers mutated")
			}
		})
	}
}

func TestTrustedProxyIPHashWiring(t *testing.T) {
	var backends []config.Backend
	for i := 0; i < 3; i++ {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, i) }))
		t.Cleanup(backend.Close)
		backends = append(backends, config.Backend{URL: backend.URL})
	}
	cfg := &config.Config{TrustedProxies: []string{"10.0.0.5/32"}, Routes: []config.Route{{Path: "/", Algorithm: config.AlgorithmIPHash, Backends: backends}}}
	r, err := router.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := buildHandler(cfg, r)
	for i := 1; i <= 32; i++ {
		client := fmt.Sprintf("198.51.100.%d", i)
		direct := httptest.NewRequest("GET", "/", nil)
		direct.RemoteAddr = client + ":1234"
		want := httptest.NewRecorder()
		h.ServeHTTP(want, direct)
		forwarded := httptest.NewRequest("GET", "/", nil)
		forwarded.RemoteAddr = "10.0.0.5:1234"
		forwarded.Header.Set("X-Forwarded-For", "1.2.3.4, "+client)
		got := httptest.NewRecorder()
		h.ServeHTTP(got, forwarded)
		if got.Code != http.StatusOK || got.Body.String() != want.Body.String() {
			t.Fatalf("client %s: got %d %q, want %q", client, got.Code, got.Body.String(), want.Body.String())
		}
	}
}

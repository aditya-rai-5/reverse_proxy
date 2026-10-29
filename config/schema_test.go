package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictSchema(t *testing.T) {
	base := "port: 8080\nroutes:\n  - path: /api\n    backends:\n      - url: http://localhost:9000\n"
	cases := []string{
		"typo: true\n" + base,
		strings.Replace(base, "    backends:", "    typo: true\n    backends:", 1),
		strings.Replace(base, "      - url:", "      - typo: true\n        url:", 1),
		base + "health_check:\n  typo: true\n",
		base + "tls:\n  typo: true\n",
		base + "rate_limit:\n  typo: true\n",
		strings.Replace(base, "    backends:", "    rewrite: {pattern: x, typo: y}\n    backends:", 1),
		strings.Replace(base, "    backends:", "    circuit_breaker: {typo: 1}\n    backends:", 1),
		strings.Replace(base, "    backends:", "    rate_limit: {typo: 1}\n    backends:", 1),
		base + "---\n" + base,
		base + "port: 9090\n",
		base + "---\n",
	}
	for _, data := range cases {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("accepted invalid schema")
			}
		})
	}
}

func TestValidationBoundaries(t *testing.T) {
	for _, path := range []string{"", "api", "//api", "/a?b", "/a#b", "/a%2Fb", "/a/../b", "/a b", "/a\\b", "/a//b"} {
		if err := validatePath(path); err == nil {
			t.Errorf("accepted path %q", path)
		}
	}
	for _, raw := range []string{"", "localhost:80", "/relative", "ftp://host", "http://", "http://host:0", "http://host:65536", "http://host:", "http://host:abc", "http://user:pass@host", "http://host/base", "http://host?q=x", "http://host#", "http://[garbage]", "http://::1", "http://bad_host"} {
		if err := validateBackend(raw); err == nil {
			t.Errorf("accepted backend %q", raw)
		}
	}
	for _, raw := range []string{"http://localhost", "https://example.com:65535/", "http://[::1]:80", "http://127.0.0.1:1"} {
		if err := validateBackend(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	for _, port := range []int{-1, 0, 65536} {
		cfg := &Config{Port: port, Routes: []Route{{Path: "/", Backends: []Backend{{URL: "http://localhost"}}}}}
		if err := validate(cfg); err == nil {
			t.Errorf("accepted port %d", port)
		}
	}
	cfg := &Config{Port: 8080, Routes: []Route{
		{Path: "/api", Backends: []Backend{{URL: "http://localhost"}}},
		{Path: "/api", Backends: []Backend{{URL: "http://localhost"}}},
	}}
	if err := validate(cfg); err == nil {
		t.Fatal("accepted duplicate routes")
	}
}

func TestBackendUniqueness(t *testing.T) {
	for _, tc := range []struct {
		name, routes string
		wantError    bool
	}{
		{"duplicate", "- path: /\n  backends: [{url: 'http://one.example'}, {url: 'http://one.example', weight: 2}]", true},
		{"parsed duplicate", "- path: /\n  backends: [{url: 'HTTP://one.example'}, {url: 'http://one.example'}]", true},
		{"distinct", "- path: /\n  backends: [{url: 'http://one.example', weight: 2}, {url: 'http://two.example'}]", false},
		{"different routes", "- path: /a\n  backends: [{url: 'http://one.example'}]\n- path: /b\n  backends: [{url: 'http://one.example'}]", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("port: 8080\nroutes:\n"+tc.routes+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "route[0].backend[1]: duplicate backend URL") {
					t.Fatalf("expected duplicate backend error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

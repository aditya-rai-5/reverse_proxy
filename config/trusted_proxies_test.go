package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTrustedProxies(t *testing.T) {
	for _, tt := range []struct {
		name, setting string
		wantError     bool
	}{
		{"omitted", "", false},
		{"empty", "trusted_proxies: []\n", false},
		{"valid", "trusted_proxies: [10.0.0.5/32, '2001:db8::/64']\n", false},
		{"invalid", "trusted_proxies: [10.0.0.5/32, bad]\n", true},
		{"bare IP", "trusted_proxies: [10.0.0.5]\n", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := tt.setting + "port: 8080\nroutes:\n  - path: /\n    backends:\n      - url: http://localhost:9000\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "trusted_proxies[") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.name == "valid" && len(cfg.TrustedProxies) != 2 {
				t.Fatalf("trusted proxies = %v", cfg.TrustedProxies)
			}
		})
	}
}

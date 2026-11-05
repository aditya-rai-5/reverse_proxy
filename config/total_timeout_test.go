package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTotalTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		name, value, wantErr string
		want                 time.Duration
	}{
		{name: "omitted"}, {name: "disabled", value: "0s"},
		{name: "positive", value: "250ms", want: 250 * time.Millisecond},
		{name: "negative", value: "-1s", wantErr: "total_timeout must be non-negative"},
		{name: "malformed", value: "soon", wantErr: "failed to parse"},
		{name: "overflow", value: "999999999999999999h", wantErr: "failed to parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			setting := ""
			if tc.value != "" {
				setting = "    total_timeout: " + tc.value + "\n"
			}
			if err := os.WriteFile(path, []byte("port: 8080\nroutes:\n  - path: /\n"+setting+"    backends: [{url: http://localhost:9000}]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error=%v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Routes[0].TotalTimeout != tc.want {
				t.Fatalf("timeout=%v, want %v", cfg.Routes[0].TotalTimeout, tc.want)
			}
		})
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerConfig(t *testing.T) {
	defaults := Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20}
	for _, tc := range []struct {
		name, settings, errContains string
		want                        Server
	}{
		{"omitted", "", "", defaults},
		{"empty", "server: {}", "", defaults},
		{"zero", "server: {read_header_timeout: 0s, idle_timeout: 0s, max_header_bytes: 0}", "", defaults},
		{"partial", "server: {idle_timeout: 2m}", "", Server{5 * time.Second, 2 * time.Minute, 1 << 20}},
		{"custom", "server: {read_header_timeout: 250ms, idle_timeout: 30s, max_header_bytes: 8192}", "", Server{250 * time.Millisecond, 30 * time.Second, 8192}},
		{"negative header timeout", "server: {read_header_timeout: -1s}", "server timeouts", Server{}},
		{"negative idle timeout", "server: {idle_timeout: -1s}", "server timeouts", Server{}},
		{"negative limit", "server: {max_header_bytes: -1}", "server.max_header_bytes", Server{}},
		{"malformed header timeout", "server: {read_header_timeout: soon}", "failed to parse", Server{}},
		{"malformed idle timeout", "server: {idle_timeout: soon}", "failed to parse", Server{}},
		{"overflow duration", "server: {idle_timeout: 999999999999999999999h}", "failed to parse", Server{}},
		{"malformed limit", "server: {max_header_bytes: large}", "failed to parse", Server{}},
		{"overflow limit", "server: {max_header_bytes: 999999999999999999999}", "failed to parse", Server{}},
		{"unknown field", "server: {header_timeout: 5s}", "field header_timeout", Server{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := "port: 8080\n" + tc.settings + "\nroutes: [{path: /, backends: [{url: http://localhost:9000}]}]\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tc.errContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errContains) {
					t.Fatalf("error=%v; want %q", err, tc.errContains)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Server != tc.want {
				t.Fatalf("settings=%+v want=%+v", cfg.Server, tc.want)
			}
		})
	}
}

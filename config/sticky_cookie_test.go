package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStickyCookieName(t *testing.T) {
	tests := []struct {
		name, algorithm, field, want string
		invalid                      bool
	}{
		{name: "omitted", want: "SERVERID"},
		{name: "empty", field: `cookie_name: ""`, want: "SERVERID"},
		{name: "custom", field: `cookie_name: MY_SESSION`, want: "MY_SESSION"},
		{name: "token punctuation", field: "cookie_name: \"!#$%&'*+-.^_`|~AZaz09\"", want: "!#$%&'*+-.^_`|~AZaz09"},
		{name: "alias", algorithm: "cookie", field: `cookie_name: SESSION`, want: "SESSION"},
		{name: "space", field: `cookie_name: "bad name"`, invalid: true},
		{name: "whitespace only", field: `cookie_name: " "`, invalid: true},
		{name: "semicolon", field: `cookie_name: "bad;name"`, invalid: true},
		{name: "tab", field: `cookie_name: "bad\tname"`, invalid: true},
		{name: "newline", field: `cookie_name: "bad\nname"`, invalid: true},
		{name: "null", field: `cookie_name: "bad\0name"`, invalid: true},
		{name: "non ASCII", field: `cookie_name: "café"`, invalid: true},
		{name: "equals", field: `cookie_name: "bad=name"`, invalid: true},
		{name: "invalid alias", algorithm: "cookie", field: `cookie_name: "bad name"`, invalid: true},
		{name: "unused on round robin", algorithm: "round_robin", field: `cookie_name: "bad name"`, want: "bad name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			algorithm := tt.algorithm
			if algorithm == "" {
				algorithm = "sticky"
			}
			// Keep the sticky route second to verify the error identifies its index.
			raw := fmt.Sprintf("port: 8080\nroutes:\n  - path: /other\n    backends:\n      - url: http://other.example\n  - path: /\n    algorithm: %s\n    %s\n    backends:\n      - url: http://one.example\n", algorithm, tt.field)
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "route[1].cookie_name") {
					t.Fatalf("expected route-specific cookie_name error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Routes[1].CookieName; got != tt.want {
				t.Fatalf("cookie name = %q, want %q", got, tt.want)
			}
		})
	}
}

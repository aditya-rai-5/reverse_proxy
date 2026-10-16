package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadRetryMethods(t *testing.T) {
	for _, tt := range []struct {
		name, setting string
		want          []string
		wantError     bool
	}{
		{"omitted defaults", "", []string{"GET", "HEAD", "OPTIONS"}, false},
		{"explicit empty disables", "    retry_methods: []\n", []string{}, false},
		{"write opt-in normalized", "    retry_methods: [get, ' post ', PATCH, POST]\n", []string{"GET", "POST", "PATCH"}, false},
		{"unknown method", "    retry_methods: [GET, MAGIC]\n", nil, true},
		{"blank method", "    retry_methods: ['']\n", nil, true},
		{"no CONNECT tunneling", "    retry_methods: [CONNECT]\n", nil, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			data := "port: 8080\nroutes:\n  - path: /\n    retries: 1\n" + tt.setting + "    backends:\n      - url: http://one.example\n      - url: http://two.example\n"
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "route[0]: unsupported retry method") {
					t.Fatalf("expected contextual validation error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.Routes[0].RetryMethods, tt.want) {
				t.Fatalf("methods=%v want=%v", cfg.Routes[0].RetryMethods, tt.want)
			}
			// Explicit empty policy must survive exporting a loaded config.
			encoded, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			reloaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded.Routes[0].RetryMethods, tt.want) {
				t.Fatal("retry policy changed after YAML round trip")
			}
		})
	}
}

func TestBundledConfigsRemainCompatibleWithSafeRetryDefaults(t *testing.T) {
	for _, path := range []string{"../config.yaml", "../demo/config.yaml"} {
		t.Run(path, func(t *testing.T) {
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range cfg.Routes {
				if !reflect.DeepEqual(route.RetryMethods, []string{"GET", "HEAD", "OPTIONS"}) {
					t.Errorf("route %s methods=%v", route.Path, route.RetryMethods)
				}
			}
		})
	}
}

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadRetryBudget(t *testing.T) {
	for _, tt := range []struct {
		name     string
		setting  string
		backends int
		want     int
		wantErr  string
	}{
		{name: "omitted single backend", backends: 1, want: 0},
		{name: "omitted two backends", backends: 2, want: 1},
		{name: "omitted three backends", backends: 3, want: 2},
		{name: "omitted capped at two", backends: 5, want: 2},
		{name: "zero single backend", setting: "retries: 0", backends: 1, want: 0},
		{name: "zero two backends", setting: "retries: 0", backends: 2, want: 0},
		{name: "zero many backends", setting: "retries: 0", backends: 5, want: 0},
		{name: "positive below default", setting: "retries: 1", backends: 5, want: 1},
		{name: "positive above default cap", setting: "retries: 4", backends: 5, want: 4},
		{name: "positive single backend", setting: "retries: 3", backends: 1, want: 3},
		{name: "null keeps default", setting: "retries: null", backends: 2, want: 1},
		{name: "negative rejected", setting: "retries: -1", backends: 2, wantErr: "route[0]: retries must be non-negative"},
		{name: "string rejected", setting: "retries: nope", backends: 2, wantErr: "failed to parse config file"},
		{name: "boolean rejected", setting: "retries: false", backends: 2, wantErr: "failed to parse config file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var data strings.Builder
			fmt.Fprintf(&data, "port: 8080\nroutes:\n  - path: /\n    %s\n    backends:\n", tt.setting)
			for i := 0; i < tt.backends; i++ {
				fmt.Fprintf(&data, "      - url: http://backend%d.example\n", i)
			}
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(data.String()), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error=%v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Routes[0].Retries; got != tt.want {
				t.Fatalf("retries=%d, want %d", got, tt.want)
			}
			// Exporting and reloading must preserve the effective budget,
			// especially a disabled route with more than one backend.
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
			if got := reloaded.Routes[0].Retries; got != tt.want {
				t.Fatalf("retries after YAML round trip=%d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoadRetryBudgetYAMLMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := `port: 8080
routes:
  - &defaults
    path: /disabled
    retries: 0
    backends:
      - url: http://one.example
      - url: http://two.example
  - <<: *defaults
    path: /enabled
    retries: 2
  - path: /automatic
    backends:
      - url: http://one.example
      - url: http://two.example
`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{0, 2, 1} {
		if got := cfg.Routes[i].Retries; got != want {
			t.Errorf("route %s retries=%d, want %d", cfg.Routes[i].Path, got, want)
		}
	}
}

func TestProgrammaticZeroRetryBudget(t *testing.T) {
	cfg := &Config{Port: 8080, Routes: []Route{{
		Path: "/", Retries: 0,
		Backends: []Backend{{URL: "http://one.example"}, {URL: "http://two.example"}},
	}}}
	if err := validate(cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Routes[0].Retries; got != 0 {
		t.Fatalf("validation replaced explicit zero with %d retries", got)
	}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := yaml.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := decoded.Routes[0].Retries; got != 0 {
		t.Fatalf("exporting programmatic zero enabled %d retries", got)
	}
}

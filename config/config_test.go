package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	validYAML := []byte(`
port: 8080
routes:
  - path: "/api"
    backends:
      - url: "http://localhost:9001"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, validYAML, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("Expected port 8080, got %d", cfg.Port)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("Expected 1 route, got %d", len(cfg.Routes))
	}
	if cfg.Routes[0].Path != "/api" {
		t.Errorf("Expected route path '/api', got %q", cfg.Routes[0].Path)
	}
	if len(cfg.Routes[0].Backends) != 1 {
		t.Fatalf("Expected 1 backend, got %d", len(cfg.Routes[0].Backends))
	}
	if cfg.Routes[0].Backends[0].URL != "http://localhost:9001" {
		t.Errorf("Expected backend URL 'http://localhost:9001', got %q", cfg.Routes[0].Backends[0].URL)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		cfg         *Config
		expectError bool
	}{
		{
			name: "Valid Config",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: false,
		},
		{
			name: "Missing Port",
			cfg: &Config{
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: true,
		},
		{
			name: "Missing Routes",
			cfg: &Config{
				Port: 8080,
			},
			expectError: true,
		},
		{
			name: "Empty Route Path",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{Path: "", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: true,
		},
		{
			name: "Missing Backends",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{Path: "/"},
				},
			},
			expectError: true,
		},
		{
			name: "Empty Backend URL",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: ""}}},
				},
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validate(tt.cfg)
			if (err != nil) != tt.expectError {
				t.Errorf("validate() error = %v, expectError %v", err, tt.expectError)
			}
		})
	}
}

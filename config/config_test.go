package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigWithDefaults(t *testing.T) {
	validYAML := []byte(`
port: 8080
routes:
  - path: "/api"
    strip_prefix: "/api"
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
	if cfg.Routes[0].StripPrefix != "/api" {
		t.Errorf("Expected strip_prefix '/api', got %q", cfg.Routes[0].StripPrefix)
	}
	if len(cfg.Routes[0].Backends) != 1 {
		t.Fatalf("Expected 1 backend, got %d", len(cfg.Routes[0].Backends))
	}
	if cfg.Routes[0].Backends[0].URL != "http://localhost:9001" {
		t.Errorf("Expected backend URL 'http://localhost:9001', got %q", cfg.Routes[0].Backends[0].URL)
	}

	// Verify health check defaults
	if cfg.HealthCheck.Path != "/health" {
		t.Errorf("Expected default health check path '/health', got %q", cfg.HealthCheck.Path)
	}
	if cfg.HealthCheck.Interval != 10*time.Second {
		t.Errorf("Expected default interval 10s, got %v", cfg.HealthCheck.Interval)
	}
	if cfg.HealthCheck.Timeout != 5*time.Second {
		t.Errorf("Expected default timeout 5s, got %v", cfg.HealthCheck.Timeout)
	}
}

func TestLoadConfigWithRewrite(t *testing.T) {
	yamlData := []byte(`
port: 8080
routes:
  - path: "/legacy"
    rewrite:
      pattern: "^/legacy/(.*)$"
      replacement: "/v2/$1"
    backends:
      - url: "http://localhost:9001"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, yamlData, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if cfg.Routes[0].Rewrite == nil {
		t.Fatal("Expected rewrite rule to be loaded, got nil")
	}
	if cfg.Routes[0].Rewrite.Pattern != "^/legacy/(.*)$" {
		t.Errorf("Expected pattern '^/legacy/(.*)$', got %q", cfg.Routes[0].Rewrite.Pattern)
	}
	if cfg.Routes[0].Rewrite.Replacement != "/v2/$1" {
		t.Errorf("Expected replacement '/v2/$1', got %q", cfg.Routes[0].Rewrite.Replacement)
	}
}

func TestLoadConfigCustomHealthCheck(t *testing.T) {
	customYAML := []byte(`
port: 9090
routes:
  - path: "/v1"
    backends:
      - url: "http://localhost:8001"
health_check:
  path: "/ping"
  interval: 2s
  timeout: 1s
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, customYAML, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if cfg.HealthCheck.Path != "/ping" {
		t.Errorf("Expected custom path '/ping', got %q", cfg.HealthCheck.Path)
	}
	if cfg.HealthCheck.Interval != 2*time.Second {
		t.Errorf("Expected custom interval 2s, got %v", cfg.HealthCheck.Interval)
	}
	if cfg.HealthCheck.Timeout != 1*time.Second {
		t.Errorf("Expected custom timeout 1s, got %v", cfg.HealthCheck.Timeout)
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
			name: "Valid Config With StripPrefix and Rewrite",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:        "/api",
						StripPrefix: "/api",
						Rewrite: &RewriteRule{
							Pattern:     "^/v1/(.*)$",
							Replacement: "/v2/$1",
						},
						Backends: []Backend{{URL: "http://localhost"}},
					},
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
			name: "Invalid StripPrefix Without Leading Slash",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:        "/api",
						StripPrefix: "api", // Missing leading slash
						Backends:    []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Empty Rewrite Pattern",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:     "/api",
						Rewrite:  &RewriteRule{Pattern: "", Replacement: "/test"},
						Backends: []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Invalid Rewrite Regex",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:     "/api",
						Rewrite:  &RewriteRule{Pattern: "[a-z", Replacement: "/test"},
						Backends: []Backend{{URL: "http://localhost"}},
					},
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

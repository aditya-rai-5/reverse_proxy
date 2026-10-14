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
		{
			name: "Negative Backend Weight",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost", Weight: -1}}},
				},
			},
			expectError: true,
		},
		{
			name: "Unsupported Algorithm",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:      "/",
						Algorithm: "magic_balance",
						Backends:  []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Valid Sticky Algorithm Sets Default Cookie",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:      "/",
						Algorithm: "sticky",
						Backends:  []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: false,
		},
		{
			name: "Negative Retries",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:     "/",
						Retries:  -1,
						Backends: []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Negative Timeout",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path:     "/",
						Timeout:  -1 * time.Second,
						Backends: []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Negative Global Rate Limit RequestsPerMinute",
			cfg: &Config{
				Port: 8080,
				RateLimit: &RateLimit{
					RequestsPerMinute: -1,
					Burst:             10,
				},
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: true,
		},
		{
			name: "Negative Global Rate Limit Burst",
			cfg: &Config{
				Port: 8080,
				RateLimit: &RateLimit{
					RequestsPerMinute: 60,
					Burst:             -1,
				},
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: true,
		},
		{
			name: "Negative Route Rate Limit",
			cfg: &Config{
				Port: 8080,
				Routes: []Route{
					{
						Path: "/",
						RateLimit: &RateLimit{
							RequestsPerMinute: -5,
							Burst:             2,
						},
						Backends: []Backend{{URL: "http://localhost"}},
					},
				},
			},
			expectError: true,
		},
		{
			name: "Valid Rate Limit Defaults Burst",
			cfg: &Config{
				Port: 8080,
				RateLimit: &RateLimit{
					RequestsPerMinute: 60,
					Burst:             0,
				},
				Routes: []Route{
					{Path: "/", Backends: []Backend{{URL: "http://localhost"}}},
				},
			},
			expectError: false,
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

func TestValidateTLSModesAndDefaults(t *testing.T) {
	t.Run("manual certificate defaults listener ports", func(t *testing.T) {
		cfg := &Config{
			Port:   8080,
			TLS:    &TLS{CertFile: "server.crt", KeyFile: "server.key"},
			Routes: []Route{{Path: "/", Backends: []Backend{{URL: "http://localhost"}}}},
		}
		if err := validate(cfg); err != nil {
			t.Fatalf("validate returned error: %v", err)
		}
		if cfg.TLS.HTTPPort != 80 || cfg.TLS.HTTPSPort != 443 {
			t.Fatalf("expected HTTP/HTTPS ports 80/443, got %d/%d", cfg.TLS.HTTPPort, cfg.TLS.HTTPSPort)
		}
	})

	t.Run("autocert defaults cache and listener ports", func(t *testing.T) {
		cfg := &Config{
			Port:   8080,
			TLS:    &TLS{AutoCert: true, Domains: []string{"proxy.example.com"}},
			Routes: []Route{{Path: "/", Backends: []Backend{{URL: "http://localhost"}}}},
		}
		if err := validate(cfg); err != nil {
			t.Fatalf("validate returned error: %v", err)
		}
		if cfg.TLS.HTTPPort != 80 || cfg.TLS.HTTPSPort != 443 || cfg.TLS.CacheDir == "" {
			t.Fatalf("unexpected autocert defaults: %+v", cfg.TLS)
		}
	})
}

func TestLoadTLSConfig(t *testing.T) {
	yamlData := []byte(`
port: 8080
tls:
  auto_cert: true
  domains:
    - proxy.example.com
  email: admin@example.com
  cache_dir: /tmp/proxy-certs
  http_port: 8081
  https_port: 8443
routes:
  - path: /
    backends:
      - url: http://localhost:9000
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, yamlData, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.TLS == nil || !cfg.TLS.AutoCert {
		t.Fatal("expected autocert configuration to be loaded")
	}
	if cfg.TLS.HTTPSPort != 8443 || cfg.TLS.HTTPPort != 8081 || cfg.TLS.CacheDir != "/tmp/proxy-certs" {
		t.Fatalf("unexpected TLS config: %+v", cfg.TLS)
	}
	if len(cfg.TLS.Domains) != 1 || cfg.TLS.Domains[0] != "proxy.example.com" {
		t.Fatalf("unexpected TLS domains: %v", cfg.TLS.Domains)
	}
}

func TestValidateRejectsInvalidTLSConfig(t *testing.T) {
	tests := []struct {
		name string
		tls  *TLS
	}{
		{name: "incomplete manual certificate", tls: &TLS{CertFile: "server.crt"}},
		{name: "autocert without domains", tls: &TLS{AutoCert: true}},
		{name: "manual and automatic certificates combined", tls: &TLS{AutoCert: true, Domains: []string{"proxy.example.com"}, CertFile: "server.crt", KeyFile: "server.key"}},
		{name: "same HTTP and HTTPS port", tls: &TLS{CertFile: "server.crt", KeyFile: "server.key", HTTPPort: 8443, HTTPSPort: 8443}},
		{name: "invalid HTTPS port", tls: &TLS{CertFile: "server.crt", KeyFile: "server.key", HTTPSPort: 70000}},
		{name: "empty autocert domain", tls: &TLS{AutoCert: true, Domains: []string{"  "}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Port:   8080,
				TLS:    tt.tls,
				Routes: []Route{{Path: "/", Backends: []Backend{{URL: "http://localhost"}}}},
			}
			if err := validate(cfg); err == nil {
				t.Fatal("expected invalid TLS configuration to be rejected")
			}
		})
	}
}

func TestLoadConfigRetriesAndCircuitBreaker(t *testing.T) {
	yamlData := []byte(`
port: 8080
routes:
  - path: "/resilient"
    retries: 3
    timeout: 2s
    circuit_breaker:
      failure_threshold: 4
      cooldown: 5s
    backends:
      - url: "http://srv1"
      - url: "http://srv2"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, yamlData, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	r := cfg.Routes[0]
	if r.Retries != 3 {
		t.Errorf("Expected retries 3, got %d", r.Retries)
	}
	if r.Timeout != 2*time.Second {
		t.Errorf("Expected timeout 2s, got %v", r.Timeout)
	}
	if r.CircuitBreaker == nil {
		t.Fatal("Expected CircuitBreaker to be non-nil")
	}
	if r.CircuitBreaker.FailureThreshold != 4 {
		t.Errorf("Expected failure_threshold 4, got %d", r.CircuitBreaker.FailureThreshold)
	}
	if r.CircuitBreaker.Cooldown != 5*time.Second {
		t.Errorf("Expected cooldown 5s, got %v", r.CircuitBreaker.Cooldown)
	}
}

func TestLoadConfigRateLimit(t *testing.T) {
	yamlData := []byte(`
port: 8080
rate_limit:
  requests_per_minute: 60
  burst: 10
routes:
  - path: "/api"
    rate_limit:
      requests_per_minute: 120
      burst: 20
    backends:
      - url: "http://srv1"
  - path: "/"
    backends:
      - url: "http://srv2"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, yamlData, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if cfg.RateLimit == nil {
		t.Fatal("Expected global RateLimit to be loaded")
	}
	if cfg.RateLimit.RequestsPerMinute != 60 {
		t.Errorf("Expected global requests_per_minute 60, got %d", cfg.RateLimit.RequestsPerMinute)
	}
	if cfg.RateLimit.Burst != 10 {
		t.Errorf("Expected global burst 10, got %d", cfg.RateLimit.Burst)
	}

	r0 := cfg.Routes[0]
	if r0.RateLimit == nil {
		t.Fatal("Expected route 0 RateLimit to be loaded")
	}
	if r0.RateLimit.RequestsPerMinute != 120 {
		t.Errorf("Expected route 0 requests_per_minute 120, got %d", r0.RateLimit.RequestsPerMinute)
	}
	if r0.RateLimit.Burst != 20 {
		t.Errorf("Expected route 0 burst 20, got %d", r0.RateLimit.Burst)
	}

	r1 := cfg.Routes[1]
	if r1.RateLimit != nil {
		t.Errorf("Expected route 1 RateLimit to be nil, got %+v", r1.RateLimit)
	}
}

func TestLoadConfigAlgorithmsAndWeights(t *testing.T) {
	yamlData := []byte(`
port: 8080
routes:
  - path: "/weighted"
    algorithm: "weighted_round_robin"
    backends:
      - url: "http://server1"
        weight: 3
      - url: "http://server2"
        weight: 1
  - path: "/least-conn"
    algorithm: "least_conn"
    backends:
      - url: "http://server1"
      - url: "http://server2"
  - path: "/sticky"
    algorithm: "sticky"
    cookie_name: "MY_SESSION"
    backends:
      - url: "http://server1"
  - path: "/iphash"
    algorithm: "ip_hash"
    backends:
      - url: "http://server1"
  - path: "/inferred-weighted"
    backends:
      - url: "http://server1"
        weight: 2
      - url: "http://server2"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, yamlData, 0644); err != nil {
		t.Fatalf("Failed to write temp file: %v", err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if len(cfg.Routes) != 5 {
		t.Fatalf("Expected 5 routes, got %d", len(cfg.Routes))
	}

	r0 := cfg.Routes[0]
	if r0.Algorithm != AlgorithmWeightedRoundRobin {
		t.Errorf("Route 0: expected algorithm %q, got %q", AlgorithmWeightedRoundRobin, r0.Algorithm)
	}
	if r0.Backends[0].Weight != 3 || r0.Backends[1].Weight != 1 {
		t.Errorf("Route 0: unexpected weights %d, %d", r0.Backends[0].Weight, r0.Backends[1].Weight)
	}

	r1 := cfg.Routes[1]
	if r1.Algorithm != AlgorithmLeastConn {
		t.Errorf("Route 1: expected algorithm %q, got %q", AlgorithmLeastConn, r1.Algorithm)
	}
	if r1.Backends[0].Weight != 1 {
		t.Errorf("Route 1: expected default weight 1, got %d", r1.Backends[0].Weight)
	}

	r2 := cfg.Routes[2]
	if r2.Algorithm != AlgorithmSticky {
		t.Errorf("Route 2: expected algorithm %q, got %q", AlgorithmSticky, r2.Algorithm)
	}
	if r2.CookieName != "MY_SESSION" {
		t.Errorf("Route 2: expected cookie_name 'MY_SESSION', got %q", r2.CookieName)
	}

	r3 := cfg.Routes[3]
	if r3.Algorithm != AlgorithmIPHash {
		t.Errorf("Route 3: expected algorithm %q, got %q", AlgorithmIPHash, r3.Algorithm)
	}

	r4 := cfg.Routes[4]
	if r4.Algorithm != AlgorithmWeightedRoundRobin {
		t.Errorf("Route 4: expected inferred algorithm %q, got %q", AlgorithmWeightedRoundRobin, r4.Algorithm)
	}
	if r4.Backends[1].Weight != 1 {
		t.Errorf("Route 4: expected backend 1 weight defaulted to 1, got %d", r4.Backends[1].Weight)
	}
}

func TestNormalizeAlgorithmAliases(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"", AlgorithmRoundRobin},
		{"round_robin", AlgorithmRoundRobin},
		{"round-robin", AlgorithmRoundRobin},
		{"rr", AlgorithmRoundRobin},
		{"weighted_round_robin", AlgorithmWeightedRoundRobin},
		{"weighted-round-robin", AlgorithmWeightedRoundRobin},
		{"wrr", AlgorithmWeightedRoundRobin},
		{"least_conn", AlgorithmLeastConn},
		{"least-conn", AlgorithmLeastConn},
		{"least_connections", AlgorithmLeastConn},
		{"lc", AlgorithmLeastConn},
		{"ip_hash", AlgorithmIPHash},
		{"ip-hash", AlgorithmIPHash},
		{"sticky", AlgorithmSticky},
		{"sticky_sessions", AlgorithmSticky},
		{"cookie", AlgorithmSticky},
	}

	for _, c := range cases {
		norm, err := NormalizeAlgorithm(c.input)
		if err != nil {
			t.Errorf("NormalizeAlgorithm(%q) returned unexpected error: %v", c.input, err)
		}
		if norm != c.expected {
			t.Errorf("NormalizeAlgorithm(%q) = %q, expected %q", c.input, norm, c.expected)
		}
	}
}

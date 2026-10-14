package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	AlgorithmRoundRobin         = "round_robin"
	AlgorithmWeightedRoundRobin = "weighted_round_robin"
	AlgorithmLeastConn          = "least_conn"
	AlgorithmIPHash             = "ip_hash"
	AlgorithmSticky             = "sticky"
)

// NormalizeAlgorithm standardizes algorithm names and aliases.
func NormalizeAlgorithm(algo string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(algo)) {
	case "", "round_robin", "round-robin", "roundrobin", "rr":
		return AlgorithmRoundRobin, nil
	case "weighted_round_robin", "weighted-round-robin", "weighted", "wrr":
		return AlgorithmWeightedRoundRobin, nil
	case "least_conn", "least-conn", "least_connections", "least-connections", "lc":
		return AlgorithmLeastConn, nil
	case "ip_hash", "ip-hash", "iphash":
		return AlgorithmIPHash, nil
	case "sticky", "sticky_session", "sticky_sessions", "sticky-session", "sticky-sessions", "cookie":
		return AlgorithmSticky, nil
	default:
		return "", fmt.Errorf("unsupported load balancing algorithm %q", algo)
	}
}

type Backend struct {
	URL    string `yaml:"url"`
	Weight int    `yaml:"weight,omitempty"`
}

// RewriteRule defines regex pattern replacement for a route's path.
type RewriteRule struct {
	Pattern     string `yaml:"pattern"`
	Replacement string `yaml:"replacement"`
}

// CircuitBreakerConfig holds settings for backend failure detection and recovery.
type CircuitBreakerConfig struct {
	FailureThreshold int           `yaml:"failure_threshold"`
	Cooldown         time.Duration `yaml:"cooldown"`
}

// Defaults fills in zero-value fields with sensible defaults.
func (cb *CircuitBreakerConfig) Defaults() {
	if cb.FailureThreshold <= 0 {
		cb.FailureThreshold = 5
	}
	if cb.Cooldown <= 0 {
		cb.Cooldown = 10 * time.Second
	}
}

// RateLimit holds configuration for token bucket rate limiting.
type RateLimit struct {
	RequestsPerMinute int `yaml:"requests_per_minute" json:"requests_per_minute"`
	Burst             int `yaml:"burst" json:"burst"`
}

func (rl *RateLimit) Enabled() bool {
	return rl != nil && rl.RequestsPerMinute > 0
}

func (rl *RateLimit) Defaults() {
	if rl.RequestsPerMinute > 0 && rl.Burst <= 0 {
		rl.Burst = 1
	}
}

type Route struct {
	Path           string                `yaml:"path"`
	StripPrefix    string                `yaml:"strip_prefix"`
	Rewrite        *RewriteRule          `yaml:"rewrite"`
	Algorithm      string                `yaml:"algorithm,omitempty"`
	CookieName     string                `yaml:"cookie_name,omitempty"`
	Retries        int                   `yaml:"retries,omitempty"`
	Timeout        time.Duration         `yaml:"timeout,omitempty"`
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker,omitempty"`
	RateLimit      *RateLimit            `yaml:"rate_limit,omitempty"`
	Backends       []Backend             `yaml:"backends"`
}

// HealthCheck holds configuration for backend health probing.
type HealthCheck struct {
	Path     string        `yaml:"path"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// defaults fills in zero-value fields with sensible defaults.
func (hc *HealthCheck) defaults() {
	if hc.Path == "" {
		hc.Path = "/health"
	}
	if hc.Interval <= 0 {
		hc.Interval = 10 * time.Second
	}
	if hc.Timeout <= 0 {
		hc.Timeout = 5 * time.Second
	}
}

type Config struct {
	Port                int         `yaml:"port"`
	MaxRequestBodyBytes int64       `yaml:"max_request_body_bytes"`
	RateLimit           *RateLimit  `yaml:"rate_limit,omitempty"`
	Routes              []Route     `yaml:"routes"`
	HealthCheck         HealthCheck `yaml:"health_check"`
}

const DefaultMaxRequestBodyBytes int64 = 10 << 20 // 10 MiB

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Apply defaults before validation so validate() sees resolved values.
	cfg.HealthCheck.defaults()

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Port == 0 {
		return fmt.Errorf("port must be specified")
	}
	if cfg.MaxRequestBodyBytes < 0 {
		return fmt.Errorf("max_request_body_bytes must be non-negative")
	}
	if cfg.MaxRequestBodyBytes == 0 {
		cfg.MaxRequestBodyBytes = DefaultMaxRequestBodyBytes
	}
	if cfg.RateLimit != nil {
		if cfg.RateLimit.RequestsPerMinute < 0 {
			return fmt.Errorf("rate_limit: requests_per_minute must be non-negative")
		}
		if cfg.RateLimit.Burst < 0 {
			return fmt.Errorf("rate_limit: burst must be non-negative")
		}
		cfg.RateLimit.Defaults()
	}
	if len(cfg.Routes) == 0 {
		return fmt.Errorf("at least one route must be defined")
	}
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		if route.Path == "" {
			return fmt.Errorf("route[%d]: path must not be empty", i)
		}
		if route.StripPrefix != "" && !strings.HasPrefix(route.StripPrefix, "/") {
			return fmt.Errorf("route[%d]: strip_prefix must start with '/'", i)
		}
		if route.Rewrite != nil {
			if route.Rewrite.Pattern == "" {
				return fmt.Errorf("route[%d]: rewrite pattern must not be empty", i)
			}
			if _, err := regexp.Compile(route.Rewrite.Pattern); err != nil {
				return fmt.Errorf("route[%d]: invalid rewrite pattern %q: %w", i, route.Rewrite.Pattern, err)
			}
		}
		if len(route.Backends) == 0 {
			return fmt.Errorf("route[%d]: at least one backend must be defined", i)
		}

		// Normalize & validate algorithm
		normalizedAlgo, err := NormalizeAlgorithm(route.Algorithm)
		if err != nil {
			return fmt.Errorf("route[%d]: %w", i, err)
		}

		hasCustomWeight := false
		for j := range route.Backends {
			backend := &route.Backends[j]
			if backend.URL == "" {
				return fmt.Errorf("route[%d].backend[%d]: url must not be empty", i, j)
			}
			if backend.Weight < 0 {
				return fmt.Errorf("route[%d].backend[%d]: weight must be non-negative", i, j)
			}
			if backend.Weight == 0 {
				backend.Weight = 1
			}
			if backend.Weight > 1 {
				hasCustomWeight = true
			}
		}

		if route.Algorithm == "" && hasCustomWeight {
			normalizedAlgo = AlgorithmWeightedRoundRobin
		}
		route.Algorithm = normalizedAlgo

		if route.Algorithm == AlgorithmSticky && route.CookieName == "" {
			route.CookieName = "SERVERID"
		}

		if route.Retries < 0 {
			return fmt.Errorf("route[%d]: retries must be non-negative", i)
		}
		if route.Retries == 0 && len(route.Backends) > 1 {
			route.Retries = len(route.Backends) - 1
			if route.Retries > 2 {
				route.Retries = 2
			}
		}

		if route.Timeout < 0 {
			return fmt.Errorf("route[%d]: timeout must be non-negative", i)
		}

		if route.CircuitBreaker == nil {
			route.CircuitBreaker = &CircuitBreakerConfig{}
		}
		route.CircuitBreaker.Defaults()

		if route.RateLimit != nil {
			if route.RateLimit.RequestsPerMinute < 0 {
				return fmt.Errorf("route[%d]: rate_limit.requests_per_minute must be non-negative", i)
			}
			if route.RateLimit.Burst < 0 {
				return fmt.Errorf("route[%d]: rate_limit.burst must be non-negative", i)
			}
			route.RateLimit.Defaults()
		}
	}
	return nil
}

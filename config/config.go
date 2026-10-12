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

type Route struct {
	Path        string       `yaml:"path"`
	StripPrefix string       `yaml:"strip_prefix"`
	Rewrite     *RewriteRule `yaml:"rewrite"`
	Algorithm   string       `yaml:"algorithm,omitempty"`
	CookieName  string       `yaml:"cookie_name,omitempty"`
	Backends    []Backend    `yaml:"backends"`
}

// HealthCheck holds configuration for backend health probing.
type HealthCheck struct {
	// Path is the HTTP path to probe on each backend (default: "/health").
	Path string `yaml:"path"`
	// Interval is how often to probe each backend (default: 10s).
	Interval time.Duration `yaml:"interval"`
	// Timeout is the maximum time to wait for a probe response (default: 5s).
	Timeout time.Duration `yaml:"timeout"`
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
	Port        int         `yaml:"port"`
	Routes      []Route     `yaml:"routes"`
	HealthCheck HealthCheck `yaml:"health_check"`
}

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
	}
	return nil
}

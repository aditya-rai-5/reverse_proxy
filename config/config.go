package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Backend struct {
	URL string `yaml:"url"`
}

type Route struct {
	Path     string    `yaml:"path"`
	Backends []Backend `yaml:"backends"`
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
	for i, route := range cfg.Routes {
		if route.Path == "" {
			return fmt.Errorf("route[%d]: path must not be empty", i)
		}
		if len(route.Backends) == 0 {
			return fmt.Errorf("route[%d]: at least one backend must be defined", i)
		}
		for j, backend := range route.Backends {
			if backend.URL == "" {
				return fmt.Errorf("route[%d].backend[%d]: url must not be empty", i, j)
			}
		}
	}
	return nil
}

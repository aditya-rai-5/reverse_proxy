package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Backend struct {
	URL string `yaml:"url"`
}

type Route struct {
	Path     string    `yaml:"path"`
	Backends []Backend `yaml:"backends"`
}

type Config struct {
	Port   int     `yaml:"port"`
	Routes []Route `yaml:"routes"`
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

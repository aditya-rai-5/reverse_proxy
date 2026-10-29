package config

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aditya-rai-5/reverse_proxy/clientip"

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
	Retries        int                   `yaml:"retries"`
	RetryMethods   []string              `yaml:"retry_methods"`
	Timeout        time.Duration         `yaml:"timeout,omitempty"`
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker,omitempty"`
	RateLimit      *RateLimit            `yaml:"rate_limit,omitempty"`
	Backends       []Backend             `yaml:"backends"`
}

// UnmarshalYAML resolves the retry default at the YAML boundary, where an
// omitted setting can still be distinguished from an explicit zero. Keep
// Retries as an int so programmatic configs and routing use a resolved budget.
func (r *Route) UnmarshalYAML(node *yaml.Node) error {
	type plainRoute Route
	var decoded plainRoute
	if err := checkFields(node, reflect.TypeOf(decoded), make(map[*yaml.Node]bool)); err != nil {
		return err
	}
	if err := node.Decode(&decoded); err != nil {
		return err
	}

	var settings struct {
		Retries *int `yaml:"retries"`
	}
	if err := node.Decode(&settings); err != nil {
		return err
	}
	if settings.Retries == nil && len(decoded.Backends) > 1 {
		decoded.Retries = min(len(decoded.Backends)-1, 2)
	}
	*r = Route(decoded)
	return nil
}

// NormalizeRetryMethods supplies a conservative default when omitted. An
// explicitly empty list disables retries; write methods require an idempotency
// key at request time as well as inclusion in this list.
func NormalizeRetryMethods(methods []string) ([]string, error) {
	if methods == nil {
		return []string{"GET", "HEAD", "OPTIONS"}, nil
	}
	normalized := make([]string, 0, len(methods))
	seen := make(map[string]bool)
	for _, method := range methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		switch method {
		case "GET", "HEAD", "OPTIONS", "PUT", "DELETE", "POST", "PATCH":
		default:
			return nil, fmt.Errorf("unsupported retry method %q", method)
		}
		if !seen[method] {
			normalized = append(normalized, method)
			seen[method] = true
		}
	}
	return normalized, nil
}

// HealthCheck holds configuration for backend health probing.
type HealthCheck struct {
	Path     string        `yaml:"path"`
	Interval time.Duration `yaml:"interval"`
	Timeout  time.Duration `yaml:"timeout"`
}

// TLS configures HTTPS termination. Choose either a certificate/key pair or
// automatic certificate management with Let's Encrypt.
type TLS struct {
	CertFile  string   `yaml:"cert_file,omitempty"`
	KeyFile   string   `yaml:"key_file,omitempty"`
	AutoCert  bool     `yaml:"auto_cert,omitempty"`
	Domains   []string `yaml:"domains,omitempty"`
	Email     string   `yaml:"email,omitempty"`
	CacheDir  string   `yaml:"cache_dir,omitempty"`
	HTTPSPort int      `yaml:"https_port,omitempty"`
	HTTPPort  int      `yaml:"http_port,omitempty"`
}

func (t *TLS) defaults() {
	if t.HTTPSPort == 0 {
		t.HTTPSPort = 443
	}
	if t.HTTPPort == 0 {
		t.HTTPPort = 80
	}
	if t.AutoCert && t.CacheDir == "" {
		t.CacheDir = ".cache/autocert"
	}
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
	Management          Management  `yaml:"management"`
	TrustedProxies      []string    `yaml:"trusted_proxies"`
	Port                int         `yaml:"port"`
	MaxRequestBodyBytes int64       `yaml:"max_request_body_bytes"`
	TLS                 *TLS        `yaml:"tls,omitempty"`
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
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("config must contain exactly one YAML document")
	}

	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if _, err := clientip.NewResolver(cfg.TrustedProxies); err != nil {
		return err
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if cfg.MaxRequestBodyBytes < 0 {
		return fmt.Errorf("max_request_body_bytes must be non-negative")
	}
	if cfg.MaxRequestBodyBytes == 0 {
		cfg.MaxRequestBodyBytes = DefaultMaxRequestBodyBytes
	}
	if cfg.TLS != nil {
		cfg.TLS.defaults()
		if cfg.TLS.HTTPSPort < 1 || cfg.TLS.HTTPSPort > 65535 {
			return fmt.Errorf("tls.https_port must be between 1 and 65535")
		}
		if cfg.TLS.HTTPPort < 1 || cfg.TLS.HTTPPort > 65535 {
			return fmt.Errorf("tls.http_port must be between 1 and 65535")
		}
		if cfg.TLS.HTTPPort == cfg.TLS.HTTPSPort {
			return fmt.Errorf("tls.http_port and tls.https_port must be different")
		}
		if cfg.TLS.AutoCert {
			if cfg.TLS.CertFile != "" || cfg.TLS.KeyFile != "" {
				return fmt.Errorf("tls: cert_file/key_file cannot be combined with auto_cert")
			}
			if len(cfg.TLS.Domains) == 0 {
				return fmt.Errorf("tls.domains must contain at least one domain when auto_cert is enabled")
			}
			for i, domain := range cfg.TLS.Domains {
				if strings.TrimSpace(domain) != domain || domain == "" ||
					strings.ContainsAny(domain, " \t\r\n/:*") || !strings.Contains(domain, ".") {
					return fmt.Errorf("tls.domains[%d] must be a hostname without a scheme, port, or wildcard", i)
				}
			}
		} else if cfg.TLS.CertFile == "" || cfg.TLS.KeyFile == "" {
			return fmt.Errorf("tls.cert_file and tls.key_file are both required unless auto_cert is enabled")
		}
	}
	if err := cfg.Management.validate(cfg); err != nil {
		return err
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
	if cfg.HealthCheck.Interval < 0 || cfg.HealthCheck.Timeout < 0 {
		return fmt.Errorf("health_check durations must be non-negative")
	}
	cfg.HealthCheck.defaults()
	if err := validatePath(cfg.HealthCheck.Path); err != nil {
		return fmt.Errorf("health_check.path: %w", err)
	}
	if len(cfg.Routes) == 0 {
		return fmt.Errorf("at least one route must be defined")
	}
	seenPaths := make(map[string]bool)
	for i := range cfg.Routes {
		route := &cfg.Routes[i]
		if err := validatePath(route.Path); err != nil {
			return fmt.Errorf("route[%d].path: %w", i, err)
		}
		if seenPaths[route.Path] {
			return fmt.Errorf("route[%d]: duplicate path %q", i, route.Path)
		}
		seenPaths[route.Path] = true
		if route.StripPrefix != "" {
			if err := validatePath(route.StripPrefix); err != nil {
				return fmt.Errorf("route[%d].strip_prefix: %w", i, err)
			}
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
		seenBackends := make(map[string]int, len(route.Backends))
		for j := range route.Backends {
			backend := &route.Backends[j]
			if err := validateBackend(backend.URL); err != nil {
				return fmt.Errorf("route[%d].backend[%d]: %w", i, j, err)
			}
			u, _ := url.Parse(backend.URL) // Already validated above.
			if first, exists := seenBackends[u.String()]; exists {
				return fmt.Errorf("route[%d].backend[%d]: duplicate backend URL %q (already configured at backend[%d]); use weight for weighted distribution", i, j, backend.URL, first)
			}
			seenBackends[u.String()] = j
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

		if route.Algorithm == AlgorithmSticky {
			if route.CookieName == "" {
				route.CookieName = "SERVERID"
			}
			if err := (&http.Cookie{Name: route.CookieName}).Valid(); err != nil {
				return fmt.Errorf("route[%d].cookie_name %q: %w", i, route.CookieName, err)
			}
		}

		if route.Retries < 0 {
			return fmt.Errorf("route[%d]: retries must be non-negative", i)
		}
		route.RetryMethods, err = NormalizeRetryMethods(route.RetryMethods)
		if err != nil {
			return fmt.Errorf("route[%d]: %w", i, err)
		}
		if route.Timeout < 0 {
			return fmt.Errorf("route[%d]: timeout must be non-negative", i)
		}

		if route.CircuitBreaker == nil {
			route.CircuitBreaker = &CircuitBreakerConfig{}
		}
		if route.CircuitBreaker.FailureThreshold < 0 || route.CircuitBreaker.Cooldown < 0 {
			return fmt.Errorf("route[%d]: circuit_breaker values must be non-negative", i)
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

// validatePath checks literal, decoded paths used by the router and probes.
func validatePath(path string) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "?#\\%") || strings.IndexFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("must be an absolute path without query, fragment, escapes, whitespace, or controls")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("path must not contain dot segments")
		}
	}
	if strings.Contains(path, "//") {
		return fmt.Errorf("path must not contain repeated slashes")
	}
	return nil
}

func validateBackend(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid backend URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.Opaque != "" ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return fmt.Errorf("backend must be an absolute HTTP(S) origin URL without credentials, base path, query, or fragment")
	}
	host := u.Hostname()
	if strings.Contains(u.Host, "[") {
		if net.ParseIP(host) == nil {
			return fmt.Errorf("invalid backend IP address")
		}
	} else {
		if strings.Contains(host, ":") {
			return fmt.Errorf("IPv6 backend addresses must be bracketed")
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return fmt.Errorf("invalid backend hostname")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return fmt.Errorf("invalid backend hostname")
				}
			}
		}
		if len(host) > 253 {
			return fmt.Errorf("backend hostname too long")
		}
	}
	if strings.HasSuffix(u.Host, ":") {
		return fmt.Errorf("backend port must not be empty")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("backend port must be between 1 and 65535")
		}
	}
	return nil
}

// checkFields preserves YAML aliases and merges while enforcing the schema
// inside Route.UnmarshalYAML, where Decoder.KnownFields does not propagate.
func checkFields(node *yaml.Node, typ reflect.Type, visiting map[*yaml.Node]bool) error {
	if visiting[node] {
		return fmt.Errorf("cyclic YAML alias")
	}
	visiting[node] = true
	defer delete(visiting, node)
	if node.Kind == yaml.AliasNode {
		return checkFields(node.Alias, typ, visiting)
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if node.Kind == yaml.SequenceNode && typ.Kind() == reflect.Slice {
		for _, item := range node.Content {
			if err := checkFields(item, typ.Elem(), visiting); err != nil {
				return err
			}
		}
	}
	if node.Kind != yaml.MappingNode || typ.Kind() != reflect.Struct {
		return nil
	}
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("yaml"), ",")[0]
		fields[name] = field.Type
	}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Tag == "!!merge" {
			if value.Kind == yaml.SequenceNode {
				for _, item := range value.Content {
					if err := checkFields(item, typ, visiting); err != nil {
						return err
					}
				}
			} else if err := checkFields(value, typ, visiting); err != nil {
				return err
			}
			continue
		}
		field, ok := fields[key.Value]
		if !ok {
			return fmt.Errorf("line %d: unknown field %q", key.Line, key.Value)
		}
		if err := checkFields(value, field, visiting); err != nil {
			return err
		}
	}
	return nil
}

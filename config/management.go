package config

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// Management configures the separate HTTP listener and its direct-peer allowlist.
type Management struct {
	Address      string   `yaml:"address"`
	AllowedCIDRs []string `yaml:"allowed_cidrs"`
}

func (m *Management) validate(cfg *Config) error {
	if m.Address == "" {
		m.Address = "127.0.0.1:9091"
	}
	host, port, err := net.SplitHostPort(m.Address)
	if err != nil {
		return fmt.Errorf("management.address must be an IP:port: %w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" {
		return fmt.Errorf("management.address must use a literal IP without a zone")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("management.address port must be between 1 and 65535")
	}
	if (cfg.TLS == nil && p == cfg.Port) || (cfg.TLS != nil && (p == cfg.TLS.HTTPPort || p == cfg.TLS.HTTPSPort)) {
		return fmt.Errorf("management.address must use a separate listener port")
	}
	if len(m.AllowedCIDRs) == 0 {
		if !ip.IsLoopback() {
			return fmt.Errorf("management.allowed_cidrs is required for a non-loopback listener")
		}
		m.AllowedCIDRs = []string{"127.0.0.0/8", "::1/128"}
	}
	for _, raw := range m.AllowedCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Addr().Is4In6() {
			return fmt.Errorf("invalid management.allowed_cidrs entry %q", raw)
		}
	}
	return nil
}

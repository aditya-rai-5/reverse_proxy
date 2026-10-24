// Package clientip resolves client addresses using an explicit proxy trust policy.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver is immutable and safe for concurrent use. Its zero value trusts no proxies.
type Resolver struct {
	trusted []netip.Prefix
}

// NewResolver validates the CIDRs of proxies allowed to supply X-Forwarded-For.
func NewResolver(cidrs []string) (*Resolver, error) {
	var trusted []netip.Prefix
	for i, cidr := range cidrs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies[%d]: invalid CIDR %q: %w", i, cidr, err)
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("trusted_proxies[%d]: mapped IPv4 CIDR must have at least 96 prefix bits", i)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		trusted = append(trusted, prefix.Masked())
	}
	return &Resolver{trusted: trusted}, nil
}

// Resolve returns a canonical client IP without a port. It returns an empty
// string for a missing or invalid peer, giving such requests one shared identity.
// Forwarding headers are ignored unless the immediate peer is trusted.
func (r *Resolver) Resolve(req *http.Request) string {
	if req == nil {
		return ""
	}
	host := req.RemoteAddr
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	peer = peer.WithZone("").Unmap()
	if !r.isTrusted(peer) {
		return peer.String()
	}

	// Validate all header lines before resolving the chain. Malformed values
	// cannot be skipped, because that would change the trust boundary.
	var chain []netip.Addr
	for _, line := range req.Header.Values("X-Forwarded-For") {
		for _, value := range strings.Split(line, ",") {
			addr, err := netip.ParseAddr(strings.TrimSpace(value))
			if err != nil || addr.Zone() != "" {
				return peer.String()
			}
			chain = append(chain, addr.Unmap())
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !r.isTrusted(chain[i]) {
			return chain[i].String()
		}
	}
	return peer.String()
}

func (r *Resolver) isTrusted(addr netip.Addr) bool {
	if r != nil {
		for _, prefix := range r.trusted {
			if prefix.Contains(addr) {
				return true
			}
		}
	}
	return false
}

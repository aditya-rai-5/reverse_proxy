package clientip

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	trusted, err := NewResolver([]string{"10.0.0.0/24", "2001:db8:1::/64", "::ffff:192.0.2.0/120"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		resolver *Resolver
		peer     string
		xff      []string
		want     string
	}{
		{"default ignores spoof", nil, "198.51.100.25:1234", []string{"1.2.3.4"}, "198.51.100.25"},
		{"zero value ignores spoof", &Resolver{}, "198.51.100.25:1234", []string{"1.2.3.4"}, "198.51.100.25"},
		{"untrusted peer", trusted, "198.51.100.25:1234", []string{"1.2.3.4, 10.0.0.2"}, "198.51.100.25"},
		{"trusted single", trusted, "10.0.0.5:1234", []string{"198.51.100.25"}, "198.51.100.25"},
		{"spoofed leftmost", trusted, "10.0.0.5:1234", []string{"1.2.3.4, 198.51.100.25"}, "198.51.100.25"},
		{"trusted hops", trusted, "10.0.0.5:1234", []string{"1.2.3.4, 198.51.100.25, 10.0.0.2"}, "198.51.100.25"},
		{"multiple lines", trusted, "10.0.0.5:1234", []string{"1.2.3.4", "198.51.100.25, 10.0.0.2"}, "198.51.100.25"},
		{"malformed left", trusted, "10.0.0.5:1234", []string{"bad, 198.51.100.25"}, "10.0.0.5"},
		{"malformed right", trusted, "10.0.0.5:1234", []string{"198.51.100.25, bad"}, "10.0.0.5"},
		{"empty element", trusted, "10.0.0.5:1234", []string{"198.51.100.25,,10.0.0.2"}, "10.0.0.5"},
		{"empty header", trusted, "10.0.0.5:1234", []string{""}, "10.0.0.5"},
		{"no header ignores real IP", trusted, "10.0.0.5:1234", nil, "10.0.0.5"},
		{"all trusted", trusted, "10.0.0.5:1234", []string{"10.0.0.2"}, "10.0.0.5"},
		{"IPv6 peer", nil, "[2001:db8::1]:1234", nil, "2001:db8::1"},
		{"IPv6 chain", trusted, "[2001:db8:1::1]:1234", []string{"2001:0db8:2::1, 2001:db8:1::2"}, "2001:db8:2::1"},
		{"plain peer", nil, "198.51.100.25", nil, "198.51.100.25"},
		{"mapped peer", trusted, "[::ffff:10.0.0.5]:1234", []string{"::ffff:198.51.100.25"}, "198.51.100.25"},
		{"mapped CIDR", trusted, "192.0.2.1:1234", []string{"198.51.100.25"}, "198.51.100.25"},
		{"zone peer", nil, "[fe80::1%eth0]:1234", nil, "fe80::1"},
		{"zone header rejected", trusted, "10.0.0.5:1234", []string{"fe80::1%eth0"}, "10.0.0.5"},
		{"port in header rejected", trusted, "10.0.0.5:1234", []string{"198.51.100.25:80"}, "10.0.0.5"},
		{"invalid peer", trusted, "bad", []string{"198.51.100.25"}, ""},
		{"missing peer", trusted, "", []string{"198.51.100.25"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tt.peer
			for _, value := range tt.xff {
				req.Header.Add("X-Forwarded-For", value)
			}
			req.Header.Set("X-Real-IP", "203.0.113.1")
			req.Header.Set("Forwarded", "for=203.0.113.1")
			if got := tt.resolver.Resolve(req); got != tt.want {
				t.Fatalf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := trusted.Resolve(nil); got != "" {
		t.Fatalf("nil request = %q", got)
	}
}

func TestNewResolver(t *testing.T) {
	for _, cidr := range []string{"bad", "10.0.0.1", "10.0.0.0/33", "2001:db8::/129", "", "::ffff:192.0.2.1/80"} {
		t.Run(cidr, func(t *testing.T) {
			_, err := NewResolver([]string{"10.0.0.0/24", cidr})
			if err == nil || !strings.Contains(err.Error(), "trusted_proxies[1]") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

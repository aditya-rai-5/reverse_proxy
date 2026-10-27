package config

import "testing"

func TestManagementValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		management Management
		invalid    bool
	}{
		{"defaults", Management{}, false},
		{"ipv6", Management{Address: "[::1]:9091"}, false},
		{"private listener", Management{Address: "0.0.0.0:9091", AllowedCIDRs: []string{"10.0.0.0/8"}}, false},
		{"missing allowlist", Management{Address: "0.0.0.0:9091"}, true},
		{"invalid CIDR", Management{AllowedCIDRs: []string{"127.0.0.1"}}, true},
		{"hostname", Management{Address: "localhost:9091"}, true},
		{"invalid port", Management{Address: "127.0.0.1:65536"}, true},
		{"conflicting port", Management{Address: "127.0.0.1:8080"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Port: 8080, Management: tc.management, Routes: []Route{{Path: "/", Backends: []Backend{{URL: "http://localhost:9000"}}}}}
			if err := validate(cfg); (err != nil) != tc.invalid {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

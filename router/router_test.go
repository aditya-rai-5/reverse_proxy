package router

import (
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestMatchesPath(t *testing.T) {
	tests := []struct {
		requestPath string
		routePath   string
		expected    bool
	}{
		// Exact matches
		{"/", "/", true},
		{"/api", "/api", true},
		{"/api/", "/api/", true},

		// Valid sub-paths (boundary check passes)
		{"/api/users", "/api", true},
		{"/api/v1/status", "/api", true},
		{"/users/123", "/", true},

		// Invalid sub-paths (boundary check fails)
		{"/apikeys", "/api", false},
		{"/apiv2", "/api", false},

		// Completely different paths
		{"/test", "/api", false},
		{"/api", "/test", false},
	}

	for _, tt := range tests {
		t.Run(tt.requestPath+"_vs_"+tt.routePath, func(t *testing.T) {
			result := matchesPath(tt.requestPath, tt.routePath)
			if result != tt.expected {
				t.Errorf("matchesPath(%q, %q) = %v, expected %v", tt.requestPath, tt.routePath, result, tt.expected)
			}
		})
	}
}

func TestRouterMatchPriority(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{Path: "/", Backends: []config.Backend{{URL: "http://root"}}},
			{Path: "/api/v2", Backends: []config.Backend{{URL: "http://v2"}}},
			{Path: "/api", Backends: []config.Backend{{URL: "http://api"}}},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	if r.routes[0].path != "/api/v2" {
		t.Errorf("Expected longest path first, got %q", r.routes[0].path)
	}

	tests := []struct {
		requestPath   string
		expectedURL   string
		expectedError bool
	}{
		{"/api/v2/users", "http://v2", false},
		{"/api/v2", "http://v2", false},
		{"/api/users", "http://api", false},
		{"/api", "http://api", false},
		{"/apikeys", "http://root", false},
		{"/test", "http://root", false},
		{"/", "http://root", false},
	}

	for _, tt := range tests {
		t.Run(tt.requestPath, func(t *testing.T) {
			lb, err := r.Match(tt.requestPath)

			if tt.expectedError {
				if err == nil {
					t.Errorf("Expected error for %q, got nil", tt.requestPath)
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error for %q: %v", tt.requestPath, err)
				return
			}

			target := lb.Next()
			if target.String() != tt.expectedURL {
				t.Errorf("Match(%q) routed to %q, expected %q", tt.requestPath, target.String(), tt.expectedURL)
			}
		})
	}
}

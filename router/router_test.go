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

	entries := r.Entries()
	if len(entries) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(entries))
	}
	if entries[0].Path != "/api/v2" {
		t.Errorf("Expected longest path first, got %q", entries[0].Path)
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

			target, err := lb.Next()
			if err != nil {
				t.Fatalf("Unexpected error from Next(): %v", err)
			}
			if target.String() != tt.expectedURL {
				t.Errorf("Match(%q) routed to %q, expected %q", tt.requestPath, target.String(), tt.expectedURL)
			}
		})
	}
}

func TestRouterEntries(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{Path: "/web", Backends: []config.Backend{{URL: "http://web1"}, {URL: "http://web2"}}},
			{Path: "/api", Backends: []config.Backend{{URL: "http://api1"}}},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	entries := r.Entries()
	if len(entries) != 2 {
		t.Fatalf("Expected 2 entries, got %d", len(entries))
	}

	for _, entry := range entries {
		if entry.LB == nil {
			t.Errorf("Expected non-nil LB for path %s", entry.Path)
		}
	}
}

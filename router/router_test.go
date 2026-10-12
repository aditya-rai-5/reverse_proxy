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
			entry, err := r.Match(tt.requestPath)

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

			target, err := entry.Next()
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

func TestRewritePath_StripPrefix(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{
				Path:        "/api",
				StripPrefix: "/api",
				Backends:    []config.Backend{{URL: "http://backend"}},
			},
			{
				Path:        "/v1/",
				StripPrefix: "/v1/",
				Backends:    []config.Backend{{URL: "http://backend"}},
			},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	tests := []struct {
		routePath   string
		inputPath   string
		expectedPath string
	}{
		{"/api", "/api/users", "/users"},
		{"/api", "/api/users/123", "/users/123"},
		{"/api", "/api", "/"},
		{"/api", "/api/", "/"},
		{"/v1/", "/v1/orders", "/orders"},
		{"/v1/", "/v1/orders/create", "/orders/create"},
	}

	for _, tt := range tests {
		entry, err := r.Match(tt.inputPath)
		if err != nil {
			t.Fatalf("Match(%q) unexpected error: %v", tt.inputPath, err)
		}
		rewritten := entry.RewritePath(tt.inputPath)
		if rewritten != tt.expectedPath {
			t.Errorf("RewritePath(%q) = %q, expected %q", tt.inputPath, rewritten, tt.expectedPath)
		}
	}
}

func TestRewritePath_RegexRewrite(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{
				Path: "/legacy",
				Rewrite: &config.RewriteRule{
					Pattern:     "^/legacy/(.*)$",
					Replacement: "/v2/$1",
				},
				Backends: []config.Backend{{URL: "http://backend"}},
			},
			{
				Path: "/users",
				Rewrite: &config.RewriteRule{
					Pattern:     "^/users/([0-9]+)$",
					Replacement: "/api/user/$1",
				},
				Backends: []config.Backend{{URL: "http://backend"}},
			},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	tests := []struct {
		inputPath    string
		expectedPath string
	}{
		{"/legacy/products", "/v2/products"},
		{"/legacy/categories/5", "/v2/categories/5"},
		{"/users/100", "/api/user/100"},
		{"/users/all", "/users/all"}, // Regex didn't match (non-digits), path preserved
	}

	for _, tt := range tests {
		entry, err := r.Match(tt.inputPath)
		if err != nil {
			t.Fatalf("Match(%q) unexpected error: %v", tt.inputPath, err)
		}
		rewritten := entry.RewritePath(tt.inputPath)
		if rewritten != tt.expectedPath {
			t.Errorf("RewritePath(%q) = %q, expected %q", tt.inputPath, rewritten, tt.expectedPath)
		}
	}
}

func TestRewritePath_StripPrefixAndRewrite(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{
				Path:        "/gateway",
				StripPrefix: "/gateway",
				Rewrite: &config.RewriteRule{
					Pattern:     "^/v1/(.*)$",
					Replacement: "/v2/$1",
				},
				Backends: []config.Backend{{URL: "http://backend"}},
			},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	entry, err := r.Match("/gateway/v1/customers")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// First /gateway stripped -> /v1/customers
	// Then /v1/customers rewritten -> /v2/customers
	rewritten := entry.RewritePath("/gateway/v1/customers")
	if rewritten != "/v2/customers" {
		t.Errorf("Expected '/v2/customers', got %q", rewritten)
	}
}

func TestRewritePath_NoRules(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{
				Path:     "/api",
				Backends: []config.Backend{{URL: "http://backend"}},
			},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	entry, err := r.Match("/api/status")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	rewritten := entry.RewritePath("/api/status")
	if rewritten != "/api/status" {
		t.Errorf("Expected path to remain unchanged '/api/status', got %q", rewritten)
	}
}

func TestRouterAlgorithmConfiguration(t *testing.T) {
	cfg := &config.Config{
		Routes: []config.Route{
			{
				Path:       "/sticky",
				Algorithm:  "sticky",
				CookieName: "MY_COOKIE",
				Backends:   []config.Backend{{URL: "http://srv1"}, {URL: "http://srv2"}},
			},
			{
				Path:      "/weighted",
				Algorithm: "weighted_round_robin",
				Backends:  []config.Backend{{URL: "http://heavy", Weight: 3}, {URL: "http://light", Weight: 1}},
			},
		},
	}

	r, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create router: %v", err)
	}

	stickyEntry, err := r.Match("/sticky/test")
	if err != nil {
		t.Fatalf("Failed to match /sticky: %v", err)
	}
	if stickyEntry.LB.Algorithm() != config.AlgorithmSticky {
		t.Errorf("Expected algorithm %q, got %q", config.AlgorithmSticky, stickyEntry.LB.Algorithm())
	}

	weightedEntry, err := r.Match("/weighted/test")
	if err != nil {
		t.Fatalf("Failed to match /weighted: %v", err)
	}
	if weightedEntry.LB.Algorithm() != config.AlgorithmWeightedRoundRobin {
		t.Errorf("Expected algorithm %q, got %q", config.AlgorithmWeightedRoundRobin, weightedEntry.LB.Algorithm())
	}
	if weightedEntry.LB.Weight(0) != 3 || weightedEntry.LB.Weight(1) != 1 {
		t.Errorf("Expected weights 3 and 1, got %d and %d", weightedEntry.LB.Weight(0), weightedEntry.LB.Weight(1))
	}

	// Verify entry.Select works
	res, err := weightedEntry.Select(nil)
	if err != nil {
		t.Fatalf("weightedEntry.Select failed: %v", err)
	}
	if res.URL == nil {
		t.Fatal("Expected non-nil URL from Select")
	}
}

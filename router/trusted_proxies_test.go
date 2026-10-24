package router

import (
	"strings"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestRouterRejectsInvalidTrustedProxies(t *testing.T) {
	_, err := New(&config.Config{TrustedProxies: []string{"bad"}})
	if err == nil || !strings.Contains(err.Error(), "trusted_proxies[0]") {
		t.Fatalf("unexpected error: %v", err)
	}
}

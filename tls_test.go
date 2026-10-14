package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestHTTPSRedirectHandlerPreservesMethodPathAndQuery(t *testing.T) {
	handler := httpsRedirectHandler(8443)
	req := httptest.NewRequest(http.MethodPost, "http://proxy.example.com/upload?part=2", nil)
	req.Host = "proxy.example.com:80"
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected status %d, got %d", http.StatusPermanentRedirect, rec.Code)
	}
	if got, want := rec.Header().Get("Location"), "https://proxy.example.com:8443/upload?part=2"; got != want {
		t.Fatalf("expected redirect location %q, got %q", want, got)
	}
}

func TestHTTPSRedirectHandlerUsesDefaultPortAndIPv6(t *testing.T) {
	tests := []struct {
		name string
		host string
		want string
	}{
		{name: "default port", host: "proxy.example.com:80", want: "https://proxy.example.com/"},
		{name: "IPv6", host: "[2001:db8::1]:80", want: "https://[2001:db8::1]/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			httpsRedirectHandler(443).ServeHTTP(rec, req)
			if got := rec.Header().Get("Location"); got != tt.want {
				t.Fatalf("expected location %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNewServersWithManualCertificate(t *testing.T) {
	certFile, keyFile := writeTestCertificate(t)
	cfg := &config.Config{
		Port: 8080,
		TLS: &config.TLS{
			CertFile:  certFile,
			KeyFile:   keyFile,
			HTTPPort:  8081,
			HTTPSPort: 8443,
		},
	}
	servers, err := newServers(cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("newServers returned error: %v", err)
	}
	if servers.app.Addr != ":8443" || servers.redirect.Addr != ":8081" {
		t.Fatalf("unexpected listener addresses: HTTPS=%q HTTP=%q", servers.app.Addr, servers.redirect.Addr)
	}
	if servers.app.TLSConfig == nil || len(servers.app.TLSConfig.Certificates) != 1 {
		t.Fatal("manual certificate was not installed on the HTTPS server")
	}
	if servers.certManager != nil {
		t.Fatal("manual TLS mode should not create an autocert manager")
	}
}

func TestNewServersWithoutTLSKeepsHTTPMode(t *testing.T) {
	cfg := &config.Config{Port: 8080}
	servers, err := newServers(cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("newServers returned error: %v", err)
	}
	if servers.app.Addr != ":8080" || servers.app.TLSConfig != nil {
		t.Fatalf("unexpected HTTP server configuration: %+v", servers.app)
	}
	if servers.redirect != nil {
		t.Fatal("plain HTTP mode should not create a redirect listener")
	}
}

func TestNewServersWithAutocertRedirectAndChallengeHandler(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		TLS: &config.TLS{
			AutoCert:  true,
			Domains:   []string{"proxy.example.com"},
			CacheDir:  t.TempDir(),
			HTTPPort:  8081,
			HTTPSPort: 8443,
		},
	}
	servers, err := newServers(cfg, http.NotFoundHandler())
	if err != nil {
		t.Fatalf("newServers returned error: %v", err)
	}
	if servers.certManager == nil || servers.app.TLSConfig.GetCertificate == nil {
		t.Fatal("autocert manager is not configured for HTTPS")
	}

	redirectReq := httptest.NewRequest(http.MethodGet, "http://proxy.example.com:8081/healthz?full=1", nil)
	redirectReq.Host = "proxy.example.com:8081"
	redirectRec := httptest.NewRecorder()
	servers.redirect.Handler.ServeHTTP(redirectRec, redirectReq)
	if redirectRec.Code != http.StatusPermanentRedirect {
		t.Fatalf("expected normal HTTP traffic to redirect, got status %d", redirectRec.Code)
	}
	if got, want := redirectRec.Header().Get("Location"), "https://proxy.example.com:8443/healthz?full=1"; got != want {
		t.Fatalf("expected redirect location %q, got %q", want, got)
	}

	challengeReq := httptest.NewRequest(http.MethodGet, "http://proxy.example.com/.well-known/acme-challenge/not-a-real-token", nil)
	challengeReq.Host = "proxy.example.com"
	challengeRec := httptest.NewRecorder()
	servers.redirect.Handler.ServeHTTP(challengeRec, challengeReq)
	if challengeRec.Code == http.StatusPermanentRedirect {
		t.Fatal("ACME challenge request must be handled before the HTTPS redirect")
	}
	if challengeRec.Code != http.StatusNotFound {
		t.Fatalf("expected unknown ACME challenge to return 404, got %d", challengeRec.Code)
	}
}

func TestNewServersRejectsInvalidManualCertificate(t *testing.T) {
	cfg := &config.Config{
		Port: 8080,
		TLS: &config.TLS{
			CertFile:  "missing.crt",
			KeyFile:   "missing.key",
			HTTPPort:  8081,
			HTTPSPort: 8443,
		},
	}
	if _, err := newServers(cfg, http.NotFoundHandler()); err == nil {
		t.Fatal("expected invalid certificate paths to fail server setup")
	}
}

func writeTestCertificate(t *testing.T) (string, string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "proxy.example.com"},
		DNSNames:     []string{"proxy.example.com"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatalf("write test certificate: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatalf("write test private key: %v", err)
	}
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		t.Fatalf("verify generated test key pair: %v", err)
	}
	return certFile, keyFile
}

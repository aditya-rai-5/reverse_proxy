package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func startListenerTestServer(t *testing.T, srv *http.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(srv.Handler)
	ts.Config = srv
	if srv.TLSConfig != nil {
		ts.TLS = srv.TLSConfig.Clone()
		ts.StartTLS()
	} else {
		ts.Start()
	}
	t.Cleanup(ts.Close)
	return ts
}

func dialListenerTestServer(t *testing.T, ts *httptest.Server) net.Conn {
	t.Helper()
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	var conn net.Conn
	var err error
	if ts.TLS != nil {
		// Only connect to this test's local server with its generated certificate.
		conn, err = tls.DialWithDialer(dialer, "tcp", ts.Listener.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	} else {
		conn, err = dialer.Dial("tcp", ts.Listener.Addr().String())
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestListenerRejectsSlowAndOversizedHeaders(t *testing.T) {
	cert, key := writeTestCertificate(t)
	for _, mode := range []string{"HTTP", "HTTPS", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &config.Config{Port: 8080, Server: config.Server{
				ReadHeaderTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond, MaxHeaderBytes: 1024,
			}}
			if mode != "HTTP" {
				cfg.TLS = &config.TLS{CertFile: cert, KeyFile: key, HTTPPort: 8081, HTTPSPort: 8443}
			}
			servers, err := newServers(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
			if err != nil {
				t.Fatal(err)
			}
			srv := servers.app
			if mode == "redirect" {
				srv = servers.redirect
			}
			ts := startListenerTestServer(t, srv)
			t.Run("slow headers", func(t *testing.T) {
				conn := dialListenerTestServer(t, ts)
				if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nX-Slow: "); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return // The server closed the incomplete request on its header deadline.
				}
				if err != nil {
					t.Fatalf("expected server rejection before client deadline: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest {
					t.Fatalf("slow headers returned %d", resp.StatusCode)
				}
			})
			t.Run("oversized headers", func(t *testing.T) {
				conn := dialListenerTestServer(t, ts)
				// net/http permits some read-ahead beyond MaxHeaderBytes.
				if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nX-Large: %s\r\n\r\n", strings.Repeat("x", 16<<10)); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
					t.Fatalf("oversized headers returned %d", resp.StatusCode)
				}
			})
			t.Run("idle connection", func(t *testing.T) {
				conn := dialListenerTestServer(t, ts)
				if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(conn)
				resp, err := http.ReadResponse(reader, nil)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.Close {
					t.Fatal("response unexpectedly disabled keep-alive")
				}
				if _, err := reader.ReadByte(); err != io.EOF {
					t.Fatalf("expected idle connection to close before client deadline: %v", err)
				}
			})
		})
	}
}

func TestListenerAllowsSlowUploadAndStreaming(t *testing.T) {
	cert, key := writeTestCertificate(t)
	for _, useTLS := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%v", useTLS), func(t *testing.T) {
			const headerTimeout = 100 * time.Millisecond
			cfg := &config.Config{Port: 8080, Server: config.Server{
				ReadHeaderTimeout: headerTimeout, IdleTimeout: headerTimeout, MaxHeaderBytes: 1024,
			}}
			if useTLS {
				cfg.TLS = &config.TLS{CertFile: cert, KeyFile: key, HTTPPort: 8081, HTTPSPort: 8443}
			}
			uploadStarted := make(chan struct{})
			servers, err := newServers(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/upload" {
					close(uploadStarted)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					w.Write(body)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: first\n\n")
				w.(http.Flusher).Flush()
				time.Sleep(3 * headerTimeout)
				fmt.Fprint(w, "data: second\n\n")
			}))
			if err != nil {
				t.Fatal(err)
			}
			ts := startListenerTestServer(t, servers.app)
			t.Run("upload", func(t *testing.T) {
				conn := dialListenerTestServer(t, ts)
				if _, err := io.WriteString(conn, "POST /upload HTTP/1.1\r\nHost: localhost\r\nContent-Length: 7\r\n\r\n"); err != nil {
					t.Fatal(err)
				}
				select {
				case <-uploadStarted:
				case <-time.After(2 * time.Second):
					t.Fatal("upload handler did not start")
				}
				time.Sleep(3 * headerTimeout)
				if _, err := io.WriteString(conn, "payload"); err != nil {
					t.Fatal(err)
				}
				resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || string(body) != "payload" {
					t.Fatalf("upload: status=%d body=%q err=%v", resp.StatusCode, body, err)
				}
			})
			t.Run("stream", func(t *testing.T) {
				client := ts.Client()
				client.Timeout = 3 * time.Second
				if useTLS {
					client.Transport.(*http.Transport).TLSClientConfig.ServerName = "proxy.example.com"
				}
				resp, err := client.Get(ts.URL + "/events")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != http.StatusOK || string(body) != "data: first\n\ndata: second\n\n" {
					t.Fatalf("stream: status=%d body=%q err=%v", resp.StatusCode, body, err)
				}
			})
		})
	}
}

func TestListenerSettings(t *testing.T) {
	cert, key := writeTestCertificate(t)
	for _, tc := range []struct {
		name, settings string
		header, idle   time.Duration
		maxHeader      int
	}{
		{"defaults", "", 5 * time.Second, time.Minute, 1 << 20},
		{"custom", "server: {read_header_timeout: 2s, idle_timeout: 15s, max_header_bytes: 8192}\n", 2 * time.Second, 15 * time.Second, 8192},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("port: 8080\n"+tc.settings+"routes: [{path: /, backends: [{url: http://localhost:9000}]}]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, mode := range []struct {
				name string
				tls  *config.TLS
			}{
				{"HTTP", nil},
				{"HTTPS", &config.TLS{CertFile: cert, KeyFile: key, HTTPPort: 8081, HTTPSPort: 8443}},
				{"ACME", &config.TLS{AutoCert: true, Domains: []string{"proxy.example.com"}, CacheDir: t.TempDir(), HTTPPort: 8081, HTTPSPort: 8443}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					cfg.TLS = mode.tls
					servers, err := newServers(cfg, http.NotFoundHandler())
					if err != nil {
						t.Fatal(err)
					}
					for _, srv := range servers.all() {
						if srv.ReadHeaderTimeout != tc.header || srv.IdleTimeout != tc.idle || srv.MaxHeaderBytes != tc.maxHeader {
							t.Errorf("%s: header timeout=%v idle timeout=%v max headers=%d; want %v, %v, %d", srv.Addr, srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes, tc.header, tc.idle, tc.maxHeader)
						}
						if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
							t.Error("listener imposes a whole-body or response deadline")
						}
					}
				})
			}
		})
	}
}

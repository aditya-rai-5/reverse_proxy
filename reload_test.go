package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func writeRuntimeConfig(t *testing.T, path, backend string, port int) {
	t.Helper()
	data := fmt.Sprintf("port: %d\nhealth_check: {interval: 1h}\nroutes:\n  - path: /\n    backends:\n      - url: %s\n", port, backend)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigChecking(t *testing.T) {
	var calls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer backend.Close()
	path := filepath.Join(t.TempDir(), "selected.yaml")
	writeRuntimeConfig(t, path, backend.URL, 8080)
	var out bytes.Buffer
	if err := run([]string{"--config", path, "--check-config"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "valid") || calls.Load() != 0 {
		t.Fatalf("output=%s backend calls=%d", out.String(), calls.Load())
	}
	for _, args := range [][]string{{"--config", path + ".missing", "--check-config"}, {"--unknown"}, {"extra"}} {
		if err := run(args, &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if err := os.WriteFile(path, []byte("port: -1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--config", path, "--check-config"}, &out); err == nil {
		t.Fatal("accepted invalid config")
	}
	duplicate := fmt.Sprintf("port: 8080\nroutes: [{path: /, backends: [{url: %q}, {url: %q}]}]", backend.URL, backend.URL)
	if err := os.WriteFile(path, []byte(duplicate), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--config", path, "--check-config"}, &out); err == nil {
		t.Fatal("accepted duplicate backends")
	}
	if calls.Load() != 0 {
		t.Fatal("config checking contacted backend")
	}
}

func TestReloadAtomicAndRollback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			once.Do(func() { close(entered) })
			<-release
		}
		fmt.Fprint(w, "old")
	}))
	defer old.Close()
	next := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "new") }))
	defer next.Close()
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeRuntimeConfig(t, path, old.URL, 8080)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := newRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.close()
	// Always release the outstanding request, including on test failure.
	defer onceRelease(release)()
	done := make(chan string, 1)
	go func() {
		w := httptest.NewRecorder()
		runtime.ServeHTTP(w, httptest.NewRequest("GET", "/slow", nil))
		done <- w.Body.String()
	}()
	<-entered
	writeRuntimeConfig(t, path, next.URL, 8080)
	if err := runtime.reload(path); err != nil {
		t.Fatal(err)
	}
	request := func() string {
		w := httptest.NewRecorder()
		runtime.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		return w.Body.String()
	}
	if got := request(); got != "new" {
		t.Fatalf("after reload: %q", got)
	}
	close(release)
	if got := <-done; got != "old" {
		t.Fatalf("in-flight response: %q", got)
	}
	for _, invalid := range []string{"port: -1", fmt.Sprintf("port: 9090\nroutes: [{path: /, backends: [{url: %s}]}]", old.URL), fmt.Sprintf("port: 8080\nmanagement: {address: \"127.0.0.1:9092\"}\nroutes: [{path: /, backends: [{url: %s}]}]", old.URL), fmt.Sprintf("port: 8080\nroutes: [{path: /, backends: [{url: %q}, {url: %q}]}]", old.URL, old.URL)} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if err := runtime.reload(path); err == nil {
			t.Fatal("accepted invalid/restart-only reload")
		}
		if got := request(); got != "new" {
			t.Fatalf("rollback: %q", got)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				request()
			}
		}()
	}
	for i := 0; i < 5; i++ {
		writeRuntimeConfig(t, path, next.URL, 8080)
		if err := runtime.reload(path); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

func onceRelease(ch chan struct{}) func() {
	return func() {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func TestListenerFailureReturnsError(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	management, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	managementAddress := management.Addr().String()
	management.Close()
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := fmt.Sprintf("port: %d\nmanagement: {address: %q}\nroutes: [{path: /, backends: [{url: http://127.0.0.1:1}]}]", occupied.Addr().(*net.TCPAddr).Port, managementAddress)
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run([]string{"--config", path}, &output); err == nil || !strings.Contains(err.Error(), "listener") {
		t.Fatalf("expected listener failure, got %v", err)
	}
}

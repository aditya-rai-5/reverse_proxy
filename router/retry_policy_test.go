package router

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/aditya-rai-5/reverse_proxy/config"
)

func TestRouterRetryPolicy(t *testing.T) {
	for _, tt := range []struct {
		name    string
		methods []string
		method  string
		keys    []string
		want    bool
	}{
		{"default GET", nil, http.MethodGet, nil, true},
		{"default HEAD", nil, http.MethodHead, nil, true},
		{"default OPTIONS", nil, http.MethodOptions, nil, true},
		{"default POST even with key", nil, http.MethodPost, []string{"once"}, false},
		{"normalized write opt-in", []string{" post "}, http.MethodPost, []string{"once"}, true},
		{"write without key", []string{http.MethodPost}, http.MethodPost, nil, false},
		{"write empty key", []string{http.MethodPost}, http.MethodPost, []string{""}, false},
		{"write blank key", []string{http.MethodPost}, http.MethodPost, []string{" \t"}, false},
		{"ambiguous multiple keys", []string{http.MethodPost}, http.MethodPost, []string{"one", "two"}, false},
		{"empty allowlist", []string{}, http.MethodGet, nil, false},
		{"allowlist replaces default", []string{http.MethodPost}, http.MethodGet, nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := New(&config.Config{Routes: []config.Route{{Path: "/", RetryMethods: tt.methods, Backends: []config.Backend{{URL: "http://one.example"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := r.Match("/item")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(tt.method, "/item", nil)
			if tt.keys != nil {
				req.Header["Idempotency-Key"] = tt.keys
			}
			if got := entry.CanRetry(req); got != tt.want {
				t.Errorf("CanRetry=%v want=%v", got, tt.want)
			}
			if entry.CanRetry(nil) {
				t.Error("nil request can retry")
			}
		})
	}
}

func TestRouterRejectsInvalidProgrammaticRetryPolicy(t *testing.T) {
	_, err := New(&config.Config{Routes: []config.Route{{Path: "/", RetryMethods: []string{"INVALID"}, Backends: []config.Backend{{URL: "http://one.example"}}}}})
	if err == nil {
		t.Error("invalid programmatic retry policy accepted")
	}
}

func TestRouterCopiesRetryMethods(t *testing.T) {
	methods := []string{http.MethodPost}
	r, err := New(&config.Config{Routes: []config.Route{{Path: "/", RetryMethods: methods, Backends: []config.Backend{{URL: "http://one.example"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	methods[0] = http.MethodGet
	if !reflect.DeepEqual(r.Entries()[0].RetryMethods, []string{http.MethodPost}) {
		t.Error("caller mutation changed active policy")
	}
}

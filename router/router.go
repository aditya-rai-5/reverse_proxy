package router

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
)

type Entry struct {
	Path        string
	StripPrefix string
	LB          *loadbalancer.LoadBalancer
	rewriteRe   *regexp.Regexp
	replacement string
}

// Next selects the next healthy backend from the entry's load balancer.
func (e *Entry) Next() (*url.URL, error) {
	return e.LB.Next()
}

// RewritePath applies configured prefix stripping and regex rewriting to requestPath.
// The returned path is guaranteed to begin with '/'.
func (e *Entry) RewritePath(requestPath string) string {
	p := requestPath

	// 1. Strip prefix if configured
	if e.StripPrefix != "" && strings.HasPrefix(p, e.StripPrefix) {
		p = strings.TrimPrefix(p, e.StripPrefix)
		if p == "" || p[0] != '/' {
			p = "/" + p
		}
	}

	// 2. Regex rewrite if configured
	if e.rewriteRe != nil {
		p = e.rewriteRe.ReplaceAllString(p, e.replacement)
		if p == "" || p[0] != '/' {
			p = "/" + p
		}
	}

	return p
}

type Router struct {
	routes []Entry
}

func New(cfg *config.Config) (*Router, error) {
	entries := make([]Entry, 0, len(cfg.Routes))

	for _, route := range cfg.Routes {
		lb, err := loadbalancer.New(route.Backends)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.Path, err)
		}

		entry := Entry{
			Path:        route.Path,
			StripPrefix: route.StripPrefix,
			LB:          lb,
		}

		if route.Rewrite != nil {
			re, err := regexp.Compile(route.Rewrite.Pattern)
			if err != nil {
				return nil, fmt.Errorf("route %q: invalid rewrite pattern: %w", route.Path, err)
			}
			entry.rewriteRe = re
			entry.replacement = route.Rewrite.Replacement
		}

		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].Path) > len(entries[j].Path)
	})

	return &Router{routes: entries}, nil
}

func (r *Router) Match(requestPath string) (*Entry, error) {
	for i := range r.routes {
		if matchesPath(requestPath, r.routes[i].Path) {
			return &r.routes[i], nil
		}
	}
	return nil, fmt.Errorf("no route matched for path: %s", requestPath)
}

func (r *Router) Entries() []Entry {
	return r.routes
}

func matchesPath(requestPath, routePath string) bool {
	if !strings.HasPrefix(requestPath, routePath) {
		return false
	}
	if routePath == "/" || len(requestPath) == len(routePath) {
		return true
	}
	if strings.HasSuffix(routePath, "/") {
		return true
	}
	return requestPath[len(routePath)] == '/'
}

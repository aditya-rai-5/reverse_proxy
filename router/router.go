package router

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
)

type Entry struct {
	Path string
	LB   *loadbalancer.LoadBalancer
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
		entries = append(entries, Entry{Path: route.Path, LB: lb})
	}
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].Path) > len(entries[j].Path)
	})

	return &Router{routes: entries}, nil
}

func (r *Router) Match(requestPath string) (*loadbalancer.LoadBalancer, error) {
	for _, e := range r.routes {
		if matchesPath(requestPath, e.Path) {
			return e.LB, nil
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
	return requestPath[len(routePath)] == '/'
}

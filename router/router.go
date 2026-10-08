package router

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/loadbalancer"
)

type entry struct {
	path string
	lb   *loadbalancer.LoadBalancer
}
type Router struct {
	routes []entry
}

func New(cfg *config.Config) (*Router, error) {
	entries := make([]entry, 0, len(cfg.Routes))

	for _, route := range cfg.Routes {
		lb, err := loadbalancer.New(route.Backends)
		if err != nil {
			return nil, fmt.Errorf("route %q: %w", route.Path, err)
		}
		entries = append(entries, entry{path: route.Path, lb: lb})
	}
	sort.Slice(entries, func(i, j int) bool {
		return len(entries[i].path) > len(entries[j].path)
	})

	return &Router{routes: entries}, nil
}

func (r *Router) Match(requestPath string) (*loadbalancer.LoadBalancer, error) {
	for _, e := range r.routes {
		if matchesPath(requestPath, e.path) {
			return e.lb, nil
		}
	}
	return nil, fmt.Errorf("no route matched for path: %s", requestPath)
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

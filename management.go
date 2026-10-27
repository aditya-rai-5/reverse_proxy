package main

import (
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func newManagementServer(cfg config.Management, handler http.Handler) *http.Server {
	return &http.Server{Addr: cfg.Address, Handler: managementAccess(cfg, handler),
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
}

func managementAccess(cfg config.Management, next http.Handler) http.Handler {
	var allowed []netip.Prefix
	for _, raw := range cfg.AllowedCIDRs {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			allowed = append(allowed, prefix)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		if err == nil {
			if ip, err := netip.ParseAddr(host); err == nil {
				for _, prefix := range allowed {
					if prefix.Contains(ip.Unmap()) {
						next.ServeHTTP(w, req)
						return
					}
				}
			}
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func readinessHandler(r *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		for _, entry := range r.Entries() {
			if !entry.LB.Ready() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.Write([]byte("ready\n"))
	}
}

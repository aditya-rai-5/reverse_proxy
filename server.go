package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"golang.org/x/crypto/acme/autocert"
)

type serverPair struct {
	app         *http.Server
	redirect    *http.Server
	certManager *autocert.Manager
}

func (s *serverPair) all() []*http.Server {
	servers := []*http.Server{s.app}
	if s.redirect != nil {
		servers = append(servers, s.redirect)
	}
	return servers
}

func newServers(cfg *config.Config, handler http.Handler) (*serverPair, error) {
	newServer := func(port int, h http.Handler) *http.Server {
		return &http.Server{
			Addr:              fmt.Sprintf(":%d", port),
			Handler:           h,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
			MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
		}
	}
	if cfg.TLS == nil {
		return &serverPair{app: newServer(cfg.Port, handler)}, nil
	}

	tlsCfg := cfg.TLS
	serverTLS := &tls.Config{MinVersion: tls.VersionTLS12}
	var manager *autocert.Manager
	if tlsCfg.AutoCert {
		manager = &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache(tlsCfg.CacheDir),
			HostPolicy: autocert.HostWhitelist(tlsCfg.Domains...),
			Email:      tlsCfg.Email,
		}
		serverTLS = manager.TLSConfig()
		serverTLS.MinVersion = tls.VersionTLS12
	} else {
		cert, err := tls.LoadX509KeyPair(tlsCfg.CertFile, tlsCfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS certificate/key: %w", err)
		}
		serverTLS.Certificates = []tls.Certificate{cert}
	}

	redirectHandler := httpsRedirectHandler(tlsCfg.HTTPSPort)
	if manager != nil {
		// autocert's HTTP handler serves HTTP-01 challenges before delegating
		// normal requests to our HTTPS redirect handler.
		redirectHandler = manager.HTTPHandler(redirectHandler)
	}

	app := newServer(tlsCfg.HTTPSPort, handler)
	app.TLSConfig = serverTLS
	return &serverPair{
		app:         app,
		redirect:    newServer(tlsCfg.HTTPPort, redirectHandler),
		certManager: manager,
	}, nil
}

func serve(srv *http.Server) error {
	if srv.TLSConfig != nil {
		return srv.ListenAndServeTLS("", "")
	}
	return srv.ListenAndServe()
}

func httpsRedirectHandler(httpsPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := *r.URL
		target.Scheme = "https"
		target.Host = httpsAuthority(r.Host, httpsPort)
		http.Redirect(w, r, target.String(), http.StatusPermanentRedirect)
	})
}

func httpsAuthority(hostport string, httpsPort int) string {
	host := hostport
	if parsedHost, _, err := net.SplitHostPort(hostport); err == nil {
		host = parsedHost
	} else {
		host = strings.TrimPrefix(strings.TrimSuffix(hostport, "]"), "[")
	}
	if httpsPort == 443 {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(httpsPort))
}

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/proxy"
	"github.com/aditya-rai-5/reverse_proxy/ratelimit"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

// buildHandler creates the proxy handler and applies the global rate limiting middleware if configured.
func buildHandler(cfg *config.Config, r *router.Router, observers ...proxy.RequestObserver) http.Handler {
	handler, _ := buildManagedHandler(cfg, r, observers...)
	return handler
}

func buildManagedHandler(cfg *config.Config, r *router.Router, observers ...proxy.RequestObserver) (http.Handler, func()) {
	proxyHandler := proxy.NewWithTransportAndBodyLimit(r, nil, cfg.MaxRequestBodyBytes)
	if len(observers) > 0 {
		proxyHandler.SetRequestObserver(observers[0])
	}
	var handler http.Handler = proxyHandler
	cleanup := proxyHandler.CloseIdleConnections
	if cfg.RateLimit != nil && cfg.RateLimit.Enabled() {
		rl := ratelimit.New(ratelimit.Config{
			ClientIPResolver:  r.ClientIPResolver(),
			RequestsPerMinute: cfg.RateLimit.RequestsPerMinute,
			Burst:             cfg.RateLimit.Burst,
		})
		var onRejected func()
		if len(observers) > 0 {
			if rejectionObserver, ok := observers[0].(interface{ ObserveRateLimitRejection(string) }); ok {
				onRejected = func() { rejectionObserver.ObserveRateLimitRejection("global") }
			}
		}
		handler = rl.Middleware(handler, onRejected)
		cleanup = func() { rl.Stop(); proxyHandler.CloseIdleConnections() }
	}
	return handler, cleanup
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("reverse_proxy", flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("config", "config.yaml", "configuration file path")
	check := flags.Bool("check-config", false, "validate configuration and exit")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *check {
		if cfg.TLS != nil && !cfg.TLS.AutoCert {
			if _, err := newServers(cfg, nil); err != nil {
				return err
			}
		}
		fmt.Fprintln(output, "Configuration valid")
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	reloads := make(chan os.Signal, 1)
	signal.Notify(reloads, syscall.SIGHUP)
	defer signal.Stop(reloads)
	runtime, err := newRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer runtime.close()
	servers, err := newServers(cfg, runtime)
	if err != nil {
		return err
	}
	for _, srv := range servers.all() {
		protocol := "HTTP"
		if srv.TLSConfig != nil {
			protocol = "HTTPS"
		}
		log.Printf("Reverse proxy %s listener on %s", protocol, srv.Addr)
		go func(srv *http.Server) {
			if err := serve(srv); err != nil && err != http.ErrServerClosed {
				log.Printf("server error on %s: %v", srv.Addr, err)
				cancel()
			}
		}(srv)
	}
	log.Printf("Health check: path=%s interval=%s timeout=%s",
		cfg.HealthCheck.Path, cfg.HealthCheck.Interval, cfg.HealthCheck.Timeout)

running:
	for {
		select {
		case <-ctx.Done():
			break running
		case <-reloads:
			if err := runtime.reload(*path); err != nil {
				log.Printf("configuration reload rejected: %v", err)
			} else {
				log.Printf("configuration reloaded from %s", *path)
			}
		}
	}
	log.Println("Shutting down…")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	for _, srv := range servers.all() {
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("graceful shutdown error for %s: %v", srv.Addr, err)
			_ = srv.Close()
		}
	}

	return nil
}

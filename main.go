package main

import (
	"fmt"
	"log"
	"net/http"

	"github.com/aditya-rai-5/reverse_proxy/config"
	"github.com/aditya-rai-5/reverse_proxy/proxy"
	"github.com/aditya-rai-5/reverse_proxy/router"
)

func main() {
	// Load config from YAML file
	cfg, err := config.Load("config.yaml")
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Build the router (path → load balancer mappings)
	r, err := router.New(cfg)
	if err != nil {
		log.Fatalf("failed to build router: %v", err)
	}

	// Create the proxy handler
	handler := proxy.New(r)

	// Start the HTTP server
	addr := fmt.Sprintf(":%d", cfg.Port)
	log.Printf("Reverse proxy listening on %s", addr)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

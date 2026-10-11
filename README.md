# Reverse Proxy in Go

A fast, production-style HTTP reverse proxy with path-based routing, round-robin load balancing, active background health checks, graceful shutdown, and a `/healthz` observability endpoint.

## Features

- **Path-Based Routing:** Longest-prefix matching route resolution with boundary protection (e.g. `/api` won't mistakenly match `/apikeys`).
- **Round-Robin Load Balancing:** Thread-safe, lock-free backend distribution via atomic operations.
- **Active Health Checks:** Periodic background HTTP probing of all upstream backends with automatic failure detection and recovery.
- **Failover & Fault Tolerance:** Dead backends are automatically skipped; returns HTTP `503 Service Unavailable` if all backends are down.
- **Header Forwarding:** Automatically preserves and forwards `X-Forwarded-For` and `X-Forwarded-Host`.
- **Healthz Endpoint:** Exposes `GET /healthz` returning JSON status of all backends and routes.
- **Graceful Shutdown:** Catches `SIGINT`/`SIGTERM` and drains in-flight requests with a 10-second timeout.

## Running Tests

Run all unit and integration tests across all packages:

```bash
go test -v ./...
```

Run tests with test coverage:

```bash
go test -cover ./...
```

## Running the Proxy

1. Start the proxy server:
```bash
go run main.go
```

2. Inspect the health of all upstream backends:
```bash
curl http://localhost:8080/healthz
```
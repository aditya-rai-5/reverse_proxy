# Reverse Proxy in Go

A fast, production-style HTTP reverse proxy with path-based routing, path stripping & rewriting, round-robin load balancing, active background health checks, graceful shutdown, and a `/healthz` observability endpoint.

## Features

- **Path-Based Routing:** Longest-prefix matching route resolution with boundary protection (e.g. `/api` won't mistakenly match `/apikeys`).
- **Path Stripping & Rewriting:** Strip prefixes (`strip_prefix: "/api"`) or rewrite paths via regex (`rewrite: { pattern: "^/legacy/(.*)$", replacement: "/v2/$1" }`) before forwarding upstream.
- **Round-Robin Load Balancing:** Thread-safe, lock-free backend distribution via atomic operations.
- **Active Health Checks:** Periodic background HTTP probing of all upstream backends with automatic failure detection and recovery.
- **Failover & Fault Tolerance:** Dead backends are automatically skipped; returns HTTP `503 Service Unavailable` if all backends are down.
- **Header Forwarding:** Automatically preserves and forwards `X-Forwarded-For` and `X-Forwarded-Host`.
- **Healthz Endpoint:** Exposes `GET /healthz` returning JSON status of all backends and routes.
- **Graceful Shutdown:** Catches `SIGINT`/`SIGTERM` and drains in-flight requests with a 10-second timeout.

## Configuration

Configure routes and features via `config.yaml`:

```yaml
port: 8080

health_check:
  path: "/health"
  interval: 10s
  timeout: 5s

routes:
  # Path stripping: incoming /api/users is forwarded to backend as /users
  - path: "/api"
    strip_prefix: "/api"
    backends:
      - url: "http://localhost:9001"
      - url: "http://localhost:9002"

  # Path rewriting: incoming /legacy/orders is forwarded to backend as /v2/orders
  - path: "/legacy"
    rewrite:
      pattern: "^/legacy/(.*)$"
      replacement: "/v2/$1"
    backends:
      - url: "http://localhost:9003"

  # Fallback root route
  - path: "/"
    backends:
      - url: "http://localhost:9000"
```

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
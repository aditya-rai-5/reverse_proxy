# Reverse Proxy in Go

A fast, production-style HTTP reverse proxy with path-based routing, path stripping & rewriting, round-robin load balancing, active background health checks, graceful shutdown, and a `/healthz` observability endpoint.

## Features

- **Path-Based Routing:** Longest-prefix matching route resolution with boundary protection (e.g. `/api` won't mistakenly match `/apikeys`).
- **Path Stripping & Rewriting:** Strip prefixes (`strip_prefix: "/api"`) or rewrite paths via regex (`rewrite: { pattern: "^/legacy/(.*)$", replacement: "/v2/$1" }`) before forwarding upstream.
- **Advanced Load Balancing Algorithms:**
  - **Round-Robin (`round_robin`):** Lock-free, atomic round-robin request distribution.
  - **Weighted Round-Robin (`weighted_round_robin`):** Smooth Weighted Round-Robin (Nginx algorithm) distributing traffic according to server weights (e.g., `weight: 2`).
  - **Least Connections (`least_conn`):** Real-time tracking of active in-flight requests per backend, routing each request to the least busy healthy server.
  - **IP Hash (`ip_hash`):** Deterministic hashing of client IP (from `RemoteAddr` or `X-Forwarded-For`), pinning clients to backends with automatic failover.
  - **Sticky Sessions (`sticky`):** Cookie-based session persistence (configurable `cookie_name`) ensuring subsequent user requests stay on the same backend, with automatic health failover.
- **Automatic Request Retries:** If an upstream backend returns `502 Bad Gateway`, `504 Gateway Timeout`, or encounters a connection error/timeout, the proxy immediately replays the request (including buffered request body) on an alternate healthy backend before failing to the client.
- **Circuit Breaking:** Tracks consecutive failures per backend. If a backend fails 5 consecutive times (configurable `failure_threshold`), the circuit breaker trips **Open**, immediately skipping the backend for subsequent client requests so clients are not slowed down. Automatically transitions to **Half-Open** after a cooldown period (`cooldown`) to test backend recovery.
- **Active Health Checks:** Periodic background HTTP probing of all upstream backends with automatic failure detection and recovery.
- **Failover & Fault Tolerance:** Dead backends are automatically skipped across all load balancing algorithms; returns HTTP `503 Service Unavailable` if all backends are down.
- **Header Forwarding:** Automatically preserves and forwards `X-Forwarded-For` and `X-Forwarded-Host`.
- **Healthz Endpoint:** Exposes `GET /healthz` returning JSON status of all backends (including health status, weight, active in-flight connections, circuit breaker state, consecutive failures, and route algorithm).
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
  # Weighted Round-Robin with Retries & Circuit Breaker
  - path: "/api"
    strip_prefix: "/api"
    algorithm: "weighted_round_robin"
    retries: 2             # Retry up to 2 times on other healthy backends
    timeout: 3s            # Per-attempt request timeout
    circuit_breaker:
      failure_threshold: 5 # Trip breaker Open after 5 consecutive failures
      cooldown: 10s        # Cooldown duration before attempting recovery
    backends:
      - url: "http://localhost:9001"
        weight: 2
      - url: "http://localhost:9002"
        weight: 1

  # Least Connections: routes to backend with least in-flight requests
  - path: "/compute"
    algorithm: "least_conn"
    retries: 1
    timeout: 5s
    backends:
      - url: "http://localhost:9003"
      - url: "http://localhost:9004"

  # Sticky Sessions: uses session cookie to keep subsequent requests on the same backend
  - path: "/app"
    algorithm: "sticky"
    cookie_name: "SERVERID"
    retries: 1
    backends:
      - url: "http://localhost:9005"
      - url: "http://localhost:9006"

  # IP Hash: hashes client IP to deterministically pin user requests
  - path: "/ip"
    algorithm: "ip_hash"
    retries: 1
    backends:
      - url: "http://localhost:9007"
      - url: "http://localhost:9008"

  # Fallback root route (defaults to round-robin)
  - path: "/"
    algorithm: "round_robin"
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
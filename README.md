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
- **Rate Limiting Middleware:** Protects backends from abuse using the token bucket algorithm (`golang.org/x/time/rate`). Enforces per-IP limits configured in YAML (globally or per-route via `requests_per_minute` and `burst`). Returns HTTP `429 Too Many Requests` with a `Retry-After` header when exceeded, with automatic background TTL cleanup of inactive client IP entries.
- **Safe Request Retries:** By default only `GET`, `HEAD`, and `OPTIONS` retry after an upstream `502`, `504`, connection error, or per-attempt timeout. Write retries require route opt-in and an `Idempotency-Key` enforced by the backends. Client cancellation stops retries without counting as a backend failure.
- **Bounded Retry Buffering:** Request bodies up to `max_request_body_bytes` (10 MiB by default) are retained for retries. Larger bodies are streamed to one backend without retries.
- **Circuit Breaking:** Tracks consecutive failures per backend. If a backend fails 5 consecutive times (configurable `failure_threshold`), the circuit breaker trips **Open**, immediately skipping the backend for subsequent client requests so clients are not slowed down. Automatically transitions to **Half-Open** after a cooldown period (`cooldown`) to test backend recovery.
- **Active Health Checks:** Periodic background HTTP probing of all upstream backends with automatic failure detection and recovery.
- **Failover & Fault Tolerance:** Dead backends are automatically skipped across all load balancing algorithms; returns HTTP `503 Service Unavailable` if all backends are down.
- **Header Forwarding:** Automatically preserves and forwards `X-Forwarded-For` and `X-Forwarded-Host`.
- **Healthz Endpoint:** Exposes `GET /healthz` returning JSON status of all backends (including health status, weight, active in-flight connections, circuit breaker state, consecutive failures, and route algorithm).
- **Prometheus Metrics:** Exposes `GET /metrics` with request totals by route/backend/status, request latency histograms, and backend health gauges.
- **Graceful Shutdown:** Catches `SIGINT`/`SIGTERM` and drains in-flight requests with a 10-second timeout.
- **HTTPS Termination:** Serve HTTPS using a configured certificate/key pair or automatically provision and renew Let's Encrypt certificates with `autocert`. TLS mode redirects ordinary HTTP traffic to HTTPS and leaves ACME HTTP-01 challenge paths available.

## Configuration

Configure routes and features via `config.yaml`:

```yaml
port: 8080
max_request_body_bytes: 10485760 # 10 MiB retained for retries; larger bodies stream once

# Global per-IP rate limiting (token bucket via golang.org/x/time/rate)
rate_limit:
  requests_per_minute: 60
  burst: 10

health_check:
  path: "/health"
  interval: 10s
  timeout: 5s

routes:
  # Weighted Round-Robin with Retries & Circuit Breaker
  - path: "/api"
    strip_prefix: "/api"
    algorithm: "weighted_round_robin"
    retries: 2             # Retry eligible requests on other healthy backends
    retry_methods: [GET, HEAD, OPTIONS] # Defaults; writes require explicit opt-in
    timeout: 3s            # Per-attempt request timeout
    circuit_breaker:
      failure_threshold: 5 # Trip breaker Open after 5 consecutive failures
      cooldown: 10s        # Cooldown duration before attempting recovery
    backends:
      - url: "http://localhost:9001"
        weight: 2
      - url: "http://localhost:9002"
        weight: 1

  # Least Connections: routes to backend with least in-flight requests (with route-level rate limit)
  - path: "/compute"
    algorithm: "least_conn"
    retries: 1
    timeout: 5s
    rate_limit:
      requests_per_minute: 30
      burst: 5
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

### Retry safety

`retries` sets the retry budget. `retry_methods` selects which methods may use
that budget and defaults to `[GET, HEAD, OPTIONS]`. An explicit empty list
(`retry_methods: []`) disables proxy retries for the route. A configured list
replaces the default rather than extending it. Existing configurations keep
read-method failover; POST, PATCH, PUT, and DELETE are forwarded once by default.

To retry a write method, include it explicitly:

```yaml
routes:
  - path: "/payments"
    retries: 1
    retry_methods: [GET, HEAD, OPTIONS, POST]
    backends:
      - url: "http://payments-1:9001"
      - url: "http://payments-2:9002"
```

POST, PATCH, PUT, and DELETE also require exactly one non-empty
`Idempotency-Key` request header. Without it, even an opted-in write is forwarded
once. The same key and buffered body are forwarded on each attempt. A key alone
does not enable retries on a route that has not opted in. Unknown retry methods
are rejected during configuration loading/router construction.

Only enable write retries when **all backends share durable, atomic
deduplication for the key**, bind it to the operation/payload, and retain the
result long enough for retries. The proxy checks eligibility and preserves the
key; it does not store keys or deduplicate application writes. Larger bodies
still stream once without retries, regardless of method/key eligibility.

Cancellation is checked before forwarding and after each attempt. It stops
retries and leaves backend failure counters unchanged. Observations use `499`
for client cancellation and `504` for an expired client request deadline;
disconnected clients may not receive those responses. A route's per-attempt
timeout can still count as an upstream failure and retry while the original
request context remains active. Once response headers have been sent, no error
body is appended on cancellation.

### HTTPS with certificate files

Add a `tls` section to terminate TLS in the proxy. HTTPS listens on port `443` and HTTP redirects from port `80` by default; both ports can be changed for local development or deployments behind a port mapper.

```yaml
tls:
  cert_file: "/etc/reverse-proxy/tls/fullchain.pem"
  key_file: "/etc/reverse-proxy/tls/privkey.pem"
  https_port: 443
  http_port: 80
```

### Let's Encrypt automatic certificates

Set `auto_cert: true`, list the exact domain names this proxy serves, and keep the cache directory on persistent storage. DNS for each domain must point to this host, and the configured HTTP/HTTPS ports must be reachable by Let's Encrypt (normally ports 80 and 443).

```yaml
tls:
  auto_cert: true
  domains:
    - "proxy.example.com"
  email: "admin@example.com"
  cache_dir: "/var/lib/reverse-proxy/autocert"
  https_port: 443
  http_port: 80
```

Use either `cert_file` plus `key_file` or `auto_cert`, not both. Without a `tls` section, the existing plain HTTP listener on `port` is used.

## Running Tests

Run all unit and integration tests across all packages:

```bash
go test -v ./...
```

Run tests with test coverage:

```bash
go test -cover ./...
```

Prometheus can scrape the proxy with:

```yaml
scrape_configs:
  - job_name: reverse-proxy
    static_configs:
      - targets: ["localhost:8080"]
```

When TLS is enabled, configure the scrape target to use the HTTPS listener.

## Running the Proxy

1. Start the proxy server:
```bash
go run main.go
```

2. Inspect the health of all upstream backends:
```bash
curl http://localhost:8080/healthz
```

## One-Click Docker Demo

The Compose demo builds the proxy, starts three mock backends, Prometheus, and
Grafana, and runs a small traffic generator so the dashboard has live data as
soon as the stack is healthy. It uses the separate [`demo/config.yaml`](demo/config.yaml)
and does not change the normal `config.yaml` setup.

```bash
docker compose up --build
```

Open [Grafana](http://localhost:3000) and choose **Reverse Proxy Demo** in the
**Reverse Proxy** folder. Sign in with `admin` / `demo`. The preloaded dashboard
shows request rate, p50/p95/p99 latency, rate-limit rejections, circuit-breaker
trips and state, and backend health. Prometheus is also available at
[localhost:9090](http://localhost:9090), and the proxy is at
[localhost:8080](http://localhost:8080).

Try the routes directly:

```bash
curl http://localhost:8080/api/demo
curl http://localhost:8080/limited/demo
curl http://localhost:8080/healthz
```

The generator continuously calls `/api`, `/limited`, and `/breaker`, which fills
the request and rate-limit panels and repeatedly trips the breaker. Backend3 is
configured to return HTTP 502 for normal requests while `/health` remains OK,
so it acts as a controlled failure source without marking the backend unhealthy.
You can also exercise the failure route directly:

```bash
curl -i http://localhost:8080/breaker/demo
```

The breaker panel records the trip while the backend health panel shows backend3
as reachable. Stop the demo with `Ctrl+C`, or run `docker compose down` in another
terminal. Add `-v` to `down` if you also want to remove the Prometheus and
Grafana data volumes.

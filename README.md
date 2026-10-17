# Reverse Proxy in Go

An HTTP reverse proxy written in Go, with path-based routing, five load balancing algorithms, backend health checks, safe retries, circuit breakers, rate limiting, HTTPS termination, and Prometheus metrics.

Start with the Docker demo for a working stack with mock backends and monitoring, or run the proxy locally against your own upstream services.

## Contents

- [Features](#features)
- [Quick start: Docker demo](#quick-start-docker-demo)
- [Run locally](#run-locally)
- [Configuration](#configuration)
- [Retry safety](#retry-safety)
- [HTTPS](#https)
- [Health and monitoring](#health-and-monitoring)
- [Troubleshooting](#troubleshooting)
- [Development and contributions](#development-and-contributions)

## Features

- Longest-prefix routing with path boundaries: `/api` matches `/api/users`, but not `/apikeys`.
- Prefix stripping and regular expression path rewriting.
- Round-robin, weighted round-robin, least connections, IP hash, and cookie-based sticky sessions.
- Background health checks, automatic backend failover, and per-backend circuit breakers.
- Global and per-route token bucket rate limits with HTTP `429` and `Retry-After` responses.
- Configurable retries with bounded request body buffering and explicit write-method safeguards.
- HTTPS using certificate files or automatic Let's Encrypt certificates.
- JSON backend status at `/healthz` and Prometheus metrics at `/metrics`.
- Graceful shutdown on `SIGINT` or `SIGTERM`, with up to 10 seconds to drain requests.

## Quick start: Docker demo

Requires Docker with the Compose plugin. Run these commands from the repository root:

```bash
git clone https://github.com/aditya-rai-5/reverse_proxy.git
cd reverse_proxy
docker compose up --build
```

The stack starts the proxy, three mock backends, Prometheus, Grafana, and a traffic generator. It uses [demo/config.yaml](demo/config.yaml), mounted as the container's `/app/config.yaml`.

Once the services start:

```bash
curl -i http://localhost:8080/api/demo
curl -i http://localhost:8080/limited/demo
curl -i http://localhost:8080/breaker/demo
curl -i http://localhost:8080/healthz
curl http://localhost:8080/metrics
```

- Proxy: [localhost:8080/api/demo](http://localhost:8080/api/demo).
- Prometheus: [localhost:9090](http://localhost:9090).
- Grafana: [localhost:3000](http://localhost:3000), using **admin / demo**. Open **Reverse Proxy → Reverse Proxy Demo**.

The traffic generator continuously exercises `/api`, `/limited`, and `/breaker`. The `/limited` route deliberately produces rate-limit rejections. Backend3 returns `502` for normal requests while its `/health` endpoint stays healthy, so `/breaker` demonstrates circuit-breaker trips. During an open circuit, `/healthz` can report `503` even though backend3 is reachable.

Stop the stack with `Ctrl+C`, then remove the containers and network:

```bash
docker compose down
```

Prometheus and Grafana data remain in named volumes. To also delete that demo data:

```bash
docker compose down -v
```

The bundled Grafana credentials are for the local demo only.

## Run locally

Requires Git and **Go 1.27.1**, as declared in [go.mod](go.mod). Docker is optional for this workflow.

```bash
git clone https://github.com/aditya-rai-5/reverse_proxy.git
cd reverse_proxy
go mod download
```

1. Start your upstream HTTP services. Each backend should serve the configured health-check path with a `2xx` response.
2. Edit [config.yaml](config.yaml) to point to those services. The checked-in example references local ports `9000`–`9008`; `go run` does not start those backends.
3. Run the proxy from the repository root:

   ```bash
   go run .
   ```

4. In another terminal, send a request and inspect backend status:

   ```bash
   curl -i http://localhost:8080/api/users
   curl -i http://localhost:8080/healthz
   ```

To build and run a binary instead:

```bash
go build .
./reverse_proxy
```

The application always reads `config.yaml` from its **current working directory**. There is no command-line config-path flag. Restart the process after changing configuration. Press `Ctrl+C` to shut down.

## Configuration

The following is a complete, smaller alternative to the checked-in configuration. Replace the backend URLs with your services:

```yaml
port: 8080
max_request_body_bytes: 10485760 # 10 MiB retry buffer per eligible request

health_check:
  path: /health
  interval: 10s
  timeout: 5s

rate_limit:
  requests_per_minute: 60
  burst: 10

routes:
  - path: /api
    strip_prefix: /api
    algorithm: weighted_round_robin
    retries: 1
    retry_methods: [GET, HEAD, OPTIONS]
    timeout: 3s
    circuit_breaker:
      failure_threshold: 5
      cooldown: 10s
    backends:
      - url: http://localhost:9001
        weight: 2
      - url: http://localhost:9002
        weight: 1
```

With this configuration, `/api/users` reaches an upstream as `/users`. An unrelated path has no matching route. Add a route with `path: /` if you want a catch-all.

### Routing and load balancing

Routes are evaluated by longest matching path, independently of their order in YAML. Prefix stripping runs before regex rewriting. For example, this optional route field rewrites `/legacy/users` to `/v2/users`:

```yaml
rewrite:
  pattern: '^/legacy/(.*)$'
  replacement: '/v2/$1'
```

Choose a route's `algorithm`:

- `round_robin`: rotates across healthy backends; the default when no custom weights are supplied.
- `weighted_round_robin`: distributes requests using smooth weighted round-robin. A backend's omitted or zero `weight` becomes `1`. If `algorithm` is omitted and any weight exceeds `1`, weighted round-robin is selected automatically.
- `least_conn`: selects the healthy backend with the fewest active requests.
- `ip_hash`: selects a backend using the client IP.
- `sticky`: keeps a client on a backend using a cookie. Set `cookie_name` to customize its name; the default is `SERVERID`.

Unhealthy backends and open circuits are skipped during selection.

### Defaults and limits

- `port` is required, including when TLS is configured. Without TLS, it is the HTTP listener port.
- `max_request_body_bytes` defaults to `10485760` (10 MiB) when omitted or zero. It limits retry buffering, **not upload size**: larger bodies stream to one backend without retries.
- `health_check` defaults to `/health`, a `10s` interval, and a `5s` timeout. Probes use the backend's scheme and host plus this path, without route rewriting. Non-`2xx` responses and connection failures mark a backend unhealthy.
- `timeout` is a per-attempt upstream timeout, such as `3s`. Omitted or zero adds no route-specific deadline.
- `circuit_breaker` defaults to a failure threshold of `5` and a `10s` cooldown, even when the section is omitted. After cooldown, a half-open circuit allows a recovery attempt.
- `rate_limit.requests_per_minute` enables per-IP limiting when positive. Omitted or zero disables that limiter. For an enabled limiter, omitted or zero `burst` defaults to `1`.

A route may also define its own `rate_limit` section. Global and route limits both apply; a route limit does not replace the global limit. `/healthz` and `/metrics` bypass these proxy request limiters.

## Retry safety

`retries` sets the number of **additional attempts** after the initial request:

- Omitted or `null`: defaults to `min(number of backends - 1, 2)`.
- `retries: 0`: disables retries, including on routes with multiple backends.
- A positive value: sets an explicit retry budget. Actual attempts also depend on eligible, available backends.

The effective budget, including zero, is reported by `/healthz`. Retries can follow upstream `502` or `504` responses, connection errors, or per-attempt timeouts. They are not triggered by every `5xx` response.

`retry_methods` defaults to `[GET, HEAD, OPTIONS]`. A configured list replaces that default; `retry_methods: []` disables retries. Supported methods are `GET`, `HEAD`, `OPTIONS`, `POST`, `PATCH`, `PUT`, and `DELETE`.

To opt a write method into retries, configure it explicitly:

```yaml
routes:
  - path: /payments
    retries: 1
    retry_methods: [GET, HEAD, OPTIONS, POST]
    backends:
      - url: http://payments-1:9001
      - url: http://payments-2:9002
```

`POST`, `PATCH`, `PUT`, and `DELETE` additionally require exactly one non-empty `Idempotency-Key` request header. The same key and buffered body are forwarded on each attempt. A key alone does not enable retries, and oversized bodies still stream once.

Enable write retries only when **all backends share durable, atomic deduplication** for the key, bind it to the operation and payload, and retain the result long enough for retries. The proxy does not store keys or deduplicate writes.

Client cancellation stops retries without increasing backend failure counters. Observations use `499` for cancellation and `504` for an expired client deadline; a disconnected client may not receive a response. A route's per-attempt timeout can still count as an upstream failure while the client request remains active.

## HTTPS

Add one of the following top-level `tls` sections to your configuration, keeping `port` and `routes`.

### Certificate files

```yaml
tls:
  cert_file: /etc/reverse-proxy/tls/fullchain.pem
  key_file: /etc/reverse-proxy/tls/privkey.pem
  https_port: 443
  http_port: 80
```

Both files must be readable by the proxy process. For local development, use unprivileged ports such as `8443` and `8080` if needed.

### Automatic Let's Encrypt certificates

```yaml
tls:
  auto_cert: true
  domains:
    - proxy.example.com
  email: admin@example.com
  cache_dir: /var/lib/reverse-proxy/autocert
  https_port: 443
  http_port: 80
```

Use exact hostnames without schemes, ports, or wildcards. DNS must point to the proxy, and public ports `80` and `443` must reach its HTTP and HTTPS listeners. Persist the certificate cache in a directory writable by the proxy. Enabling automatic certificates accepts the ACME provider's terms through `autocert`.

Choose certificate files or `auto_cert`, not both. HTTPS defaults to port `443`, and HTTP defaults to port `80`. Ordinary HTTP requests redirect to HTTPS; automatic certificate mode also serves ACME HTTP-01 challenges. Without `tls`, the proxy serves plain HTTP on `port`.

Keep private keys and certificate caches out of commits. The bundled Docker demo uses plain HTTP; a TLS deployment needs its own port mappings and certificate/cache mounts.

## Health and monitoring

`GET /healthz` returns JSON with overall status, route algorithms, retry budgets, retry methods, rate limits, and backend details. Those details include health and circuit state, with weights, active connections, and failure counters where applicable.

It returns `200` when all backends are healthy and no circuit is open, or `503` with status `degraded` otherwise. A degraded result does not necessarily mean every route is unavailable.

`GET /metrics` exposes Prometheus text metrics for requests, latency, backend health, rate-limit rejections, and circuit breakers. The demo includes [Prometheus configuration](prometheus/prometheus.yml) and a provisioned [Grafana dashboard](grafana/dashboards/reverse-proxy.json).

For a Prometheus instance running directly on the same host:

```yaml
scrape_configs:
  - job_name: reverse-proxy
    static_configs:
      - targets: ["localhost:8080"]
```

Inside a container, `localhost` refers to that container. The Compose setup uses `proxy:8080`. When TLS is enabled, scrape the HTTPS listener with `scheme: https` and the appropriate certificate trust settings.

## Troubleshooting

- **Cannot read `config.yaml`:** run from the repository root, or place the configuration in the binary's working directory.
- **Port already in use:** stop the conflicting process, change `port` locally, or change the published host port in Compose. Local and Docker workflows both use `8080` by default.
- **`502` response:** check that the URL matches a route and that upstream URLs are reachable. Inspect proxy logs for forwarding errors.
- **`503` response:** inspect `/healthz` for unhealthy backends or open circuits. Verify upstream `/health` responses and the configured probe path.
- **`429` response:** check global and route rate limits; the demo's `/limited` route intentionally rejects excess traffic.
- **Empty dashboard:** check `docker compose ps` and `docker compose logs proxy prometheus traffic-generator`, then allow time for scrapes and traffic to accumulate.
- **Go build fails:** check `go version` against [go.mod](go.mod) and ensure dependencies can be downloaded. The Docker build pins the same Go version.

## Development and contributions

See [contribution.md](contribution.md) for setup, project structure, coding conventions, regression testing, and pull request guidance.

Run the core checks from the repository root:

```bash
go build .
go test -v ./...
go test -race ./...
go vet ./...
```

For coverage:

```bash
go test -cover ./...
```

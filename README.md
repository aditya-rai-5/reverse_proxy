# Reverse Proxy in Go

An HTTP reverse proxy written in Go, with path-based routing, five load balancing algorithms, backend health checks, safe retries, circuit breakers, rate limiting, HTTPS termination, and Prometheus metrics.

Start with the Docker demo for mock backends and monitoring, or run the proxy locally against your own upstream services.

The core HTTP features are implemented and tested. The project still has correctness and operational gaps described under [Current limitations](#current-limitations); it does not yet have a production capacity or reliability baseline.

## Contents

- [Features](#features)
- [Current limitations](#current-limitations)
- [Project structure](#project-structure)
- [Quick start: Docker demo](#quick-start-docker-demo)
- [Run locally](#run-locally)
- [Configuration](#configuration)
- [Retry safety](#retry-safety)
- [HTTPS](#https)
- [Health and monitoring](#health-and-monitoring)
- [Troubleshooting](#troubleshooting)
- [Development and contributions](#development-and-contributions)
- [Dependency maintenance](#dependency-maintenance)
- [License](#license)

## Features

- Longest-prefix routing with path boundaries: `/api` matches `/api/users`, but not `/apikeys`.
- Prefix stripping and regular expression path rewriting.
- Round-robin, weighted round-robin, least connections, IP hash, and cookie-based sticky sessions.
- Background health checks, automatic backend failover, and per-backend circuit breakers.
- Global and per-route token bucket rate limits with HTTP `429` and `Retry-After` responses.
- Shared client-IP resolution for rate limiting and IP hashing, with explicit trusted-proxy CIDRs.
- Configurable retries with bounded request body buffering and explicit write-method safeguards.
- HTTPS using certificate files or automatic Let's Encrypt certificates.
- JSON backend status at `/healthz` and Prometheus metrics at `/metrics`.
- Signal-driven HTTP shutdown on `SIGINT` or `SIGTERM`, with a shared 10-second server drain deadline. Health probes are canceled during shutdown.
- GitHub Actions build, vet, race-test, Compose-validation, and Docker-build jobs.

## Current limitations

Review scope: source and local validation at commit `2565da1` on **2026-10-25**. These are current implementation limits, not claims that every deployment will encounter them.

- **HTTP correctness:** headers named by `Connection` are not removed, `Trailer` handling is incomplete, and `X-Forwarded-Proto` is not sanitized or generated. Do not treat that header as an authenticated scheme signal.
- **URL forwarding:** use backend origin URLs such as `http://backend:9001`. Backend base paths and queries are rejected by configuration validation, and encoded path separators such as `%2F` are decoded during forwarding.
- **Response failures:** upstream body-copy errors are ignored after response headers are sent; a truncated response can still count as a backend success.
- **Resource controls:** listener read/header/write/idle timeouts are unset. A positive route `timeout` limits each upstream attempt, but does not cover initial retry-body buffering or impose a total request deadline. Upload size, concurrent buffers, and tracked limiter identities have no configured hard cap.
- **Recovery and lifecycle:** half-open breakers admit multiple requests, late successes can close an open breaker, listener failures currently exit with status zero, and shutdown of stalled upstream requests still depends on request cancellation.
- **Operations:** `/healthz` and `/metrics` share the application listener without authentication and bypass rate limits. `/healthz` reports aggregate backend condition, not separate liveness/readiness. Rate limits and resilience state are local to each process. Sticky cookies have limited security/scoping controls.
- **Protocols and deployment:** WebSocket/CONNECT tunnels, reliable SSE flushing, response trailers, and validated gRPC support are not implemented. Live ACME issuance, the running Compose stack, load behavior, and release deployment were not verified in this review.

Dynamic service discovery, authentication middleware, distributed rate limits, and release publishing are also outside the current implementation. Report reproducible bugs using the [contribution guide](CONTRIBUTING.md#report-a-bug-or-propose-a-feature).

## Project structure

```text
main.go                 Startup, middleware, signals, and shutdown
server.go               HTTP/HTTPS listeners and redirects
health.go               Aggregate backend status endpoint
config/                 YAML settings, defaults, and validation
router/                 Route matching, rewrites, and retry eligibility
proxy/                  Forwarding, body replay, and request observations
clientip/               Trusted-proxy client-IP resolution
loadbalancer/           Five backend-selection algorithms
circuitbreaker/         Application failure and recovery state
healthcheck/            Periodic backend HTTP probes
ratelimit/              In-memory per-IP token buckets
metrics/                Prometheus text exposition
config.yaml             Local upstream example
demo/config.yaml        Docker demo routes
prometheus/, grafana/    Scraping and dashboard provisioning
.github/                CI and dependency-update configuration
```

Tests live beside their implementation, with application integration and TLS setup tests at the root. Configuration is loaded at startup and can be reloaded with SIGHUP; runtime state is held in memory. Normal requests pass through global limiting, route matching, route limiting, backend selection, and forwarding. Management endpoints are registered separately.

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

The bundled Grafana credentials are for the local demo only. Compose publishes ports `8080`, `9090`, and `3000` on all host interfaces by default; restrict exposure when running on a shared host. The intentional breaker failures can also make the proxy container report unhealthy while `/api` still works.

## Run locally

Requires Git and **Go 1.27.1 or newer**, as declared in [go.mod](go.mod). The [Dockerfile](Dockerfile) currently builds with Go 1.27.2. Docker is optional for this workflow.

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

Select a file explicitly, or omit `--config` to use `config.yaml` in the current working directory:

```bash
go run . --config ./demo/config.yaml --check-config
go build -o reverse_proxy .
./reverse_proxy --config ./config.yaml
# After editing the selected file, reload the running binary:
kill -HUP <proxy-pid>
```

`--check-config` exits successfully only when configuration validation succeeds. It also reads and validates manual TLS certificate/key pairs. It starts no listeners, health probes, limiter workers, ACME requests, or backend requests. Relative file paths (including TLS files) remain relative to the process working directory.

The YAML schema rejects unknown fields at every level, duplicate mapping keys, and multiple documents. Anchors and merge keys are supported within schema fields; arbitrary top-level anchor containers such as `defaults:` are rejected. Listener ports must be 1–65535; omitted/zero TLS ports retain the documented defaults. Route, strip-prefix, and health-check paths must start with one slash and cannot contain queries, fragments, percent escapes, whitespace, controls, backslashes, repeated slashes, or dot segments. Route paths must be unique (overlapping prefixes remain valid).

Backends must be absolute HTTP(S) origin URLs with valid hostnames or bracketed IPv6 addresses and valid optional ports. Credentials, base paths other than `/`, queries, and fragments are rejected because forwarding does not support them.

SIGHUP rereads the selected file and atomically replaces routes, backends, retry policies, limits, trusted proxies, and health-check settings. Requests already running finish with their previous configuration. Invalid files leave the active configuration intact and log an error. Changes to `port` or any `tls` setting are rejected on reload and require a restart. Reload is signal-driven, not an automatic file watcher; use the binary's PID rather than the `go run` parent PID.

Each successful reload starts fresh health, circuit-breaker, load-balancer, rate-limiter, and metrics state. Old health probes are canceled immediately; old limiter workers and idle connections are released after their in-flight requests finish. Management endpoints switch together with the proxy configuration.

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

With this configuration, `/api/users` reaches an upstream as `/users`. An unrelated path has no matching route and returns `502`. Add a route with `path: /` if you want a catch-all.

### Routing and load balancing

Give each route a unique absolute path. Routes are evaluated by longest matching path, independently of their order in YAML. `/api/` matches descendants but not the bare `/api`; `/healthz` and `/metrics` are reserved by the application mux. Prefix stripping runs before regex rewriting. For example, this optional route field rewrites `/legacy/users` to `/v2/users`:

```yaml
rewrite:
  pattern: '^/legacy/(.*)$'
  replacement: '/v2/$1'
```

Choose a route's `algorithm`:

- `round_robin`: rotates across healthy backends; the default when no custom weights are supplied.
- `weighted_round_robin`: distributes requests using smooth weighted round-robin. A backend's omitted or zero `weight` becomes `1`. If `algorithm` is omitted and any weight exceeds `1`, weighted round-robin is selected automatically.
- `least_conn`: selects the healthy backend with the fewest active requests.
- `ip_hash`: selects a backend using the client IP. It hashes over the currently available backend list, so health changes can remap clients.
- `sticky`: keeps a client on a backend using a cookie. Set a distinct `cookie_name` for each sticky route; the default is `SERVERID`. Cookies use `Path=/` and `HttpOnly`, without explicit `Secure`, `SameSite`, or expiry attributes.

Unhealthy backends and open circuits are skipped during selection.

### Defaults and limits

- `port` is required, including when TLS is configured. Without TLS, it is the HTTP listener port.
- `max_request_body_bytes` defaults to `10485760` (10 MiB) when omitted or zero. It limits retry buffering, **not upload size**: larger bodies stream to one backend without retries.
- `health_check` defaults to `/health`, a `10s` interval, and a `5s` timeout. Probes use the backend's scheme and host plus this path, without route rewriting. Non-`2xx` responses and connection failures mark a backend unhealthy.
- `timeout` is a per-attempt upstream timeout, such as `3s`. Omitted or zero adds no route-specific deadline.
- `circuit_breaker` defaults to a failure threshold of `5` and a `10s` cooldown, even when the section is omitted. Successful health probes update backend health but do not clear circuit-breaker failures, close the circuit, or bypass cooldown. After cooldown, a healthy backend becomes eligible for application requests in the half-open state. A successful application request closes the circuit and clears its failures; a failed application request reopens it.
- `rate_limit.requests_per_minute` enables per-IP limiting when positive. Omitted or zero disables that limiter. For an enabled limiter, omitted or zero `burst` defaults to `1`.

A route may also define its own `rate_limit` section. Global and route limits both apply; a route limit does not replace the global limit. `/healthz` and `/metrics` bypass these proxy request limiters.

### Client IP and trusted proxies

By default, rate limiting and IP hash balancing identify clients by the immediate
connection peer (`RemoteAddr`). Client-supplied `X-Forwarded-For`, `X-Real-IP`, and
`Forwarded` headers cannot override that identity.

When running behind an ingress proxy you control, configure its actual source
addresses as CIDRs at the top level:

```yaml
trusted_proxies:
  - "10.20.0.5/32" # Replace with your ingress proxy's source address.
  - "2001:db8:1::5/128" # Example IPv6 address; replace before use.
```

An omitted or empty list trusts no proxies. Invalid CIDRs fail startup. Do not
trust entire private networks or all addresses unless every sender in that range
is a trusted proxy. The bundled configurations use `trusted_proxies: []`.

Only a trusted immediate peer may supply `X-Forwarded-For`. The proxy reads all
header lines in order, walks the chain from right to left, skips trusted proxy
addresses, and uses the first untrusted address. Your ingress must overwrite
incoming forwarding headers or append the actual connecting client's address.
For example, from a trusted peer, `1.2.3.4, 198.51.100.25` resolves to
`198.51.100.25`; the leftmost value may have been supplied by the client.

Missing, malformed, or entirely trusted chains fall back to the immediate peer.
Header addresses must be IP literals without ports or IPv6 zones. IPv4-mapped
IPv6 addresses are normalized to IPv4. `X-Real-IP` and `Forwarded` are never used
for client identity. Deployments previously relying on those headers must configure
their ingress to supply `X-Forwarded-For` instead. Without configured trust,
clients behind an ingress share its rate-limit bucket and IP hash identity.

The upstream receives one sanitized `X-Forwarded-For` address without a port,
rather than the original chain. Incoming `X-Real-IP` and `Forwarded` are removed.
If the peer address cannot be parsed, requests share an empty identity and no
client-IP header is forwarded.

## Retry safety

`retries` sets the number of **additional proxy-loop attempts** after the initial request:

- Omitted or `null`: defaults to `min(number of backends - 1, 2)`.
- `retries: 0`: disables retries, including on routes with multiple backends.
- A positive value: sets an explicit retry budget. Actual attempts also depend on eligible, available backends.

The proxy loop excludes backend URLs already attempted, so a single backend is not retried by that loop. Go's HTTP transport can also replay eligible requests internally on certain connection failures; the configured count is not an exact wire-attempt counter. Replayability is suppressed when route policy disallows retries.

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

It returns `200` when all backends are probe-healthy and no circuit is open, or `503` with status `degraded` otherwise. A degraded result does not necessarily mean every route is unavailable. Backends start healthy before their first probe, and half-open circuits do not by themselves make the response degraded. The handlers do not restrict requests to GET.

Both management endpoints expose configured backend URLs. Keep them behind appropriate network/access controls and avoid credentials in backend URLs.

`GET /metrics` exposes Prometheus text metrics for requests, latency, backend health, rate-limit rejections, and circuit breakers. The demo includes [Prometheus configuration](prometheus/prometheus.yml) and a provisioned [Grafana dashboard](grafana/dashboards/reverse-proxy.json).

Metrics use configured route/backend labels rather than individual request paths:

- `http_requests_total{route,backend,status_code}` counts completed proxy-handler requests.
- `http_request_duration_seconds` exposes a latency histogram in seconds with the same labels.
- `rate_limit_rejections_total{route}` includes route rejections and `route="global"` for global rejections.
- `backend_healthy_gauge{route,backend}` reports probe health as `0` or `1`.
- `circuit_breaker_state{route,backend,state}` reports one active state per backend.
- `circuit_breaker_trips_total{route,backend}` counts transitions into the open state.

Global rate-limit rejections bypass the request counter/histogram; route rejections enter both request and rejection metrics. Retries produce one final request observation labeled with the last backend, not one observation per attempt. Management requests are excluded. Counters reset when the process restarts.

For a Prometheus instance running directly on the same host:

```yaml
scrape_configs:
  - job_name: reverse-proxy
    static_configs:
      - targets: ["localhost:8080"]
```

Inside a container, `localhost` refers to that container. The Compose setup uses `proxy:8080`. When TLS is enabled, scrape the HTTPS listener with `scheme: https` and the appropriate certificate trust settings.

## Troubleshooting

- **Cannot read `config.yaml`:** pass `--config /path/to/config.yaml`, or place the configuration in the binary's working directory.
- **Port already in use:** stop the conflicting process, change `port` locally, or change the published host port in Compose. Local and Docker workflows both use `8080` by default.
- **`502` response:** check that the URL matches a route and that upstream URLs are reachable. Inspect proxy logs for forwarding errors.
- **`503` response:** inspect `/healthz` for unhealthy backends or open circuits. Verify upstream `/health` responses and the configured probe path.
- **`429` response:** check global and route rate limits; the demo's `/limited` route intentionally rejects excess traffic.
- **Empty dashboard:** check `docker compose ps` and `docker compose logs proxy prometheus traffic-generator`, then allow time for scrapes and traffic to accumulate.
- **Go build fails:** check `go version` against [go.mod](go.mod) and ensure dependencies can be downloaded. The Dockerfile pins its own compatible patch version.

## Development and contributions

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, project structure, coding conventions, regression testing, and pull request guidance.

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

## Dependency maintenance

Repository configuration assigns Go modules and GitHub Actions to [Dependabot](.github/dependabot.yml), scheduled weekly on Monday at 09:00 Asia/Kolkata. [Renovate](.github/renovate.json) handles Dockerfile and Compose images, excludes the locally built demo image, enables a Dependency Dashboard, and requires dashboard approval for major image upgrades. Renovate automerge is disabled in its configuration.

These files describe intended automation. The hosted Renovate app still requires repository access, and bot installation, repository security settings, and current remote CI results are not verified by a local checkout review. The [CI workflow](.github/workflows/ci.yml) verifies modules, builds, vets, runs race tests, validates Compose, and builds the image; it does not start the full demo stack.

## License

Released under the [MIT License](LICENSE).

# Repository Guidelines

## Project Structure & Module Organization

`main.go` wires configuration, HTTP/HTTPS listeners, middleware, metrics, and graceful shutdown. Packages separate responsibilities: `config/` loads and validates YAML; `router/` matches routes and rewrites paths; `proxy/` forwards requests and manages retries; `loadbalancer/`, `circuitbreaker/`, `healthcheck/`, and `ratelimit/` handle backend resilience. `metrics/` exposes Prometheus metrics.

Tests live beside implementation files as `*_test.go`, including application integration and TLS tests at the repository root. `config.yaml` configures local execution; `demo/config.yaml` configures Docker Compose. Monitoring assets live under `prometheus/` and `grafana/`.

## Build, Test, and Development Commands

Use the Go version declared in `go.mod` (currently `1.27.1`). Run commands from the repository root:

- `go build .`: compile the proxy binary.
- `go run .`: start the proxy using root `config.yaml`; start configured upstreams separately.
- `go test -v ./...`: run all package and integration tests.
- `go test -cover ./...`: report test coverage.
- `go test -race ./...`: check concurrent code for data races.
- `go vet ./...`: run Go's static checks.
- `docker compose up --build`: start the proxy, mock backends, Prometheus, Grafana, and traffic generator.
- `docker compose down`: stop the demo stack.

## Coding Style & Naming Conventions

Format changed Go files with `gofmt -w <files>`; use standard Go tab indentation and import grouping. Keep package names lowercase, exported identifiers in PascalCase, and internal identifiers in camelCase. Match existing snake_case YAML keys. Keep changes within the responsible package and document exported APIs. No custom formatter or linter configuration is checked in.

## Testing Guidelines

Use Go's standard `testing` package and `net/http/httptest` for HTTP behavior. Name tests `Test<Behavior>` in `*_test.go`; use table-driven subtests for configuration and routing cases. Add regression coverage for retries, cancellation, backend failover, and concurrency when changing those behaviors. Use temporary directories for fixtures. No numeric coverage threshold is configured.

## Commit & Pull Request Guidelines

Recent commits use short, imperative subjects with a `feat:` prefix, such as `feat: add prometheus metrics endpoint`. Follow that pattern and use an appropriate prefix for other changes. Keep commits focused.

PRs should explain the behavior change, link relevant issues, and list validation commands and results. Update README examples when configuration or public behavior changes; include screenshots for dashboard changes.

## Security & Configuration

Keep TLS private keys and certificate caches out of commits. Treat Compose's Grafana credentials as demo-only. Enable write retries only when upstreams enforce shared idempotency; keep configuration examples consistent with validation defaults.

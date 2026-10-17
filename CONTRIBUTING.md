# Contributing

Contributions to the reverse proxy are welcome: bug reports, documentation improvements, regression tests, and focused feature changes.

Read [README.md](README.md) for usage and configuration, and [AGENTS.md](AGENTS.md) for repository conventions before changing code.

## Contents

- [Set up your environment](#set-up-your-environment)
- [Find the relevant code](#find-the-relevant-code)
- [Make a focused change](#make-a-focused-change)
- [Test and validate](#test-and-validate)
- [Commit and open a pull request](#commit-and-open-a-pull-request)
- [Report a bug or propose a feature](#report-a-bug-or-propose-a-feature)
- [Handle sensitive information](#handle-sensitive-information)

## Set up your environment

Install Git and **Go 1.27.1**, matching [go.mod](go.mod). Docker with the Compose plugin is optional for the monitoring demo.

For an external contribution, fork the repository on GitHub, then clone your fork and add the original repository as `upstream`. Replace `YOUR_USERNAME` below:

```bash
git clone https://github.com/YOUR_USERNAME/reverse_proxy.git
cd reverse_proxy
git remote add upstream https://github.com/aditya-rai-5/reverse_proxy.git
git fetch upstream
git switch -c docs/improve-setup upstream/HEAD
go mod download
```

Choose a branch name that describes your work, such as `fix/retry-cancellation` or `feat/new-routing-option`. `upstream/HEAD` refers to the upstream default branch; if your Git installation has not set it, run `git remote set-head upstream -a` after fetching.

Check the starting state before making changes:

```bash
go build .
go test ./...
```

For local requests, start upstream services and edit root `config.yaml`, then run `go run .`. The application reads configuration from its current working directory. For a complete demo with mock services, use `docker compose up --build`; it uses `demo/config.yaml`. See the [README quick start](README.md#quick-start-docker-demo) for endpoints and credentials.

## Find the relevant code

- `main.go`: startup, listeners, middleware, health/metrics endpoints, and graceful shutdown.
- `config/`: YAML loading, validation, and defaults.
- `router/`: route matching, prefix stripping, rewriting, and retry eligibility.
- `proxy/`: upstream forwarding, body buffering, retries, cancellation, and timeouts.
- `loadbalancer/`: backend selection and connection tracking.
- `circuitbreaker/`: failure tracking and recovery state.
- `healthcheck/`: background HTTP probes.
- `ratelimit/`: per-IP token bucket limiting.
- `metrics/`: Prometheus instrumentation.
- Root `*_test.go`: application integration, TLS, and compatibility tests.
- `config.yaml`: local configuration; `demo/config.yaml`: Compose configuration.
- `docker-compose.yml`, `Dockerfile`, `prometheus/`, and `grafana/`: demo stack and monitoring assets.

Tests live next to the implementation they cover. Read the relevant implementation and existing tests before editing.

## Make a focused change

1. Reproduce the problem or describe the intended new behavior. For a bug, identify the triggering input and expected result.
2. Keep the patch within the responsible package where practical. Keep unrelated fixes in separate branches and pull requests.
3. Add regression coverage for changed behavior, including failure cases and compatibility with existing configuration.
4. Update documentation and examples when configuration, public behavior, or operational steps change.

Use standard Go formatting, tab indentation, and import grouping. Package names are lowercase, exported identifiers use PascalCase, and internal identifiers use camelCase. Document exported APIs and follow existing snake_case YAML keys. No custom formatter or linter configuration is checked in.

Format only the Go files you changed, for example:

```bash
gofmt -w proxy/proxy.go proxy/proxy_test.go
```

For configuration changes, review defaults, validation, routing/runtime behavior, `/healthz`, metrics, and both configuration examples wherever they share the affected contract. Preserve the distinction between omitted `retries` and explicit `retries: 0`.

Write retries require explicit method opt-in, an `Idempotency-Key`, and shared backend deduplication. Keep tests and examples consistent with that safety contract. Keep TLS private keys and certificate caches out of the patch.

## Test and validate

Use Go's standard `testing` package and `net/http/httptest` for HTTP behavior. Name tests `Test<Behavior>` and use table-driven subtests for configuration and routing cases. Use temporary directories for fixtures.

For a bug fix, add a test that reproduces the original failure and passes with the fix. Cover retries, cancellation, backend failover, and concurrency when your change affects those paths. Prefer local test servers or controlled transports over external network services.

While iterating, run the relevant package or test:

```bash
go test ./proxy
go test -run TestName -count=1 ./proxy
```

Replace `TestName` and the package path with your actual test. Before opening a pull request, run:

```bash
go build .
go test -v ./...
go test -race ./...
go vet ./...
git diff --check
```

Use a race-detector-supported platform with a working C compiler when running `go test -race`. For coverage reporting:

```bash
go test -cover ./...
```

No numeric coverage threshold is configured. Focus on meaningful behavior and failure cases. Some integration tests create local listeners; if your environment blocks sockets, report that restriction separately from a product failure.

For Docker or monitoring changes, validate the Compose configuration and exercise the stack:

```bash
docker compose config --quiet
docker compose up --build
```

Check the relevant routes, `/healthz`, `/metrics`, and dashboard panels, then stop with `docker compose down`. A successful Compose configuration check or image build does not verify runtime behavior. Include screenshots when changing dashboards.

For documentation changes, check that commands match the implementation, configuration examples load successfully, and relative links and heading anchors resolve in rendered Markdown. Documentation-only changes do not normally require the full Go test suite.

## Commit and open a pull request

Use a short, imperative commit subject with an appropriate prefix:

```text
fix: stop retries after client cancellation
docs: clarify local setup
feat: add a routing option
```

Review your diff and stage the intended files explicitly. For example, for a README change:

```bash
git diff --check
git diff -- README.md
git add README.md
git commit -m "docs: clarify local setup"
git push -u origin HEAD
```

Open a pull request from your branch to the upstream default branch. Include:

- The problem and resulting behavior, with a small before/after example if helpful.
- Related issue links, using `Fixes #123` only when the PR resolves that issue.
- The scope of the change and any compatibility implications.
- Validation commands and actual results, including anything you could not run.
- Updated configuration examples or dashboard screenshots where relevant.

A concise description can use this structure:

```markdown
## Change
Explain the problem and what users will observe after the change.

## Validation
- `go test ./...` — passed
- `go test -race ./...` — passed

Fixes #123
```

Replace the example results and issue number with your own evidence. Respond to review feedback with focused updates, and rerun checks affected by those updates.

## Report a bug or propose a feature

Before opening an issue, search existing issues and pull requests for the same topic.

For bugs, include the commit or version, OS, Go version, minimal sanitized configuration, exact request or reproduction steps, expected behavior, actual response, and relevant logs. For Docker problems, include `docker compose version` and the affected service logs.

For features, explain the use case, proposed behavior, configuration impact, and alternatives considered. Discuss larger changes before investing in an implementation.

## Handle sensitive information

Remove credentials, tokens, private keys, and private backend details from reports, logs, and screenshots. Do not commit certificate caches. Grafana's bundled `admin / demo` credentials are only for the demo.

For a suspected security vulnerability, use GitHub's private vulnerability reporting option if it is enabled for the repository. Otherwise, contact a maintainer privately through an available contact channel before publishing exploit details.

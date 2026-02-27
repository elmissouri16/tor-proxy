# Repository Guidelines

## Project Structure & Module Organization
This repository is a small Go service centered on `main.go`, which implements HTTP handlers, Tor proxy pool management, and server startup. Infrastructure files live at the root:
- `Dockerfile`, `docker-compose.yml`, `torrc`
- helper scripts: `test-endpoints.sh`, `benchmark.sh`
- automation: `Makefile`

If adding complexity, split logic into focused files/packages (for example `internal/proxy/`, `internal/httpapi/`) instead of growing `main.go`.

## Build, Test, and Development Commands
- `go build -o tor-proxy .`: build local binary.
- `go run .`: run locally without producing an artifact.
- `make setup`: build and start containerized service (`docker compose build && up -d`).
- `make logs` / `make stop` / `make clean`: inspect, stop, and clean Docker environment.
- `./test-endpoints.sh`: smoke-test public endpoints on `localhost:8080`.
- `./benchmark.sh`: run simple latency/concurrency checks against proxy endpoints.

## Coding Style & Naming Conventions
Use standard Go formatting and idioms.
- Run `gofmt -w .` before committing.
- Run `go vet ./...` for basic static checks.
- Use `CamelCase` for exported identifiers, `mixedCase` for internal helpers.
- Keep handler names explicit and consistent (for example `HealthCheckHandler`, `ProxyHandler`).
- Prefer small functions and clear error messages with context (`fmt.Errorf("...: %w", err)`).

## Testing Guidelines
Current coverage is script-based (endpoint and benchmark scripts). For new logic, add Go unit tests in `*_test.go` files and run:
- `go test ./...`

Name tests by behavior (`TestHTTPProxyHandler_InvalidURL_Returns400`). For endpoint changes, include at least one reproducible `curl` example in PR notes.

## Commit & Pull Request Guidelines
History favors concise, imperative commit subjects (examples: `refactor ...`, `Enhance ...`). Follow this pattern:
- Subject line under 72 chars, imperative mood.
- Group related changes per commit.

PRs should include:
- clear summary of behavior changes,
- linked issue (if available),
- commands run (`go test ./...`, script checks),
- sample request/response for API-impacting changes.

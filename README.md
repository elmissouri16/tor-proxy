# Tor Load Balancer

HTTP + SOCKS5 proxy service that routes traffic through a pool of Tor exits.

## What It Does

- Starts **10 independent Tor processes** (default), each with its own state.
- Exposes:
  - HTTP API on `:8080`
  - SOCKS5 proxy on container `:9050`
- Load-balances requests/connections across the Tor pool for IP rotation.

## Quick Start (Docker)

```bash
make setup
```

By default, host ports are:
- `8080` -> HTTP API
- `19050` -> SOCKS5 proxy (mapped to container `9050`)

Health check:
```bash
curl http://localhost:8080/health
```

SOCKS5 test:
```bash
curl --socks5-hostname 127.0.0.1:19050 https://httpbin.org/ip
```

Use a different host SOCKS port:
```bash
SOCKS5_HOST_PORT=29050 make setup
```

## Arcane Compatibility

- This repo includes `compose.yaml` for Arcane Projects.
- `compose.yaml` uses a pullable base image (`golang:1.24-alpine`) so Arcane can start the project even when deployment paths do not run image builds first.
- The service runs source from the project directory (`/app`) and installs runtime deps on startup.
- First startup is slower (package install + module download); subsequent restarts are faster due to cache volumes.

## API Endpoints

- `GET /` service info
- `GET /health` health status
- `GET /proxy?url=<target_url>` fetch URL through Tor
- `ALL /http-proxy` HTTP proxy endpoint (supports CONNECT)
- `GET /socks5-proxy` returns SOCKS5 connection details
- `GET /proxy-address` returns HTTP + SOCKS5 usage info

## Development

Build and run locally:
```bash
go build -o tor-proxy .
./tor-proxy
```

Format and static checks:
```bash
gofmt -w .
go vet ./...
```

Note: local run listens on `:9050` for SOCKS5. If another local Tor daemon already uses `9050`, stop it first.

## Makefile Commands

- `make setup` build + start containers
- `make logs` follow logs
- `make stop` stop containers
- `make clean` remove containers/networks/volumes
- `make test` run endpoint smoke tests (`test-endpoints.sh`)
- `make benchmark` run benchmark script (`benchmark.sh`)

## Testing & Rotation Notes

- Quick smoke test:
  ```bash
  make test
  ```
- Benchmark:
  ```bash
  make benchmark
  ```
- Tor does not guarantee every request gets a unique exit IP; it provides probabilistic rotation.

# Tor Load Balancer

This project provides a Tor load balancer that allows you to route HTTP requests through multiple Tor proxies for anonymity and IP rotation.

## Features

- **Load Balancing**: Automatically rotates requests through multiple Tor proxies
- **Anonymity**: Each request uses a different Tor circuit
- **Easy Integration**: Simple HTTP API for routing requests
- **Direct Proxy Access**: SOCKS5 proxy available for direct application integration
- **Containerized**: Easy deployment with Docker

## Architecture

```
+-------------------------------------+
|        Tor Load Balancer            |
|                                     |
|  +----------------------------+     |
|  |     HTTP Endpoints         |     |
|  +----------------------------+     |
|                                     |
|  +----------------------------+     |
|  |    Load Balancer with      |     |
|  |    Tor Proxy Rotation      |     |
|  +----------------------------+     |
|                                     |
|  +----------------------------+     |
|  |    Tornado Tor Pool        |     |
|  |  (10 Tor proxies managed   |     |
|  |   by Tornado library)      |     |
|  +----------------------------+     |
+-------------------------------------+
```

## Quick Start

### Using Docker (Recommended)

1. Build and run the service:
   ```bash
   # Using Makefile (recommended)
   make setup
   
   # Or using docker compose directly
   docker compose up -d
   ```

2. Test the service:
   ```bash
   curl "http://localhost:8080/proxy?url=https://httpbin.org/ip"
   ```

### Building from Source

1. Install dependencies:
   ```bash
   # On Ubuntu/Debian
   sudo apt-get install tor
   
   # On macOS with Homebrew
   brew install tor
   ```

2. Ensure Tor service is not running as a daemon:
   ```bash
   # On Ubuntu/Debian
   sudo systemctl stop tor
   
   # On macOS
   brew services stop tor
   ```

3. Build and run:
   ```bash
   go build -o tor-proxy .
   ./tor-proxy
   ```

## API Endpoints

- `GET /` - Service information
- `GET /health` - Health check
- `GET /proxy?url=<target_url>` - Route request through Tor proxy
- `GET /http-proxy` - HTTP proxy endpoint
- `GET /socks5-proxy` - Get SOCKS5 proxy address for direct use

## Usage Examples

### Route individual requests through Tor
```bash
curl "http://localhost:8080/proxy?url=https://check.torproject.org/api/ip"
```

### Use as HTTP proxy endpoint
```bash
# This endpoint proxies requests through Tor directly
curl http://localhost:8080/http-proxy
```

### Get SOCKS5 proxy address for direct application use
```bash
# Returns the SOCKS5 proxy address as plain text
curl http://localhost:8080/socks5-proxy
# Output: 
# SOCKS5 Proxy Address: socks5://127.0.0.1:9050
# 
# To use this SOCKS5 proxy directly:
# - Configure your application to use SOCKS5 proxy at 127.0.0.1:9050
# - With curl: curl --socks5 127.0.0.1:9050 http://example.com
# - With wget: wget --socks-proxy=127.0.0.1:9050 http://example.com
```

### Use SOCKS5 proxy directly with applications
```bash
# Use the SOCKS5 proxy with curl
curl --socks5 127.0.0.1:9050 http://httpbin.org/ip

# Set environment variables for applications that support HTTP_PROXY
export http_proxy=socks5://127.0.0.1:9050
export https_proxy=socks5://127.0.0.1:9050
```

## Docker Management

The project includes a Makefile for easy Docker management:

```bash
# Build and run the service
make setup

# View logs
make logs

# Stop the service
make stop

# Clean up containers
make clean

# Access container shell
make shell

# Run tests
make test

# Run benchmarks
make benchmark
```

## How It Works

1. When the service starts, it creates a pool of 10 Tor proxies using the Tornado library
2. Each Tor proxy establishes its own circuit for IP anonymity
3. Requests to the `/proxy` endpoint are automatically routed through different Tor proxies
4. The load balancer randomly selects a healthy Tor proxy for each request
5. Tornado handles proxy health checks automatically
6. A SOCKS5 proxy is available on port 9050 for direct application integration

## Configuration

### Number of Tor Proxies

The service uses 10 Tor proxies by default. This provides a good balance between anonymity and resource usage.

### Ports

- **8080**: HTTP API service
- **9050**: SOCKS5 proxy service

## Testing

Run the built-in test suite:
```bash
make test
```

Run performance benchmarks:
```bash
make benchmark
```
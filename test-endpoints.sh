#!/bin/bash

echo "=== Testing Tor Proxy Endpoints ==="
echo

# Test 1: Root endpoint
echo "1. Testing root endpoint (/)"
response=$(curl -s http://localhost:8080/)
echo "Response: $response"
echo

# Test 2: Health endpoint
echo "2. Testing health endpoint (/health)"
response=$(curl -s http://localhost:8080/health)
echo "Response: $response"
echo

# Test 3: Proxy endpoint
echo "3. Testing proxy endpoint (/proxy)"
response=$(curl -s "http://localhost:8080/proxy?url=http://httpbin.org/ip")
echo "Response: $response"
echo

# Test 4: HTTP proxy endpoint
echo "4. Testing HTTP proxy endpoint (/http-proxy)"
response=$(curl -s http://localhost:8080/http-proxy)
echo "Response: $response"
echo

# Test 5: SOCKS5 proxy address endpoint
echo "5. Testing SOCKS5 proxy address endpoint (/socks5-proxy)"
response=$(curl -s http://localhost:8080/socks5-proxy)
echo "Response:"
echo "$response"
echo

# Test 6: Direct SOCKS5 proxy usage (if curl supports it)
echo "6. Testing direct SOCKS5 proxy usage"
response=$(curl -s --socks5 127.0.0.1:9050 http://httpbin.org/ip 2>/dev/null || echo "SOCKS5 test failed or curl doesn't support SOCKS5")
echo "Response: $response"
echo

echo "=== Test Complete ==="
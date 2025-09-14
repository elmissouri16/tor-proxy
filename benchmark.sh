#!/bin/bash

echo "=== Tor Proxy Benchmark Test ==="
echo

# Configuration
TEST_URL="http://httpbin.org/ip"
REQUESTS=30
CONCURRENT_CONNECTIONS=10

# Function to measure response time
measure_time() {
    local url=$1
    local name=$2
    echo "Testing $name..."
    
    # Arrays to store results
    declare -a times
    declare -a successes
    declare -a failures
    
    # Run requests
    for i in $(seq 1 $REQUESTS); do
        # Measure time for each request
        start_time=$(date +%s%3N)
        response=$(curl -s --max-time 30 "$url" 2>/dev/null)
        end_time=$(date +%s%3N)
        
        # Calculate response time in milliseconds
        response_time=$((end_time - start_time))
        
        # Check if request was successful
        if [ -n "$response" ] && [[ $response == *"origin"* ]]; then
            successes+=("$response_time")
            echo "  Request $i: $(printf "%.2f" $(echo "$response_time/1000" | bc -l))s - Success"
        else
            failures+=("$response_time")
            echo "  Request $i: $(printf "%.2f" $(echo "$response_time/1000" | bc -l))s - Failed"
        fi
        
        times+=("$response_time")
        
        # Small delay to avoid overwhelming
        sleep 0.1
    done
    
    # Calculate statistics
    total=${#times[@]}
    success_count=${#successes[@]}
    failure_count=${#failures[@]}
    
    # Calculate average response time
    if [ $total -gt 0 ]; then
        sum=0
        for time in "${times[@]}"; do
            sum=$((sum + time))
        done
        avg_time=$(echo "scale=2; $sum / $total" | bc -l)
        avg_seconds=$(echo "scale=2; $avg_time / 1000" | bc -l)
    else
        avg_seconds="N/A"
    fi
    
    # Calculate success rate
    if [ $total -gt 0 ]; then
        success_rate=$(echo "scale=2; $success_count * 100 / $total" | bc -l)
    else
        success_rate="0"
    fi
    
    echo "  Total requests: $total"
    echo "  Successful: $success_count"
    echo "  Failed: $failure_count"
    echo "  Success rate: ${success_rate}%"
    echo "  Average response time: ${avg_seconds}s"
    echo
}

# Function to run concurrent requests
concurrent_test() {
    local url=$1
    local name=$2
    echo "Concurrent test for $name ($CONCURRENT_CONNECTIONS connections)..."
    
    start_time=$(date +%s%3N)
    
    # Run concurrent requests
    for i in $(seq 1 $CONCURRENT_CONNECTIONS); do
        curl -s --max-time 30 "$url" > /dev/null 2>&1 &
    done
    
    # Wait for all requests to complete
    wait
    
    end_time=$(date +%s%3N)
    total_time=$((end_time - start_time))
    avg_time_per_request=$(echo "scale=2; $total_time / $CONCURRENT_CONNECTIONS" | bc -l)
    total_seconds=$(echo "scale=2; $total_time / 1000" | bc -l)
    avg_seconds=$(echo "scale=2; $avg_time_per_request / 1000" | bc -l)
    
    echo "  Total time for $CONCURRENT_CONNECTIONS concurrent requests: ${total_seconds}s"
    echo "  Average time per request: ${avg_seconds}s"
    echo
}

# Install bc if not available
if ! command -v bc &> /dev/null; then
    echo "Installing bc for calculations..."
    if command -v apt-get &> /dev/null; then
        sudo apt-get update && sudo apt-get install -y bc
    elif command -v yum &> /dev/null; then
        sudo yum install -y bc
    elif command -v brew &> /dev/null; then
        brew install bc
    else
        echo "Please install bc manually for calculations"
        exit 1
    fi
    echo
fi

# Test 1: Direct proxy endpoint
measure_time "http://localhost:8080/proxy?url=$TEST_URL" "Direct Proxy Endpoint (/proxy)"

# Test 2: HTTP proxy endpoint
measure_time "http://localhost:8080/http-proxy" "HTTP Proxy Endpoint (/http-proxy)"

# Test 3: Direct SOCKS5 proxy (if curl supports it)
echo "Testing Direct SOCKS5 Proxy..."
success_count=0
failure_count=0
declare -a socks_times

for i in $(seq 1 5); do
    start_time=$(date +%s%3N)
    response=$(curl -s --max-time 30 --socks5 127.0.0.1:9050 "$TEST_URL" 2>/dev/null)
    end_time=$(date +%s%3N)
    
    response_time=$((end_time - start_time))
    socks_times+=("$response_time")
    
    if [ -n "$response" ] && [[ $response == *"origin"* ]]; then
        success_count=$((success_count + 1))
        echo "  Request $i: $(printf "%.2f" $(echo "$response_time/1000" | bc -l))s - Success"
    else
        failure_count=$((failure_count + 1))
        echo "  Request $i: $(printf "%.2f" $(echo "$response_time/1000" | bc -l))s - Failed"
    fi
    
    sleep 0.1
done

# Calculate SOCKS5 statistics
total_socks=${#socks_times[@]}
if [ $total_socks -gt 0 ]; then
    sum=0
    for time in "${socks_times[@]}"; do
        sum=$((sum + time))
    done
    avg_time=$(echo "scale=2; $sum / $total_socks" | bc -l)
    avg_seconds=$(echo "scale=2; $avg_time / 1000" | bc -l)
    success_rate=$(echo "scale=2; $success_count * 100 / $total_socks" | bc -l)
else
    avg_seconds="N/A"
    success_rate="0"
fi

echo "  Total requests: $total_socks"
echo "  Successful: $success_count"
echo "  Failed: $failure_count"
echo "  Success rate: ${success_rate}%"
echo "  Average response time: ${avg_seconds}s"
echo

# Concurrent tests
echo "=== Concurrent Tests ==="
echo

concurrent_test "http://localhost:8080/proxy?url=$TEST_URL" "Direct Proxy Endpoint"
concurrent_test "http://localhost:8080/http-proxy" "HTTP Proxy Endpoint"

echo "Concurrent test for SOCKS5 Proxy..."
start_time=$(date +%s%3N)

# Run concurrent SOCKS5 requests
for i in $(seq 1 $CONCURRENT_CONNECTIONS); do
    curl -s --max-time 30 --socks5 127.0.0.1:9050 "$TEST_URL" > /dev/null 2>&1 &
done

# Wait for all requests to complete
wait

end_time=$(date +%s%3N)
total_time=$((end_time - start_time))
avg_time_per_request=$(echo "scale=2; $total_time / $CONCURRENT_CONNECTIONS" | bc -l)
total_seconds=$(echo "scale=2; $total_time / 1000" | bc -l)
avg_seconds=$(echo "scale=2; $avg_time_per_request / 1000" | bc -l)

echo "  Total time for $CONCURRENT_CONNECTIONS concurrent requests: ${total_seconds}s"
echo "  Average time per request: ${avg_seconds}s"
echo

echo "=== Benchmark Complete ==="
echo
echo "Summary:"
echo "- Direct Proxy Endpoint (/proxy): Routes individual requests through Tor"
echo "- HTTP Proxy Endpoint (/http-proxy): HTTP endpoint that proxies requests through Tor"
echo "- SOCKS5 Proxy (127.0.0.1:9050): Direct SOCKS5 proxy connection"
echo
echo "Note: Tor network speeds vary based on:"
echo "- Current Tor network congestion"
echo "- Path selection through the Tor network"
echo "- Exit node performance"
echo "- Number of hops in the circuit"
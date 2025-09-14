package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xorcare/tornado"
)

// Configuration settings
const (
	// Number of Tor proxies to maintain in the pool
	ProxyPoolSize = 10

	// Server port to listen on
	ServerPort = ":8080"

	// HTTP client timeout for requests
	HTTPClientTimeout = 30 * time.Second
)

// LoadBalancer distributes requests among Tor proxies
type LoadBalancer struct {
	proxyPool *tornado.Pool
}

// NewLoadBalancer creates a new load balancer with Tor proxies
func NewLoadBalancer(ctx context.Context, poolSize int) (*LoadBalancer, error) {
	// Create a pool of Tor proxies using Tornado
	pool, err := tornado.NewPool(ctx, poolSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create Tor proxy pool: %w", err)
	}

	return &LoadBalancer{
		proxyPool: pool,
	}, nil
}

// getHealthyProxy randomly selects a healthy proxy from the pool
func (lb *LoadBalancer) getHealthyProxy() (*tornado.Proxy, error) {
	// Get a proxy from the pool (Tornado handles health checks internally)
	proxy := lb.proxyPool.Get()
	return proxy, nil
}

// ProxyHandler handles incoming requests and forwards them through a Tor proxy
func (lb *LoadBalancer) ProxyHandler(w http.ResponseWriter, r *http.Request) {
	// Get a healthy proxy from the pool
	proxy, err := lb.getHealthyProxy()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get healthy proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer lb.proxyPool.Put(proxy)

	// Create HTTP client with Tor proxy
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: proxy.DialContext,
		},
		Timeout: HTTPClientTimeout,
	}

	// Extract target URL from query parameters
	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		http.Error(w, "Missing 'url' query parameter", http.StatusBadRequest)
		return
	}

	// Make the request through Tor
	resp, err := client.Get(targetURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to make request through Tor: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy headers
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	// Set response status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("Error copying response body: %v", err)
	}
}

// HTTPProxyHandler acts as a simple HTTP proxy endpoint
func (lb *LoadBalancer) HTTPProxyHandler(w http.ResponseWriter, r *http.Request) {
	// Get a healthy proxy from the pool
	torProxy, err := lb.getHealthyProxy()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get healthy proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer lb.proxyPool.Put(torProxy)

	// Create HTTP client with Tor proxy
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: torProxy.DialContext,
		},
		Timeout: HTTPClientTimeout,
	}

	// Make a request to httpbin.org/ip to show the Tor IP
	resp, err := client.Get("http://httpbin.org/ip")
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to make request through Tor: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// SOCKS5ProxyAddressHandler returns the SOCKS5 proxy address with usage instructions
func (lb *LoadBalancer) SOCKS5ProxyAddressHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintln(w, "SOCKS5 Proxy Address: socks5://127.0.0.1:9050")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "To use this SOCKS5 proxy directly:")
	fmt.Fprintln(w, "- Configure your application to use SOCKS5 proxy at 127.0.0.1:9050")
	fmt.Fprintln(w, "- With curl: curl --socks5 127.0.0.1:9050 http://example.com")
	fmt.Fprintln(w, "- With wget: wget --socks-proxy=127.0.0.1:9050 http://example.com")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Note: This HTTP endpoint provides information about the SOCKS5 proxy.")
	fmt.Fprintln(w, "The actual SOCKS5 proxy service is running on port 9050.")
}

// HealthCheckHandler handles health check requests
func (lb *LoadBalancer) HealthCheckHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"status":  "OK",
		"service": "Tor Load Balancer",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// ProxyAddressHandler returns the SOCKS5 proxy address that can be used with other libraries
func (lb *LoadBalancer) ProxyAddressHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"proxy":       "socks5://127.0.0.1:9050",
		"description": "SOCKS5 proxy address for direct use with other libraries",
		"usage":       "Configure your HTTP client or library to use this SOCKS5 proxy address",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// InfoHandler handles root requests
func (lb *LoadBalancer) InfoHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"message":     "Tor Load Balancer Service",
		"description": "A load balancer that distributes requests through Tor proxies for anonymity",
		"endpoints": []string{
			"GET / - Service information",
			"GET /health - Health check",
			"GET /proxy?url=<target_url> - Route request through Tor proxy",
			"GET /http-proxy - HTTP proxy endpoint",
			"GET /socks5-proxy - Get SOCKS5 proxy address for direct use",
		},
		"usage": "Add your target URL as the 'url' query parameter to route requests through Tor",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Close releases resources used by the load balancer
func (lb *LoadBalancer) Close() error {
	if lb.proxyPool != nil {
		lb.proxyPool.Close()
	}
	return nil
}

func main() {
	// Create context with cancel function
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down gracefully...")
		cancel()
	}()

	// Create load balancer with Tor proxy pool (using configured pool size)
	lb, err := NewLoadBalancer(ctx, ProxyPoolSize)
	if err != nil {
		log.Fatalf("Failed to create load balancer: %v", err)
	}
	defer lb.Close()

	// Set up HTTP routes
	http.HandleFunc("/", lb.InfoHandler)
	http.HandleFunc("/health", lb.HealthCheckHandler)
	http.HandleFunc("/proxy", lb.ProxyHandler)
	http.HandleFunc("/http-proxy", lb.HTTPProxyHandler)
	http.HandleFunc("/socks5-proxy", lb.SOCKS5ProxyAddressHandler)

	// Start server
	log.Printf("Tor Load Balancer starting on %s", ServerPort)
	log.Printf("Load balancer with Tor proxy rotation is ready (pool size: %d)", ProxyPoolSize)

	if err := http.ListenAndServe(ServerPort, nil); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed to start: %v", err)
	}

	log.Println("Server stopped")
}

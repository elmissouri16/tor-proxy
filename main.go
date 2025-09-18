package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
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

// HTTPProxyHandler acts as a proper HTTP proxy endpoint
func (lb *LoadBalancer) HTTPProxyHandler(w http.ResponseWriter, r *http.Request) {
	// Handle CONNECT method for HTTPS tunneling
	if r.Method == "CONNECT" {
		lb.handleConnect(w, r)
		return
	}

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

	// Handle different HTTP proxy request types
	var targetURL string
	var req *http.Request

	// Check if this is a proper HTTP proxy request (full URL in request line)
	if r.URL.IsAbs() {
		// Direct proxy request with absolute URL
		targetURL = r.URL.String()
	} else if host := r.Header.Get("Host"); host != "" {
		// Construct URL from Host header and request URI
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		targetURL = fmt.Sprintf("%s://%s%s", scheme, host, r.RequestURI)
	} else {
		// Fallback: check for URL in query parameter or path
		if urlParam := r.URL.Query().Get("url"); urlParam != "" {
			targetURL = urlParam
		} else {
			// Try to extract URL from path (removing leading slash)
			path := strings.TrimPrefix(r.URL.Path, "/http-proxy/")
			if path != "" && strings.HasPrefix(path, "http") {
				targetURL = path
			} else {
				http.Error(w, "Invalid proxy request: no target URL found", http.StatusBadRequest)
				return
			}
		}
	}

	// Parse and validate the target URL
	parsedURL, err := url.Parse(targetURL)
	if err != nil {
		http.Error(w, fmt.Sprintf("Invalid target URL: %v", err), http.StatusBadRequest)
		return
	}

	// Create new request to the target URL
	req, err = http.NewRequest(r.Method, targetURL, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create request: %v", err), http.StatusBadRequest)
		return
	}

	// Copy headers from original request (excluding hop-by-hop headers)
	hopByHopHeaders := map[string]bool{
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}

	for key, values := range r.Header {
		if !hopByHopHeaders[key] && key != "Host" {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	// Set the correct Host header
	req.Header.Set("Host", parsedURL.Host)

	// Make the request through Tor
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to make request through Tor: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers (excluding hop-by-hop headers)
	for key, values := range resp.Header {
		if !hopByHopHeaders[key] {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
	}

	// Set response status code
	w.WriteHeader(resp.StatusCode)

	// Copy response body
	_, err = io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("Error copying response body: %v", err)
	}

	log.Printf("Proxied %s request to %s through Tor", r.Method, targetURL)
}

// handleConnect handles CONNECT method for HTTPS tunneling
func (lb *LoadBalancer) handleConnect(w http.ResponseWriter, r *http.Request) {
	log.Printf("Handling CONNECT request to %s", r.Host)

	// Get a healthy proxy from the pool
	torProxy, err := lb.getHealthyProxy()
	if err != nil {
		log.Printf("Failed to get healthy proxy: %v", err)
		http.Error(w, fmt.Sprintf("Failed to get healthy proxy: %v", err), http.StatusBadGateway)
		return
	}
	defer lb.proxyPool.Put(torProxy)

	// Extract host and port from the CONNECT request
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}

	if host == "" {
		log.Printf("No host specified in CONNECT request")
		http.Error(w, "No host specified", http.StatusBadRequest)
		return
	}

	log.Printf("Establishing connection to %s through Tor", host)

	// Establish connection to target through Tor
	targetConn, err := torProxy.DialContext(r.Context(), "tcp", host)
	if err != nil {
		log.Printf("Failed to connect to %s through Tor: %v", host, err)
		http.Error(w, fmt.Sprintf("Failed to connect to target through Tor: %v", err), http.StatusBadGateway)
		return
	}
	defer targetConn.Close()

	// Get the underlying connection from the ResponseWriter
	hj, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("HTTP hijacking not supported")
		http.Error(w, "HTTP hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hj.Hijack()
	if err != nil {
		log.Printf("Failed to hijack connection: %v", err)
		http.Error(w, fmt.Sprintf("Failed to hijack connection: %v", err), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Send the 200 OK response manually since we hijacked the connection
	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	if err != nil {
		log.Printf("Failed to write CONNECT response: %v", err)
		return
	}

	log.Printf("CONNECT tunnel established to %s through Tor", host)

	// Create channels for bidirectional data transfer
	errChan := make(chan error, 2)

	// Copy data from client to target
	go func() {
		_, err := io.Copy(targetConn, clientConn)
		errChan <- err
	}()

	// Copy data from target to client
	go func() {
		_, err := io.Copy(clientConn, targetConn)
		errChan <- err
	}()

	// Wait for one direction to complete (or error)
	err = <-errChan
	if err != nil {
		log.Printf("Tunnel error for %s: %v", host, err)
	} else {
		log.Printf("Tunnel closed for %s", host)
	}
}

// SOCKS5ProxyAddressHandler returns the SOCKS5 proxy address with usage instructions
func (lb *LoadBalancer) SOCKS5ProxyAddressHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"proxy":       "socks5://127.0.0.1:9050",
		"host":        "127.0.0.1",
		"port":        9050,
		"protocol":    "socks5",
		"description": "SOCKS5 proxy address for direct use",
		"usage_examples": []string{
			"curl --socks5 127.0.0.1:9050 http://example.com",
			"wget --socks-proxy=127.0.0.1:9050 http://example.com",
		},
		"note": "This HTTP endpoint provides information about the SOCKS5 proxy. The actual SOCKS5 proxy service is running on port 9050.",
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, fmt.Sprintf("failed to encode JSON response: %v", err), http.StatusInternalServerError)
		return
	}
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

// ProxyAddressHandler returns the HTTP proxy address that can be used with other libraries
func (lb *LoadBalancer) ProxyAddressHandler(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"http_proxy":   fmt.Sprintf("http://localhost%s/http-proxy", ServerPort),
		"socks5_proxy": "socks5://127.0.0.1:9050",
		"description":  "HTTP and SOCKS5 proxy addresses for use with other libraries",
		"http_proxy_usage": []string{
			fmt.Sprintf("curl --proxy http://localhost%s/http-proxy http://example.com", ServerPort),
			fmt.Sprintf("HTTP_PROXY=http://localhost%s/http-proxy curl http://example.com", ServerPort),
			"Configure your HTTP client to use the http_proxy address",
		},
		"socks5_usage": []string{
			"curl --socks5 127.0.0.1:9050 http://example.com",
			"HTTPS_PROXY=socks5://127.0.0.1:9050 curl https://example.com",
		},
		"note": "The HTTP proxy endpoint can handle various request formats for maximum compatibility",
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
			"ALL /http-proxy - HTTP proxy endpoint (supports multiple request formats)",
			"GET /socks5-proxy - Get SOCKS5 proxy address for direct use",
			"GET /proxy-address - Get both HTTP and SOCKS5 proxy addresses with usage examples",
		},
		"http_proxy_usage": []string{
			fmt.Sprintf("curl --proxy http://localhost%s/http-proxy http://example.com", ServerPort),
			fmt.Sprintf("curl http://localhost%s/http-proxy/http://example.com", ServerPort),
			fmt.Sprintf("curl http://localhost%s/http-proxy?url=http://example.com", ServerPort),
		},
		"usage": "The /http-proxy endpoint can be used as a standard HTTP proxy with clients",
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

// ProxyServer wraps the LoadBalancer and handles HTTP proxy requests
type ProxyServer struct {
	loadBalancer *LoadBalancer
	mux          *http.ServeMux
}

// NewProxyServer creates a new proxy server
func NewProxyServer(lb *LoadBalancer) *ProxyServer {
	mux := http.NewServeMux()
	ps := &ProxyServer{
		loadBalancer: lb,
		mux:          mux,
	}

	// Set up routes
	mux.HandleFunc("/", lb.InfoHandler)
	mux.HandleFunc("/health", lb.HealthCheckHandler)
	mux.HandleFunc("/proxy", lb.ProxyHandler)
	mux.HandleFunc("/socks5-proxy", lb.SOCKS5ProxyAddressHandler)
	mux.HandleFunc("/proxy-address", lb.ProxyAddressHandler)

	return ps
}

// ServeHTTP implements http.Handler interface
func (ps *ProxyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("Received %s request to %s", r.Method, r.URL.Path)

	// Handle CONNECT method for HTTPS tunneling at the top level
	if r.Method == "CONNECT" {
		log.Printf("Handling CONNECT method")
		ps.loadBalancer.handleConnect(w, r)
		return
	}

	// Handle HTTP proxy requests to /http-proxy path
	if strings.HasPrefix(r.URL.Path, "/http-proxy") {
		log.Printf("Handling HTTP proxy request")
		ps.loadBalancer.HTTPProxyHandler(w, r)
		return
	}

	// Handle all other routes through the mux
	log.Printf("Handling regular request through mux")
	ps.mux.ServeHTTP(w, r)
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

	// Create proxy server
	proxyServer := NewProxyServer(lb)

	// Create HTTP server
	server := &http.Server{
		Addr:    ServerPort,
		Handler: proxyServer,
	}

	// Start server
	log.Printf("Tor Load Balancer starting on %s", ServerPort)
	log.Printf("Load balancer with Tor proxy rotation is ready (pool size: %d)", ProxyPoolSize)
	log.Printf("HTTP Proxy available at: http://localhost%s/http-proxy", ServerPort)

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server failed to start: %v", err)
	}

	log.Println("Server stopped")
}

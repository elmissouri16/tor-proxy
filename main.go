package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xorcare/tornado"
)

// Configuration settings
const (
	// Number of Tor proxies to maintain in the pool
	ProxyPoolSize = 20

	// Server port to listen on
	ServerPort = ":8080"

	// SOCKS5 listen address for direct proxy usage
	SOCKS5ListenAddress = ":9050"

	// HTTP client timeout for requests
	HTTPClientTimeout = 30 * time.Second
)

var (
	// SOCKS5 address advertised to local clients (can differ from listen address in Docker).
	SOCKS5AdvertisedAddress = getenvOrDefault("SOCKS5_ADVERTISED_ADDRESS", "127.0.0.1:9050")
)

func getenvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// LoadBalancer distributes requests among Tor proxies
type LoadBalancer struct {
	proxyPool *TorProxyPool
}

// TorProxyPool manages multiple independent Tor proxy processes.
type TorProxyPool struct {
	ch       chan *tornado.Proxy
	proxies  []*tornado.Proxy
	closeMut sync.Mutex
	closed   bool
}

func NewTorProxyPool(ctx context.Context, size int) (*TorProxyPool, error) {
	if size <= 0 {
		return nil, fmt.Errorf("invalid pool size %d", size)
	}

	pool := &TorProxyPool{
		ch: make(chan *tornado.Proxy, size),
	}

	for i := 0; i < size; i++ {
		prx, err := tornado.NewProxy(ctx)
		if err != nil {
			_ = pool.Close()
			return nil, fmt.Errorf("failed to start Tor proxy %d/%d: %w", i+1, size, err)
		}

		pool.proxies = append(pool.proxies, prx)
		pool.ch <- prx
	}

	return pool, nil
}

func (p *TorProxyPool) Get() (*tornado.Proxy, error) {
	prx := <-p.ch
	if prx == nil {
		return nil, errors.New("tor proxy pool is closed")
	}
	return prx, nil
}

func (p *TorProxyPool) Put(prx *tornado.Proxy) {
	if prx == nil {
		return
	}

	p.closeMut.Lock()
	closed := p.closed
	p.closeMut.Unlock()
	if closed {
		_ = prx.Close()
		return
	}

	p.ch <- prx
}

func (p *TorProxyPool) Close() error {
	p.closeMut.Lock()
	if p.closed {
		p.closeMut.Unlock()
		return nil
	}
	p.closed = true
	proxies := append([]*tornado.Proxy(nil), p.proxies...)
	p.closeMut.Unlock()

	var closeErr error
	for _, prx := range proxies {
		if err := prx.Close(); err != nil && closeErr == nil {
			closeErr = err
		}
	}

	return closeErr
}

// pooledConn returns the proxy to the pool when the connection is closed.
type pooledConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (pc *pooledConn) Close() error {
	err := pc.Conn.Close()
	pc.once.Do(pc.release)
	return err
}

// NewLoadBalancer creates a new load balancer with Tor proxies
func NewLoadBalancer(ctx context.Context, poolSize int) (*LoadBalancer, error) {
	// Create a pool of independent Tor proxy processes.
	pool, err := NewTorProxyPool(ctx, poolSize)
	if err != nil {
		return nil, fmt.Errorf("failed to create Tor proxy pool: %w", err)
	}

	return &LoadBalancer{
		proxyPool: pool,
	}, nil
}

// getHealthyProxy acquires a proxy from the pool.
func (lb *LoadBalancer) getHealthyProxy() (*tornado.Proxy, error) {
	return lb.proxyPool.Get()
}

// dialThroughPool establishes a network connection through a Tor proxy from the pool.
func (lb *LoadBalancer) dialThroughPool(ctx context.Context, network, address string) (net.Conn, error) {
	torProxy, err := lb.getHealthyProxy()
	if err != nil {
		return nil, err
	}

	conn, err := torProxy.DialContext(ctx, network, address)
	if err != nil {
		lb.proxyPool.Put(torProxy)
		return nil, err
	}

	return &pooledConn{
		Conn: conn,
		release: func() {
			lb.proxyPool.Put(torProxy)
		},
	}, nil
}

// SOCKS5Server provides a SOCKS5 endpoint that routes connections through the Tor pool.
type SOCKS5Server struct {
	lb       *LoadBalancer
	listener net.Listener
	wg       sync.WaitGroup
}

func NewSOCKS5Server(addr string, lb *LoadBalancer) (*SOCKS5Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on SOCKS5 address %s: %w", addr, err)
	}

	return &SOCKS5Server{
		lb:       lb,
		listener: listener,
	}, nil
}

func (s *SOCKS5Server) Serve(errChan chan<- error) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			errChan <- fmt.Errorf("SOCKS5 accept failed: %w", err)
			return
		}

		s.wg.Add(1)
		go func(c net.Conn) {
			defer s.wg.Done()
			s.handleConnection(c)
		}(conn)
	}
}

func (s *SOCKS5Server) Shutdown() error {
	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	s.wg.Wait()
	return nil
}

func (s *SOCKS5Server) handleConnection(clientConn net.Conn) {
	defer clientConn.Close()

	targetAddr, err := negotiateSOCKS5(clientConn)
	if err != nil {
		log.Printf("SOCKS5 negotiation failed: %v", err)
		return
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), HTTPClientTimeout)
	defer cancel()

	targetConn, err := s.lb.dialThroughPool(dialCtx, "tcp", targetAddr)
	if err != nil {
		_ = writeSOCKS5Reply(clientConn, 0x04)
		log.Printf("SOCKS5 dial failed for %s: %v", targetAddr, err)
		return
	}
	defer targetConn.Close()

	if err := writeSOCKS5Reply(clientConn, 0x00); err != nil {
		log.Printf("SOCKS5 reply failed: %v", err)
		return
	}

	log.Printf("SOCKS5 tunnel established to %s through Tor pool", targetAddr)

	errChan := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(targetConn, clientConn)
		errChan <- copyErr
	}()
	go func() {
		_, copyErr := io.Copy(clientConn, targetConn)
		errChan <- copyErr
	}()

	if tunnelErr := <-errChan; tunnelErr != nil {
		log.Printf("SOCKS5 tunnel closed with error for %s: %v", targetAddr, tunnelErr)
	}
}

func negotiateSOCKS5(conn net.Conn) (string, error) {
	methodHeader := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodHeader); err != nil {
		return "", fmt.Errorf("failed to read SOCKS5 greeting: %w", err)
	}

	if methodHeader[0] != 0x05 {
		return "", fmt.Errorf("unsupported SOCKS version: %d", methodHeader[0])
	}

	methodCount := int(methodHeader[1])
	methods := make([]byte, methodCount)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("failed to read SOCKS5 auth methods: %w", err)
	}

	acceptNoAuth := false
	for _, method := range methods {
		if method == 0x00 {
			acceptNoAuth = true
			break
		}
	}
	if !acceptNoAuth {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return "", errors.New("no supported SOCKS5 auth method")
	}

	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", fmt.Errorf("failed to write SOCKS5 auth response: %w", err)
	}

	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return "", fmt.Errorf("failed to read SOCKS5 request header: %w", err)
	}
	if reqHeader[0] != 0x05 {
		return "", fmt.Errorf("invalid SOCKS5 request version: %d", reqHeader[0])
	}
	if reqHeader[1] != 0x01 {
		_ = writeSOCKS5Reply(conn, 0x07)
		return "", fmt.Errorf("unsupported SOCKS5 command: %d", reqHeader[1])
	}

	host, err := readSOCKS5Address(conn, reqHeader[3])
	if err != nil {
		_ = writeSOCKS5Reply(conn, 0x08)
		return "", err
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", fmt.Errorf("failed to read SOCKS5 destination port: %w", err)
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])

	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func readSOCKS5Address(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		ip := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", fmt.Errorf("failed to read IPv4 address: %w", err)
		}
		return net.IP(ip).String(), nil
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", fmt.Errorf("failed to read domain length: %w", err)
		}
		domain := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", fmt.Errorf("failed to read domain name: %w", err)
		}
		return string(domain), nil
	case 0x04:
		ip := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return "", fmt.Errorf("failed to read IPv6 address: %w", err)
		}
		return net.IP(ip).String(), nil
	default:
		return "", fmt.Errorf("unsupported address type: %d", atyp)
	}
}

func writeSOCKS5Reply(conn net.Conn, replyCode byte) error {
	_, err := conn.Write([]byte{0x05, replyCode, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	return err
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
	host, portStr, err := net.SplitHostPort(SOCKS5AdvertisedAddress)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid SOCKS5 address configuration: %v", err), http.StatusInternalServerError)
		return
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid SOCKS5 port configuration: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"proxy":       fmt.Sprintf("socks5://%s", SOCKS5AdvertisedAddress),
		"host":        host,
		"port":        port,
		"protocol":    "socks5",
		"description": "SOCKS5 endpoint that distributes connections across independent Tor instances",
		"usage_examples": []string{
			fmt.Sprintf("curl --socks5 %s http://example.com", SOCKS5AdvertisedAddress),
			fmt.Sprintf("wget --socks-proxy=%s http://example.com", SOCKS5AdvertisedAddress),
		},
		"note": "Each new SOCKS5 connection is routed through an independently running Tor process selected from the pool.",
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
		"socks5_proxy": fmt.Sprintf("socks5://%s", SOCKS5AdvertisedAddress),
		"description":  "HTTP and SOCKS5 proxy addresses for use with other libraries",
		"http_proxy_usage": []string{
			fmt.Sprintf("curl --proxy http://localhost%s/http-proxy http://example.com", ServerPort),
			fmt.Sprintf("HTTP_PROXY=http://localhost%s/http-proxy curl http://example.com", ServerPort),
			"Configure your HTTP client to use the http_proxy address",
		},
		"socks5_usage": []string{
			fmt.Sprintf("curl --socks5 %s http://example.com", SOCKS5AdvertisedAddress),
			fmt.Sprintf("HTTPS_PROXY=socks5://%s curl https://example.com", SOCKS5AdvertisedAddress),
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
		"description": "A load balancer that distributes requests through independent Tor proxies for anonymity",
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
	// Create load balancer with Tor proxy pool (using configured pool size)
	poolCtx, cancelPool := context.WithCancel(context.Background())
	defer cancelPool()

	lb, err := NewLoadBalancer(poolCtx, ProxyPoolSize)
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

	socksServer, err := NewSOCKS5Server(SOCKS5ListenAddress, lb)
	if err != nil {
		log.Fatalf("Failed to create SOCKS5 server: %v", err)
	}

	serverErrChan := make(chan error, 2)

	go func() {
		if serveErr := server.ListenAndServe(); serveErr != nil && serveErr != http.ErrServerClosed {
			serverErrChan <- fmt.Errorf("HTTP server error: %w", serveErr)
		}
	}()

	go socksServer.Serve(serverErrChan)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("Tor Load Balancer starting on %s", ServerPort)
	log.Printf("Load balancer with independent Tor instances is ready (pool size: %d)", ProxyPoolSize)
	log.Printf("HTTP Proxy available at: http://localhost%s/http-proxy", ServerPort)
	log.Printf("SOCKS5 Proxy available at: socks5://%s", SOCKS5AdvertisedAddress)

	select {
	case sig := <-sigChan:
		log.Printf("Received signal %s, shutting down gracefully...", sig)
	case serveErr := <-serverErrChan:
		log.Printf("Server terminated with error: %v", serveErr)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}
	if err := socksServer.Shutdown(); err != nil {
		log.Printf("SOCKS5 server shutdown error: %v", err)
	}

	cancelPool()

	log.Println("Server stopped")
}

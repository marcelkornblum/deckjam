package player

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// IPv4Proxy is a local HTTP forward proxy that resolves and dials upstream endpoints
// strictly over IPv4 ("tcp4") first, preventing mobile carrier phone hotspot IPv6 stalls
// on Linux/SteamOS where broken SLAAC IPv6 routes can blackhole Spotify endpoints.
type IPv4Proxy struct {
	server   *http.Server
	listener net.Listener
	dialer   *net.Dialer
	tr       *http.Transport
	closeOnce sync.Once
}

// dialIPv4First attempts to dial using "tcp4" first; only if tcp4 resolution/dial fails
// does it fallback to dual-stack "tcp".
func (p *IPv4Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := p.dialer.DialContext(ctx, "tcp4", addr)
	if err == nil {
		return conn, nil
	}
	return p.dialer.DialContext(ctx, "tcp", addr)
}

// StartIPv4Proxy starts an HTTP and CONNECT proxy on listenAddr (e.g. "127.0.0.1:3679").
// It returns an io.Closer to shut down the proxy, the actual listening address, and any error.
func StartIPv4Proxy(listenAddr string) (io.Closer, string, error) {
	if listenAddr == "" {
		listenAddr = "127.0.0.1:3679"
	}

	ln, err := net.Listen("tcp4", listenAddr)
	if err != nil {
		// Fallback to generic tcp if tcp4 listen fails on the given address
		ln, err = net.Listen("tcp", listenAddr)
		if err != nil {
			return nil, "", fmt.Errorf("failed to listen for IPv4 proxy on %s: %w", listenAddr, err)
		}
	}

	p := &IPv4Proxy{
		listener: ln,
		dialer: &net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}

	p.tr = &http.Transport{
		DialContext:           p.dial,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	p.server = &http.Server{
		Handler:      http.HandlerFunc(p.handleRequest),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // Streaming chunked audio needs no overall write timeout
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		if err := p.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[ipv4proxy] Server terminated: %v", err)
		}
	}()

	return p, ln.Addr().String(), nil
}

// Close gracefully shuts down the IPv4 forward proxy.
func (p *IPv4Proxy) Close() error {
	var err error
	p.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.tr.CloseIdleConnections()
		err = p.server.Shutdown(ctx)
		_ = p.listener.Close()
	})
	return err
}

func (p *IPv4Proxy) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect handles HTTPS tunneling via HTTP CONNECT.
func (p *IPv4Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	dest := r.URL.Host
	if !strings.Contains(dest, ":") {
		dest = net.JoinHostPort(dest, "443")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	destConn, err := p.dial(ctx, "tcp", dest)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to connect to %s: %v", dest, err), http.StatusServiceUnavailable)
		return
	}
	defer destConn.Close()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, fmt.Sprintf("Hijacking failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Notify client that connection is established
	_, err = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		return
	}

	// Bi-directional tunnel between client and upstream destination
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(destConn, clientConn)
		if tc, ok := destConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	go func() {
		defer wg.Done()
		_, _ = io.Copy(clientConn, destConn)
		if tc, ok := clientConn.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
	}()

	wg.Wait()
}

// handleHTTP handles plain HTTP proxy requests (e.g. GET http://apresolve.spotify.com/...).
func (p *IPv4Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""

	// Remove hop-by-hop headers
	removeHopHeaders(outReq.Header)

	resp, err := p.tr.RoundTrip(outReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Proxy roundtrip failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	removeHopHeaders(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func removeHopHeaders(h http.Header) {
	h.Del("Proxy-Connection")
	h.Del("Proxy-Authenticate")
	h.Del("Proxy-Authorization")
	h.Del("Te")
	h.Del("Trailers")
	h.Del("Transfer-Encoding")
	h.Del("Upgrade")
}

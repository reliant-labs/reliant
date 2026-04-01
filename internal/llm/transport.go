// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// DNS cache: stores resolved host → IP address with TTL.
// Avoids repeated DNS lookups for the same API host across LLM iterations.
var (
	dnsCache   = make(map[string]dnsCacheEntry)
	dnsCacheMu sync.RWMutex
)

const (
	// dnsCacheTTL is how long a successful DNS resolution is cached.
	// 60s is conservative — API hosts rarely change IPs, and this prevents
	// repeated lookups within a single agent loop.
	dnsCacheTTL = 60 * time.Second

	// dnsRetryAttempts is how many times to retry the system resolver
	// before falling back to public DNS.
	dnsRetryAttempts = 3

	// dnsRetryBaseDelay is the base delay between DNS retry attempts.
	// Actual delay is (attempt+1) * base, so 200ms, 400ms, 600ms.
	dnsRetryBaseDelay = 200 * time.Millisecond

	// dnsFallbackTimeout is the timeout for connecting to fallback DNS servers.
	dnsFallbackTimeout = 5 * time.Second
)

// Fallback DNS servers used when the system resolver fails.
var fallbackDNSServers = []string{"8.8.8.8:53", "1.1.1.1:53"}

type dnsCacheEntry struct {
	addr    string // resolved "host:port" or just "ip"
	expires time.Time
}

// ResilientTransport returns an *http.Transport hardened against DNS failures.
//
// Three layers of defense:
//  1. Retry: DNS failures are retried 3 times with the system resolver (handles transient packet loss).
//  2. Fallback: If the system resolver consistently fails, tries Google (8.8.8.8) and Cloudflare (1.1.1.1).
//  3. Cache: Successful DNS resolutions are cached for 60s, so subsequent requests skip DNS entirely.
//
// Also increases MaxIdleConnsPerHost from Go's default of 2 to 10, promoting
// HTTP/2 connection reuse and reducing the frequency of DNS lookups.
func ResilientTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()

	// Promote connection reuse — Go's default of 2 causes frequent connection
	// churn, and each new connection requires a DNS lookup.
	base.MaxIdleConnsPerHost = 10

	// ResponseHeaderTimeout limits how long we wait for response headers after
	// sending a request. This catches hung connections where the server accepts
	// the request but never responds. Once headers arrive, the body can stream
	// indefinitely (this timeout does NOT apply to body reads).
	base.ResponseHeaderTimeout = 2 * time.Minute

	systemDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	fallbackResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: dnsFallbackTimeout}
			var lastErr error
			for _, dns := range fallbackDNSServers {
				conn, err := d.DialContext(ctx, "udp", dns)
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			// All fallback servers failed, try the original address as last resort
			if lastErr != nil {
				return d.DialContext(ctx, network, address)
			}
			return nil, lastErr
		},
	}

	fallbackDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  fallbackResolver,
	}

	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			// Can't parse host:port, fall through to default behavior
			return systemDialer.DialContext(ctx, network, addr)
		}

		// Skip resilience for IP addresses (no DNS needed)
		if net.ParseIP(host) != nil {
			return systemDialer.DialContext(ctx, network, addr)
		}

		// Layer 3: Check DNS cache first
		if cachedIP := getCachedDNS(host); cachedIP != "" {
			conn, connErr := systemDialer.DialContext(ctx, network, net.JoinHostPort(cachedIP, port))
			if connErr == nil {
				return conn, nil
			}
			// Cached IP might be stale (host migrated), evict and do fresh lookup
			evictDNS(host)
			logging.Debug("[Transport] Cached DNS entry failed, doing fresh lookup", "host", host, "cachedIP", cachedIP, "error", connErr)
		}

		// Layer 1: Try system resolver with retries
		var lastErr error
		for attempt := 0; attempt < dnsRetryAttempts; attempt++ {
			conn, dialErr := systemDialer.DialContext(ctx, network, addr)
			if dialErr == nil {
				// Success — resolve and cache the IP for future use
				cacheResolvedHost(host)
				return conn, nil
			}
			lastErr = dialErr

			// Only retry DNS errors; other errors (TLS, connection refused) won't benefit from retry
			if !isDNSError(dialErr) {
				return nil, dialErr
			}

			// Don't sleep on the last attempt
			if attempt < dnsRetryAttempts-1 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * dnsRetryBaseDelay):
				}
			}
		}

		// Layer 2: Fallback to public DNS servers
		logging.Warn("[Transport] System DNS failed, trying public DNS fallback",
			"host", host,
			"attempts", dnsRetryAttempts,
			"error", lastErr,
		)

		conn, dialErr := fallbackDialer.DialContext(ctx, network, addr)
		if dialErr == nil {
			cacheResolvedHost(host)
			logging.Info("[Transport] Public DNS fallback succeeded", "host", host)
			return conn, nil
		}

		logging.Error("[Transport] All DNS resolution attempts failed",
			"host", host,
			"systemError", lastErr,
			"fallbackError", dialErr,
		)
		return nil, lastErr
	}

	return base
}

// ResilientHTTPClient returns an *http.Client using ResilientTransport.
// Convenience for drivers that pass an http.Client to SDK options.
func ResilientHTTPClient() *http.Client {
	return &http.Client{
		Transport: ResilientTransport(),
	}
}

// StreamIdleTimeout is the maximum time a streaming response can go without
// receiving any data before the read is aborted. This catches "silent hang"
// scenarios where the server keeps the TCP connection open but stops sending
// SSE events. Set to 5 minutes — LLM providers send heartbeat/ping events
// more frequently than this during normal operation.
const StreamIdleTimeout = 5 * time.Minute

// IdleTimeoutReader wraps an io.ReadCloser and enforces an idle timeout on reads.
// If no data is received within the timeout, Read returns ErrStreamIdleTimeout.
// This is designed for SSE streaming where the server may silently stop sending
// data but keep the TCP connection alive.
type IdleTimeoutReader struct {
	r       io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
	done    chan struct{}
	once    sync.Once
}

// ErrStreamIdleTimeout is returned when an SSE stream goes idle for too long.
var ErrStreamIdleTimeout = errors.New("stream idle timeout: no data received for " + StreamIdleTimeout.String())

// NewIdleTimeoutReader wraps r with an idle timeout.
// Each successful Read resets the timer. If the timer fires before the next
// Read completes, the underlying reader is closed, causing the blocked Read
// to return an error.
func NewIdleTimeoutReader(r io.ReadCloser, timeout time.Duration) *IdleTimeoutReader {
	itr := &IdleTimeoutReader{
		r:       r,
		timeout: timeout,
		done:    make(chan struct{}),
	}
	itr.timer = time.AfterFunc(timeout, func() {
		logging.Warn("[IdleTimeoutReader] Stream idle timeout reached, closing connection",
			"timeout", timeout)
		itr.Close()
	})
	return itr
}

func (itr *IdleTimeoutReader) Read(p []byte) (int, error) {
	n, err := itr.r.Read(p)
	if n > 0 {
		// Data received — reset the idle timer
		itr.timer.Reset(itr.timeout)
	}
	return n, err
}

func (itr *IdleTimeoutReader) Close() error {
	itr.once.Do(func() {
		itr.timer.Stop()
		close(itr.done)
	})
	return itr.r.Close()
}

// idleTimeoutTransport wraps an http.RoundTripper and applies IdleTimeoutReader
// to all response bodies. This ensures SDK-managed streaming connections
// (where we don't have direct access to resp.Body) still get idle timeout
// protection.
type idleTimeoutTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t *idleTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.Body != nil {
		resp.Body = NewIdleTimeoutReader(resp.Body, t.timeout)
	}
	return resp, nil
}

// WrapWithIdleTimeout wraps an existing http.RoundTripper with idle stream
// timeout protection. Use this when you have a custom transport chain (e.g.,
// with token refresh or header manipulation) and want to add idle timeout
// detection on top.
func WrapWithIdleTimeout(base http.RoundTripper) http.RoundTripper {
	return &idleTimeoutTransport{
		base:    base,
		timeout: StreamIdleTimeout,
	}
}

// StreamingHTTPClient returns an *http.Client configured for LLM streaming:
//   - DNS resilience (retry, fallback, caching) via ResilientTransport
//   - ResponseHeaderTimeout (2min) to detect hung connections before streaming starts
//   - Idle stream timeout (5min) to detect silent hangs during streaming
//
// Use this for all LLM API calls that involve streaming responses.
func StreamingHTTPClient() *http.Client {
	return &http.Client{
		Transport: &idleTimeoutTransport{
			base:    ResilientTransport(),
			timeout: StreamIdleTimeout,
		},
	}
}

// isDNSError returns true if the error is a DNS resolution failure.
func isDNSError(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// cacheResolvedHost resolves a hostname and caches the first IP address.
func cacheResolvedHost(host string) {
	// Resolve the hostname to get the IP address for caching
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		return
	}

	dnsCacheMu.Lock()
	dnsCache[host] = dnsCacheEntry{
		addr:    addrs[0],
		expires: time.Now().Add(dnsCacheTTL),
	}
	dnsCacheMu.Unlock()
}

// getCachedDNS returns a cached IP for the host, or empty string if not cached or expired.
func getCachedDNS(host string) string {
	dnsCacheMu.RLock()
	entry, ok := dnsCache[host]
	dnsCacheMu.RUnlock()

	if !ok || time.Now().After(entry.expires) {
		if ok {
			// Expired — clean up
			evictDNS(host)
		}
		return ""
	}
	return entry.addr
}

// evictDNS removes a host from the DNS cache.
func evictDNS(host string) {
	dnsCacheMu.Lock()
	delete(dnsCache, host)
	dnsCacheMu.Unlock()
}

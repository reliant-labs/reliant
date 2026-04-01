// Copyright (c) 2025 Reliant Labs
package llm

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDNSError(t *testing.T) {
	t.Run("nil error is not DNS error", func(t *testing.T) {
		assert.False(t, isDNSError(nil))
	})

	t.Run("net.DNSError is detected", func(t *testing.T) {
		err := &net.DNSError{
			Err:  "no such host",
			Name: "api.anthropic.com",
		}
		assert.True(t, isDNSError(err))
	})

	t.Run("wrapped DNS error is detected", func(t *testing.T) {
		inner := &net.DNSError{
			Err:  "no such host",
			Name: "api.openai.com",
		}
		wrapped := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: inner,
		}
		assert.True(t, isDNSError(wrapped))
	})

	t.Run("non-DNS error is not detected", func(t *testing.T) {
		err := &net.OpError{
			Op:  "dial",
			Net: "tcp",
			Err: &net.AddrError{Err: "missing port", Addr: "example.com"},
		}
		assert.False(t, isDNSError(err))
	})
}

func TestDNSCache(t *testing.T) {
	// Clean up cache between tests
	t.Cleanup(func() {
		dnsCacheMu.Lock()
		dnsCache = make(map[string]dnsCacheEntry)
		dnsCacheMu.Unlock()
	})

	t.Run("cache miss returns empty", func(t *testing.T) {
		assert.Equal(t, "", getCachedDNS("nonexistent.example.com"))
	})

	t.Run("cache hit returns IP", func(t *testing.T) {
		dnsCacheMu.Lock()
		dnsCache["test.example.com"] = dnsCacheEntry{
			addr:    "1.2.3.4",
			expires: time.Now().Add(time.Minute),
		}
		dnsCacheMu.Unlock()

		assert.Equal(t, "1.2.3.4", getCachedDNS("test.example.com"))
	})

	t.Run("expired entry returns empty", func(t *testing.T) {
		dnsCacheMu.Lock()
		dnsCache["expired.example.com"] = dnsCacheEntry{
			addr:    "5.6.7.8",
			expires: time.Now().Add(-time.Second), // already expired
		}
		dnsCacheMu.Unlock()

		assert.Equal(t, "", getCachedDNS("expired.example.com"))

		// Should also evict the entry
		dnsCacheMu.RLock()
		_, exists := dnsCache["expired.example.com"]
		dnsCacheMu.RUnlock()
		assert.False(t, exists, "expired entry should be evicted")
	})

	t.Run("evict removes entry", func(t *testing.T) {
		dnsCacheMu.Lock()
		dnsCache["evict.example.com"] = dnsCacheEntry{
			addr:    "9.10.11.12",
			expires: time.Now().Add(time.Minute),
		}
		dnsCacheMu.Unlock()

		evictDNS("evict.example.com")
		assert.Equal(t, "", getCachedDNS("evict.example.com"))
	})
}

func TestResilientTransport(t *testing.T) {
	t.Cleanup(func() {
		dnsCacheMu.Lock()
		dnsCache = make(map[string]dnsCacheEntry)
		dnsCacheMu.Unlock()
	})

	t.Run("returns a non-nil transport", func(t *testing.T) {
		tr := ResilientTransport()
		require.NotNil(t, tr)
		assert.Equal(t, 10, tr.MaxIdleConnsPerHost)
	})

	t.Run("can dial a real host", func(t *testing.T) {
		tr := ResilientTransport()
		require.NotNil(t, tr.DialContext)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		conn, err := tr.DialContext(ctx, "tcp", "dns.google:443")
		if err != nil {
			t.Skipf("network not available: %v", err)
		}
		defer conn.Close()
		assert.NotNil(t, conn)

		// Should have cached the DNS entry
		cached := getCachedDNS("dns.google")
		assert.NotEmpty(t, cached, "successful dial should cache DNS")
	})

	t.Run("IP addresses bypass DNS resilience", func(t *testing.T) {
		tr := ResilientTransport()
		require.NotNil(t, tr.DialContext)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		conn, err := tr.DialContext(ctx, "tcp", "8.8.8.8:443")
		if err != nil {
			t.Skipf("network not available: %v", err)
		}
		defer conn.Close()
		assert.NotNil(t, conn)

		// Should NOT cache IP addresses
		assert.Empty(t, getCachedDNS("8.8.8.8"))
	})
}

func TestResilientHTTPClient(t *testing.T) {
	client := ResilientHTTPClient()
	require.NotNil(t, client)
	require.NotNil(t, client.Transport)

	tr, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 10, tr.MaxIdleConnsPerHost)
}

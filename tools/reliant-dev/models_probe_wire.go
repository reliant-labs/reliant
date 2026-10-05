// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/reliant-labs/reliant/internal/llm"
)

// installWireDump makes every LLM HTTP connection in this process record its
// plaintext to dir, one file per connection, so a probe can show what a
// provider actually put on the wire (as opposed to what a driver surfaced).
//
// It works by replacing TLS dialing on the process-wide shared transport with a
// dialer that returns a recording connection. That connection is not a
// *tls.Conn, so net/http falls back to HTTP/1.1 — plaintext framing a human can
// read — which is why this is a diagnostic flag and not the default. Bearer
// credentials in request headers are scrubbed before they reach disk. Response
// bodies are recorded as received (possibly gzip + chunked).
func installWireDump(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	transport := llm.ResilientTransport()
	plainDial := transport.DialContext
	if plainDial == nil {
		plainDial = (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	}
	var seq atomic.Int64
	transport.ForceAttemptHTTP2 = false
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := plainDial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, _ := net.SplitHostPort(addr)
		tc := tls.Client(raw, &tls.Config{ServerName: host, NextProtos: []string{"http/1.1"}})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%03d-%s.log", seq.Add(1), host)))
		if err != nil {
			return tc, nil
		}
		return &recordingConn{Conn: tc, out: f}, nil
	}
	return nil
}

var wireSecret = regexp.MustCompile(`(?im)^(authorization|x-api-key|chatgpt-account-id|cookie):.*$`)

// wireFingerprint matches account and device identifiers that providers carry
// in request BODIES rather than headers — Claude Code's metadata.user_id is a
// JSON string holding device_id/account_uuid, so the quotes arrive escaped
// (\"device_id\":\"…\"), while Codex sends them as plain JSON. They are not
// credentials, but they fingerprint the account, so they never reach disk.
var wireFingerprint = regexp.MustCompile(`((?:\\)?"(?:device_id|account_uuid|organization_uuid|account_email|installation_id)(?:\\)?":\s*(?:\\)?")[^"\\]*`)

// scrubWire redacts credentials and account fingerprints from outbound bytes.
func scrubWire(b []byte) []byte {
	b = wireSecret.ReplaceAll(b, []byte("$1: [redacted]"))
	return wireFingerprint.ReplaceAll(b, []byte("${1}[redacted]"))
}

type recordingConn struct {
	net.Conn
	mu  sync.Mutex
	out *os.File
}

func (c *recordingConn) record(tag string, b []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tag == ">>" {
		b = scrubWire(b)
	}
	fmt.Fprintf(c.out, "\n%s %s %d\n", tag, time.Now().Format("15:04:05.000"), len(b))
	_, _ = c.out.Write(b)
}

func (c *recordingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.record("<<", p[:n])
	}
	return n, err
}

func (c *recordingConn) Write(p []byte) (int, error) {
	c.record(">>", p)
	return c.Conn.Write(p)
}

func (c *recordingConn) Close() error {
	c.mu.Lock()
	_ = c.out.Close()
	c.mu.Unlock()
	return c.Conn.Close()
}

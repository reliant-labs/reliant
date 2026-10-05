// Copyright (c) 2025 Reliant Labs
package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckHostMatrix(t *testing.T) {
	hosted := Policy{LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "rebind.example.com":
			return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("10.1.2.3")}, nil
		case "internal.example.com":
			return []net.IP{net.ParseIP("192.168.1.5")}, nil
		case "public.example.com":
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return nil, errors.New("no such host")
	}}
	tests := []struct {
		host    string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"[::1]", true},
		{"10.0.0.8", true},
		{"172.16.5.5", true},
		{"192.168.0.1", true},
		{"169.254.169.254", true},
		{"fd00:ec2::254", true},
		{"::ffff:10.0.0.1", true},
		{"100.64.1.1", true},
		{"0.0.0.0", true},
		{"localhost", true},
		{"foo.localhost", true},
		{"internal.example.com", true},
		{"rebind.example.com", true}, // ANY resolved address blocked => blocked
		{"8.8.8.8", false},
		{"public.example.com", false},
		{"unresolvable.example.com", false}, // cannot be dialed; the dial guard owns the rest
	}
	for _, tc := range tests {
		t.Run(tc.host, func(t *testing.T) {
			err := hosted.CheckHost(context.Background(), tc.host)
			if tc.blocked {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrBlockedAddress)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestSelfHostAllowsPrivate(t *testing.T) {
	p := Policy{AllowPrivate: true}
	for _, h := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "localhost"} {
		assert.NoError(t, p.CheckHost(context.Background(), h), h)
	}
}

// The guard must hold at connect time, not only at validation time: a name
// that resolved to a public address when saved can resolve to loopback later.
func TestTransportBlocksDialToLoopback(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer srv.Close()

	guarded := &http.Client{Transport: Policy{}.Transport()}
	resp, err := guarded.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
	assert.Zero(t, hits.Load(), "the server must never see the request")

	open := &http.Client{Transport: Policy{AllowPrivate: true}.Transport()}
	resp, err = open.Get(srv.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.EqualValues(t, 1, hits.Load())
}

func TestTransportBlocksRedirectToInternal(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	// Allow the first hop only by dialing it through an open transport...
	// the guarded client cannot reach either, which is the point.
	guarded := &http.Client{Transport: Policy{}.Transport()}
	_, err := guarded.Get(redirector.URL)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBlockedAddress)
}

func TestIPBlocked(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "fe80::1", "0.0.0.0", "100.64.0.1", "fc00::1", "::ffff:127.0.0.1", "224.0.0.1"} {
		if !IPBlocked(net.ParseIP(ip)) {
			t.Errorf("%s should be blocked", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if IPBlocked(net.ParseIP(ip)) {
			t.Errorf("%s should be allowed", ip)
		}
	}
}

func fakeResolver(ips ...string) func(ctx context.Context, host string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

func TestDialRefusesLiteralPrivateIPs(t *testing.T) {
	g := New()
	for _, addr := range []string{"127.0.0.1:80", "169.254.169.254:80", "10.0.0.5:443", "[::1]:80"} {
		_, err := g.DialContext(context.Background(), "tcp", addr)
		if err == nil || !strings.Contains(err.Error(), "blocked") {
			t.Errorf("%s: want blocked error, got %v", addr, err)
		}
	}
}

func TestDialRefusesHostnameResolvingToPrivateIP(t *testing.T) {
	g := New()
	g.Resolve = fakeResolver("10.9.9.9")
	_, err := g.DialContext(context.Background(), "tcp", "innocent.example.com:80")
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("want blocked error, got %v", err)
	}
}

func TestDialRefusesWhenAnyResolvedIPIsPrivate(t *testing.T) {
	g := New()
	g.Resolve = fakeResolver("8.8.8.8", "169.254.169.254")
	if _, err := g.DialContext(context.Background(), "tcp", "mixed.example.com:80"); err == nil {
		t.Fatal("a mixed public/private answer must be refused")
	}
}

func TestAllowLoopbackForTests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()

	blocked := &http.Client{Transport: New().Transport()}
	if _, err := blocked.Get(srv.URL); err == nil {
		t.Fatal("loopback test server must be refused by default")
	}

	g := New()
	g.AllowLoopback = true
	resp, err := (&http.Client{Transport: g.Transport()}).Get(srv.URL)
	if err != nil {
		t.Fatalf("AllowLoopback: %v", err)
	}
	resp.Body.Close()
}

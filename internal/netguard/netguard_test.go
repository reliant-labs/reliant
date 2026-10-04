package netguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

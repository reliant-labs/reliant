package hostedhttp

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

func testExecutor(t *testing.T, h http.Handler) *Executor {
	t.Helper()
	srv := netguardtest.NewTLSServer(t, h)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	guard := netguard.New()
	guard.AllowLoopback = true
	guard.Resolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	guard.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return netguardtest.Dial(ctx, network, srv.Listener.Addr().String())
	}
	return newWithGuard(guard, roots)
}
func TestRedirectIsReturnedNeverFollowed(t *testing.T) {
	calls := 0
	e := testExecutor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Location", "https://evil.example/steal")
		w.WriteHeader(http.StatusFound)
	}))
	out, err := e.Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "POST"}, &testSecret{})
	if err != nil || out == nil || calls != 1 || out.StatusCode != 302 {
		t.Fatalf("redirect behavior: calls=%d result=%+v err=%v", calls, out, err)
	}
}
func TestEncodedEchoDoesNotEscape(t *testing.T) {
	e := testExecutor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"encoded": base64.StdEncoding.EncodeToString([]byte(sentinel)), "raw": sentinel})
	}))
	out, err := e.Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "POST"}, &testSecret{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), sentinel) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString([]byte(sentinel))) {
		t.Fatal("credential echo escaped scrub boundary")
	}
}
func TestCredentialRechecksDestinationAndCannotPrint(t *testing.T) {
	c := &credential{policy: testPolicy(), value: sentinel}
	req, _ := http.NewRequest("POST", "https://evil.example/rpc", nil)
	if c.Apply(req) == nil || req.Header.Get("X-API-Key") != "" {
		t.Fatal("credential attached to unauthorized destination")
	}
	if strings.Contains(c.String(), sentinel) || strings.Contains(c.GoString(), sentinel) {
		t.Fatal("credential prints secret")
	}
}

package hostedhttp

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/netguard/netguardtest"
)

const sentinel = "fk_synthetic_not_a_real_secret_123456789"

type testSecret struct {
	calls int
	err   error
}

func (s *testSecret) Use(_ context.Context, f func([]byte) error) error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	return f([]byte(sentinel))
}
func testPolicy() Policy {
	return Policy{Origin: "https://example.com", Endpoints: []Endpoint{{Method: "POST", Path: "/rpc"}}}
}

func TestRequestDigestWireContract(t *testing.T) {
	req := Request{URL: "https://example.com/rpc", Method: "POST"}
	sum := sha256.Sum256([]byte(`{"url":"https://example.com/rpc","method":"POST","body":null}`))
	got, err := RequestDigest(req)
	if err != nil || got != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest wire drift: %q %v", got, err)
	}
}

func TestPolicyRejectsBeforeSecretRead(t *testing.T) {
	for _, u := range []string{"https://evil.example/rpc", "http://example.com/rpc", "https://example.com:443/rpc", "https://user@example.com/rpc", "https://example.com/rpc?x=1", "https://example.com/rpc#x", "https://example.com/%72pc", "https://example.com/a/../rpc", "https://example.com//rpc", "https://example.com/admin"} {
		t.Run(u, func(t *testing.T) {
			s := &testSecret{}
			_, err := New().Execute(context.Background(), testPolicy(), Request{URL: u, Method: "POST"}, s)
			if err == nil || s.calls != 0 {
				t.Fatalf("unauthorized URL reached secret: err=%v calls=%d", err, s.calls)
			}
		})
	}
	s := &testSecret{}
	_, err := New().Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "DELETE"}, s)
	if err == nil || s.calls != 0 {
		t.Fatal("unauthorized method reached secret")
	}
}

func TestProviderFailureCannotLeak(t *testing.T) {
	s := &testSecret{err: errors.New(sentinel)}
	_, err := New().Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "POST"}, s)
	if err == nil || strings.Contains(err.Error(), sentinel) || errors.Unwrap(err) != nil {
		t.Fatal("provider failure escaped sanitization")
	}
}

func TestExecuteReusesRunnerAndScrubsResponse(t *testing.T) {
	calls := 0
	srv := netguardtest.NewTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-API-Key") != sentinel {
			t.Error("missing injected credential")
		}
		if r.URL.Path != "/rpc" || r.Method != "POST" {
			t.Error("wrong endpoint")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Echo", sentinel)
		_ = json.NewEncoder(w).Encode(map[string]any{"echo": sentinel, "nested": map[string]any{"key": sentinel}})
	}))
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	g := netguard.New()
	g.AllowLoopback = true
	// Keep the grant's canonical :443 origin while routing only the test dial
	// to the TLS server. The certificate covers example.com.
	g.Resolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }
	g.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return netguardtest.Dial(ctx, network, srv.Listener.Addr().String())
	}
	e := newWithGuard(g, roots)
	s := &testSecret{}
	out, err := e.Execute(context.Background(), testPolicy(), Request{URL: "https://example.com/rpc", Method: "POST", Body: map[string]any{"safe": true}}, s)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if calls != 1 || s.calls != 1 || out.IsError || out.StatusCode != 200 {
		t.Fatalf("unexpected execution: calls=%d secret=%d out=%s", calls, s.calls, raw)
	}
	if strings.Contains(string(raw), sentinel) {
		t.Fatal("response leaked credential")
	}
}

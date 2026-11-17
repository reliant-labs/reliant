package cloud

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func noConnectBackoff(t *testing.T) {
	t.Helper()
	old := connectBackoff
	connectBackoff = func(int) time.Duration { return 0 }
	t.Cleanup(func() { connectBackoff = old })
}

// swallowFirst holds the first connection it accepts without ever answering
// it, so the client's TLS handshake on it times out; later ones reach the
// server.
type swallowFirst struct {
	net.Listener
	n    atomic.Int32
	mu   sync.Mutex
	held []net.Conn
}

func (l *swallowFirst) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil || l.n.Add(1) > 1 {
			return c, err
		}
		l.mu.Lock()
		l.held = append(l.held, c)
		l.mu.Unlock()
	}
}

func (l *swallowFirst) Close() error {
	l.mu.Lock()
	for _, c := range l.held {
		_ = c.Close()
	}
	l.mu.Unlock()
	return l.Listener.Close()
}

// One TLS handshake timeout no longer fails the call: the request never left
// this machine, so it is tried again (forge_cli_report 89bb4664: half of a
// loaded host's `obs query` runs failed on GetTenant's handshake).
func TestCall_RetriesATLSHandshakeTimeout(t *testing.T) {
	noConnectBackoff(t)
	var served atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		_, _ = w.Write([]byte(`{"tenant":{"orgId":"org-1"}}`))
	}))
	srv.Listener = &swallowFirst{Listener: srv.Listener}
	srv.StartTLS()
	defer srv.Close()

	client := NewClient(Endpoint{URL: srv.URL}, Credential{Token: "tok"})
	client.HTTP = srv.Client()
	client.HTTP.Transport.(*http.Transport).TLSHandshakeTimeout = 100 * time.Millisecond

	org, err := ResolveOrganization(context.Background(), client)
	if err != nil {
		t.Fatalf("ResolveOrganization: %v", err)
	}
	if org != "org-1" || served.Load() != 1 {
		t.Errorf("org %q, served %d times; want org-1 once", org, served.Load())
	}
}

// fakeTransport fails every round trip with err, after reporting the
// headers written when wrote is set.
type fakeTransport struct {
	wrote bool
	err   error
	calls atomic.Int32
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	if tr := httptrace.ContextClientTrace(r.Context()); f.wrote && tr != nil && tr.WroteHeaders != nil {
		tr.WroteHeaders()
	}
	return nil, f.err
}

// A failure after the request was written may have reached a handler, so it
// is never repeated; one before is tried connectAttempts times, then
// reported.
func TestCall_RetriesOnlyWhatNeverLeft(t *testing.T) {
	noConnectBackoff(t)
	for _, tc := range []struct {
		name  string
		wrote bool
		want  int32
	}{
		{"connection never set up", false, int32(connectAttempts)},
		{"request written", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTransport{wrote: tc.wrote, err: errors.New("net/http: TLS handshake timeout")}
			client := NewClient(Endpoint{URL: "https://cp.invalid"}, Credential{Token: "tok"})
			client.HTTP = &http.Client{Transport: ft}
			err := client.Call(context.Background(), "controlplane.v1.DeployService/Promote", map[string]any{}, nil)
			if err == nil || !strings.Contains(err.Error(), "TLS handshake timeout") {
				t.Errorf("err = %v, want the transport error", err)
			}
			if got := ft.calls.Load(); got != tc.want {
				t.Errorf("round trips = %d, want %d", got, tc.want)
			}
		})
	}
}

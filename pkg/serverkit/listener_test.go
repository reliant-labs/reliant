package serverkit_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// h2cClient speaks HTTP/2 with prior knowledge over plain TCP — what Connect,
// gRPC and an h2c load balancer send.
func h2cClient() *http.Client {
	return &http.Client{Transport: &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}}
}

// An h2c request in flight when SIGTERM lands is drained like an HTTP/1.1
// one: Run waits for it. serverkit served h2c through x/net's h2c handler,
// which hijacks the connection, and Shutdown does not track a hijacked
// connection — Run returned in ~20ms while the request was still running, so
// a rolling deploy cut every in-flight Connect call.
func TestRun_DrainsInFlightH2CRequests(t *testing.T) {
	requireSelfSIGTERM(t)
	for _, proto := range []string{"http1", "h2c"} {
		t.Run(proto, func(t *testing.T) {
			addr := freeAddr(t)
			inHandler := make(chan struct{})
			mux := http.NewServeMux()
			mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != map[string]int{"http1": 1, "h2c": 2}[proto] {
					t.Errorf("served over %s, want %s", r.Proto, proto)
				}
				close(inHandler)
				time.Sleep(time.Second)
				w.WriteHeader(http.StatusOK)
			})
			cfg := baseConfig(addr)
			errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: mux})
			waitReady(t, addr, 2*time.Second)

			client := http.DefaultClient
			if proto == "h2c" {
				client = h2cClient()
			}
			done := make(chan int, 1)
			go func() {
				resp, err := client.Get("http://" + addr + "/slow") //nolint:noctx // outlives the test's request ctx on purpose
				if err != nil {
					done <- 0
					return
				}
				_ = resp.Body.Close()
				done <- resp.StatusCode
			}()
			<-inHandler
			start := time.Now()
			if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if took := time.Since(start); took < 900*time.Millisecond {
				t.Errorf("Run returned after %v, before the in-flight %s request finished", took, proto)
			}
			if code := <-done; code != http.StatusOK {
				t.Errorf("in-flight %s request got %d, want 200", proto, code)
			}
		})
	}
}

// A long-lived stream is ended as the drain begins, before Shutdown waits,
// over HTTP/1.1 and h2c alike, so the drain finishes at once instead of
// holding Run until ShutdownTimeout. OnShutdown, the teardown, still runs
// after the drain. The listener's drain registry (LongLived) is the one way a
// stream is ended before Shutdown waits; over h2c each stream is its own
// request to it, because native h2c does not hijack the connection.
func TestRun_LongLivedStreamsEndBeforeTheDrainWaits(t *testing.T) {
	requireSelfSIGTERM(t)
	for _, proto := range []string{"http1", "h2c"} {
		t.Run(proto, func(t *testing.T) {
			addr := freeAddr(t)
			var (
				mu    sync.Mutex
				order []string
			)
			note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
			inStream := make(chan struct{})
			mux := http.NewServeMux()
			mux.Handle("/stream", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(inStream)
				<-r.Context().Done() // a subscription that only ends when told to
				if !serverkit.Ended(r.Context()) {
					t.Error("the stream ended, but not by the drain")
				}
				note("stream ended")
			})))
			srv := serverkit.Server{
				Handler: mux,
				OnShutdown: func(context.Context) error {
					note("OnShutdown")
					return nil
				},
			}
			cfg := baseConfig(addr)
			cfg.ShutdownTimeout = 5 * time.Second
			errCh, _ := runInBackground(t, cfg, srv)
			waitReady(t, addr, 2*time.Second)

			client := http.DefaultClient
			if proto == "h2c" {
				client = h2cClient()
			}
			resp, err := client.Get("http://" + addr + "/stream") //nolint:noctx // held open across shutdown on purpose
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			<-inStream
			start := time.Now()
			if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("Run took %v: the stream held the drain until ShutdownTimeout", took)
			}
			mu.Lock()
			defer mu.Unlock()
			want := []string{"stream ended", "OnShutdown"}
			if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
				t.Errorf("order = %v, want %v", order, want)
			}
		})
	}
}

// ReadTimeout is a deadline on each HTTP/2 stream's request body, so its
// 30s default caps a client-streaming or bidi call. A negative ReadTimeout
// lifts it, which a server with long-lived client streams needs.
func TestRun_ReadTimeoutBoundsH2RequestBodiesAndCanBeLifted(t *testing.T) {
	requireSelfSIGTERM(t)
	for _, tc := range []struct {
		name        string
		readTimeout time.Duration
		wantOK      bool
	}{
		{"short ReadTimeout cuts a slow client stream", 200 * time.Millisecond, false},
		{"negative ReadTimeout lets it run", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := freeAddr(t)
			mux := http.NewServeMux()
			mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					w.WriteHeader(http.StatusRequestTimeout)
					return
				}
				w.WriteHeader(http.StatusOK)
			})
			cfg := baseConfig(addr)
			cfg.ReadTimeout = tc.readTimeout
			errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: mux})
			waitReady(t, addr, 2*time.Second)

			body, sender := io.Pipe()
			go func() {
				_, _ = sender.Write([]byte("first"))
				time.Sleep(600 * time.Millisecond)
				_, _ = sender.Write([]byte("second"))
				_ = sender.Close()
			}()
			resp, err := h2cClient().Post("http://"+addr+"/upload", "application/octet-stream", body) //nolint:noctx // test-local
			ok := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				_ = resp.Body.Close()
			}
			if ok != tc.wantOK {
				t.Errorf("slow client stream ok=%v (err %v), want %v", ok, err, tc.wantOK)
			}
			if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil && !errors.Is(err, context.Canceled) {
				t.Fatalf("Run: %v", err)
			}
		})
	}
}

// HTTP2MaxConcurrentStreams is what the server advertises in its SETTINGS
// frame, over h2c here and over TLS through the same http.Server.HTTP2.
func TestRun_HTTP2MaxConcurrentStreamsIsAdvertised(t *testing.T) {
	requireSelfSIGTERM(t)
	addr := freeAddr(t)
	cfg := baseConfig(addr)
	cfg.HTTP2MaxConcurrentStreams = 1000
	errCh, _ := runInBackground(t, cfg, serverkit.Server{Handler: http.NewServeMux()})
	waitReady(t, addr, 2*time.Second)

	got := func() uint32 {
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			t.Fatal(err)
		}
		fr := http2.NewFramer(conn, conn)
		if err := fr.WriteSettings(); err != nil {
			t.Fatal(err)
		}
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				t.Fatalf("read server SETTINGS: %v", err)
			}
			if sf, ok := f.(*http2.SettingsFrame); ok && !sf.IsAck() {
				v, _ := sf.Value(http2.SettingMaxConcurrentStreams)
				return v
			}
		}
	}()
	if got != 1000 {
		t.Errorf("advertised MAX_CONCURRENT_STREAMS = %d, want 1000", got)
	}
	if err := shutdownAndWait(t, errCh, 10*time.Second); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

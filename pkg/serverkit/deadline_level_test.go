package serverkit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Whose deadline a DeadlineExceeded ran into is on the record. The server's
// own budget expiring is a retryable WARN with deadline=server by default, as
// it always was; under Deadline.ExpiryIsError it is an ERROR — the server
// could not do the work in the time it allows itself, and retrying will not
// help a call that always needs more. A caller's shorter deadline
// (Connect-Timeout-Ms) is the caller's choice and stays WARN, with
// deadline=caller; a caller that went away stays a cancellation.

// lockedBuffer is a log sink the server's goroutines and the test share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// record waits for the first JSON record whose msg is msg and returns it.
func (b *lockedBuffer) record(t *testing.T, msg string) map[string]any {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		b.mu.Lock()
		lines := strings.Split(b.buf.String(), "\n")
		b.mu.Unlock()
		for _, line := range lines {
			var r map[string]any
			if json.Unmarshal([]byte(line), &r) == nil && r["msg"] == msg {
				return r
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	t.Fatalf("no %q record; the log:\n%s", msg, b.buf.String())
	return nil
}

// stallServer serves deadlineUnary and deadlineStream behind a chain built
// from deps; both handlers wait for their context to end and fail with its
// error, the way a query does when its deadline lands mid-flight.
func stallServer(t *testing.T, deps serverkit.ChainDeps) (string, *lockedBuffer, chan struct{}) {
	t.Helper()
	logs := &lockedBuffer{}
	deps.Logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if deps.Auth == nil {
		deps.Auth = serverkit.AuthFor(serverkit.AudienceUser, recorder{mu: &sync.Mutex{}, got: new([]string)})
	}
	chain, err := serverkit.NewChain(deps)
	if err != nil {
		t.Fatalf("NewChain: %v", err)
	}
	started := make(chan struct{}, 4)
	mux := http.NewServeMux()
	mux.Handle(deadlineUnary, connect.NewUnaryHandler(deadlineUnary,
		func(ctx context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			started <- struct{}{}
			<-ctx.Done()
			return nil, fmt.Errorf("update orders: %w", ctx.Err())
		}, chain.HandlerOptions()...))
	mux.Handle(deadlineStream, connect.NewServerStreamHandler(deadlineStream,
		func(ctx context.Context, _ *connect.Request[emptypb.Empty], _ *connect.ServerStream[emptypb.Empty]) error {
			started <- struct{}{}
			<-ctx.Done()
			return fmt.Errorf("watch orders: %w", ctx.Err())
		}, chain.HandlerOptions()...))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL, logs, started
}

// postWithTimeout makes a Connect unary call carrying Connect-Timeout-Ms
// while the client itself waits without a deadline, so the server sees the
// caller's deadline expire rather than the connection drop.
func postWithTimeout(t *testing.T, url string, ms int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url+deadlineUnary, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Connect-Timeout-Ms", fmt.Sprint(ms))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
}

func wantRecord(t *testing.T, r map[string]any, level, deadline, class string) {
	t.Helper()
	if r["level"] != level || r["error_class"] != class || r["code"] != "deadline_exceeded" && class != "canceled" {
		t.Errorf("%s record = %v: want level %s, error_class %s", r["msg"], r, level, class)
	}
	got, has := r["deadline"]
	switch {
	case deadline == "" && has:
		t.Errorf("%s record carries deadline=%v, want none", r["msg"], got)
	case deadline != "" && got != deadline:
		t.Errorf("%s record deadline = %v, want %s", r["msg"], got, deadline)
	}
}

func TestServerDeadlineExpiryIsAServerError(t *testing.T) {
	url, logs, _ := stallServer(t, serverkit.ChainDeps{Deadline: serverkit.Deadline{
		Unary: 50 * time.Millisecond, SlowAfter: -1, ExpiryIsError: true,
	}})
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, url+deadlineUnary)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if connect.CodeOf(err) != connect.CodeDeadlineExceeded {
		t.Fatalf("client got %v, want deadline_exceeded on the wire", err)
	}
	wantRecord(t, logs.record(t, "rpc failed"), "ERROR", "server", "server")
	// The audit record runs inside the deadline slot and must agree.
	if r := logs.record(t, "audit.event"); r["level"] != "ERROR" {
		t.Errorf("audit.event level = %v, want ERROR like rpc failed: %v", r["level"], r)
	}
}

func TestServerDeadlineExpiryOfAStreamIsAServerError(t *testing.T) {
	url, logs, _ := stallServer(t, serverkit.ChainDeps{Deadline: serverkit.Deadline{
		Procedures: map[string]time.Duration{deadlineStream: 50 * time.Millisecond}, SlowAfter: -1, ExpiryIsError: true,
	}})
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, url+deadlineStream)
	st, err := client.CallServerStream(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err == nil {
		for st.Receive() {
		}
		_ = st.Close()
	}
	wantRecord(t, logs.record(t, "stream failed"), "ERROR", "server", "server")
}

// Without ExpiryIsError the server's own expiry is logged at WARN, as it
// always was, and the record says deadline=server.
func TestServerDeadlineExpiryStaysWarnByDefault(t *testing.T) {
	url, logs, _ := stallServer(t, serverkit.ChainDeps{Deadline: serverkit.Deadline{
		Unary: 50 * time.Millisecond, SlowAfter: -1,
	}})
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, url+deadlineUnary)
	_, _ = client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	wantRecord(t, logs.record(t, "rpc failed"), "WARN", "server", "server")
	if r := logs.record(t, "audit.event"); r["level"] != "WARN" {
		t.Errorf("audit.event level = %v, want WARN like rpc failed: %v", r["level"], r)
	}
}

func TestCallerDeadlineExpiryStaysWarn(t *testing.T) {
	for name, deadline := range map[string]any{
		"default server deadline": nil,
		"no server deadline":      serverkit.NoDeadline,
	} {
		t.Run(name, func(t *testing.T) {
			deps := serverkit.ChainDeps{}
			if deadline != nil {
				deps.Deadline = deadline.(connect.Interceptor)
			}
			url, logs, _ := stallServer(t, deps)
			postWithTimeout(t, url, 50)
			wantRecord(t, logs.record(t, "rpc failed"), "WARN", "caller", "server")
			if r := logs.record(t, "audit.event"); r["level"] != "WARN" {
				t.Errorf("audit.event level = %v, want WARN: %v", r["level"], r)
			}
		})
	}
}

func TestClientCancelIsUnchanged(t *testing.T) {
	url, logs, started := stallServer(t, serverkit.ChainDeps{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	client := connect.NewClient[emptypb.Empty, emptypb.Empty](http.DefaultClient, url+deadlineUnary)
	_, _ = client.CallUnary(ctx, connect.NewRequest(&emptypb.Empty{}))
	r := logs.record(t, "rpc failed")
	if r["level"] != "INFO" || r["error_class"] != "canceled" {
		t.Errorf("a cancelled call: record = %v, want INFO, error_class=canceled", r)
	}
	if d, ok := r["deadline"]; ok {
		t.Errorf("a cancelled call carries deadline=%v, want none", d)
	}
}

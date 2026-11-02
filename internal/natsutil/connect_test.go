package natsutil

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// logRecorder keeps every slog record (internal/logging writes through the
// default slog logger).
type logRecorder struct {
	mu      sync.Mutex
	records []slog.Record
}

func (r *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *logRecorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec.Clone())
	return nil
}
func (r *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *logRecorder) WithGroup(string) slog.Handler      { return r }

// matching returns the records whose message starts with prefix, rendered as
// "LEVEL message key=value ...".
func (r *logRecorder) matching(prefix string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, rec := range r.records {
		if !strings.HasPrefix(rec.Message, prefix) {
			continue
		}
		var b strings.Builder
		b.WriteString(rec.Level.String() + " " + rec.Message)
		rec.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		out = append(out, b.String())
	}
	return out
}

func (r *logRecorder) await(t *testing.T, prefix string, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		got := r.matching(prefix)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func recordLogs(t *testing.T) *logRecorder {
	t.Helper()
	rec := &logRecorder{}
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return rec
}

// TestPermissionViolationIsLoggedAtError — each role connects with
// least-privilege grants, and the broker answers a refused publish or
// subscription with an async error only: unless the client logs it, the
// feature fails with no trace. It must be an ERROR naming the subject.
func TestPermissionViolationIsLoggedAtError(t *testing.T) {
	rec := recordLogs(t)
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.Users = []*server.User{{
		Username: "reliant-api-server",
		Password: "pw",
		Permissions: &server.Permissions{
			Publish:   &server.SubjectPermission{Allow: []string{"tools.request.*.*"}},
			Subscribe: &server.SubjectPermission{Allow: []string{"_INBOX.>"}},
		},
	}}
	srv := natstest.RunServer(&opts)
	defer srv.Shutdown()

	nc, err := Connect(Endpoint{URL: srv.ClientURL(), User: "reliant-api-server", Password: "pw"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer nc.Close()

	if err := nc.Publish("tools.request.u.d", nil); err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("tools.result.u.d.r", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := nc.SubscribeSync("tools.request.*.*"); err != nil {
		t.Fatal(err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatal(err)
	}

	got := rec.await(t, "NATS refused an operation", 2, 5*time.Second)
	if len(got) != 2 {
		t.Fatalf("got %d permission-violation records, want 2 (the publish and the subscription): %q", len(got), got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{`publish to "tools.result.u.d.r"`, `subscription to "tools.request.*.*"`} {
		if !strings.Contains(strings.ToLower(joined), strings.ToLower(want)) {
			t.Errorf("no record names %s:\n%s", want, joined)
		}
	}
	for _, r := range got {
		if !strings.HasPrefix(r, "ERROR ") {
			t.Errorf("a permissions violation was not logged at ERROR: %s", r)
		}
	}
	if strings.Contains(joined, `"tools.request.u.d"`) {
		t.Errorf("the allowed publish was reported as a violation:\n%s", joined)
	}
}

// TestAuthRefusalIsRetriedNotFatal — a rollout can restart a broker on a
// config that does not know this role's user yet. The connection must keep
// retrying and come back by itself once the broker does, not close for good on
// the second refusal (nats.go's default).
func TestAuthRefusalIsRetriedNotFatal(t *testing.T) {
	testkit.Slow(t, "waits through several 2s reconnect intervals against a restarted server")
	rec := recordLogs(t)
	users := func(names ...string) []*server.User {
		var us []*server.User
		for _, n := range names {
			us = append(us, &server.User{Username: n, Password: "pw-" + n})
		}
		return us
	}
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.Users = users("daemon-gateway")
	srv := natstest.RunServer(&opts)
	port := srv.Addr().(*net.TCPAddr).Port

	nc, err := Connect(Endpoint{URL: srv.ClientURL(), User: "daemon-gateway", Password: "pw-daemon-gateway"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer nc.Close()

	// The broker comes back on a config without this user ...
	srv.Shutdown()
	stale := natstest.DefaultTestOptions
	stale.Port = port
	stale.Users = users("someone-else")
	srv = natstest.RunServer(&stale)
	if got := rec.await(t, "NATS refused this service's credentials", 3, 15*time.Second); len(got) < 3 {
		t.Fatalf("saw %d credential refusals, want 3 before the broker learns the user: %q", len(got), got)
	}
	if nc.IsClosed() {
		t.Fatal("the connection closed after auth refusals; it must keep retrying until the broker knows its user")
	}

	// ... and then on one with it: the connection must recover by itself.
	srv.Shutdown()
	fresh := natstest.DefaultTestOptions
	fresh.Port = port
	fresh.Users = users("daemon-gateway")
	srv = natstest.RunServer(&fresh)
	defer srv.Shutdown()
	deadline := time.Now().Add(15 * time.Second)
	for !nc.IsConnected() {
		if time.Now().After(deadline) || nc.IsClosed() {
			t.Fatalf("did not reconnect once the broker knew the user (closed=%v, last error %v)", nc.IsClosed(), nc.LastError())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

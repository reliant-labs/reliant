package observe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/observe"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// level_test.go — a 500 that logs at WARN is a 500 nobody is paged for.
//
// Every failed RPC used to log at slog.LevelWarn. During a total database
// outage — every request 500ing — the process emitted an unbroken stream of
// WARN records, so the standard alert rule (level=ERROR) never fired. The
// symmetric mistake would be logging every failure at ERROR, which pages on
// each client typo. The policy is fault attribution: svcerr.Classify.

func TestLevelForError_FollowsFaultAttribution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want slog.Level
	}{
		// Someone should look at these.
		{"unclassified internal", errors.New("pq: connection refused"), slog.LevelError},
		{"deliberate internal", svcerr.Internal("write failed"), slog.LevelError},
		{"unavailable", svcerr.Unavailable("payments offline"), slog.LevelError},
		{"data loss", svcerr.DataLoss("checksum mismatch"), slog.LevelError},
		{"unknown", svcerr.Unknown("?"), slog.LevelError},
		{"4xx marked server", svcerr.WithClass(svcerr.NotFound("project row"), svcerr.ClassServer), slog.LevelError},

		// Server-side, but "retry", not "page".
		{"deadline exceeded", context.DeadlineExceeded, slog.LevelWarn},
		{"aborted", svcerr.Aborted("conflict"), slog.LevelWarn},
		{"unimplemented", svcerr.Unimplemented("later"), slog.LevelWarn},

		// The API working as designed: user errors.
		{"not found", svcerr.NotFound("user"), slog.LevelInfo},
		{"invalid argument", svcerr.InvalidArgument("name required"), slog.LevelInfo},
		{"permission denied", svcerr.PermissionDenied("admin only"), slog.LevelInfo},
		{"unauthenticated", svcerr.Unauthenticated("no token"), slog.LevelInfo},
		{"resource exhausted", svcerr.ResourceExhausted("rate limited"), slog.LevelInfo},
		{"plan limit", svcerr.PlanLimit("seat cap"), slog.LevelInfo},
		{"failed precondition", svcerr.FailedPrecondition("not ready"), slog.LevelInfo},
		{"insufficient balance", svcerr.InsufficientBalance("wallet empty"), slog.LevelInfo},
		{"unavailable marked user", svcerr.WithClass(svcerr.Unavailable("your machine is offline"), svcerr.ClassUser), slog.LevelInfo},

		// The caller went away.
		{"canceled", context.Canceled, slog.LevelInfo},

		{"no error", nil, slog.LevelInfo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Route through svcerr.Wrap so the test sees exactly what the
			// interceptor sees: a *connect.Error off a handler.
			err := tc.err
			if err != nil {
				err = svcerr.Wrap(err)
			}
			if got := observe.LevelForError(err); got != tc.want {
				t.Errorf("LevelForError = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLoggingInterceptor_DatabaseOutageLogsAtError is the incident itself: a
// repository failure must produce a record an ERROR-level alert rule matches,
// carrying the diagnostic and the request id needed to act on it.
func TestLoggingInterceptor_DatabaseOutageLogsAtError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	chain := observe.Chain(observe.Deps{Logger: logger})
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, svcerr.Wrap(errors.New(`ERROR: relation "items" does not exist (SQLSTATE 42P01)`))
	})
	for i := len(chain) - 1; i >= 0; i-- {
		next = chain[i].WrapUnary(next)
	}
	_, err := next(context.Background(), connect.NewRequest(&struct{}{}))
	if err == nil {
		t.Fatal("expected the handler error to propagate")
	}

	rec := findRecord(t, &buf, "rpc failed")
	if lvl, _ := rec["level"].(string); lvl != "ERROR" {
		t.Errorf("level = %q, want ERROR — a level=ERROR alert must fire during a database outage", lvl)
	}
	if cause, _ := rec["cause"].(string); !strings.Contains(cause, "42P01") {
		t.Errorf("cause = %q, want the SQLSTATE — the diagnostic must exist somewhere", cause)
	}
	if rid, _ := rec["request_id"].(string); rid == "" {
		t.Error("no request_id on the record — the cause cannot be tied to a request")
	}
}

// TestLoggingInterceptor_ClientErrorIsAUserError is the other half: a
// rejected request must NOT page anyone, and must say why it did not.
func TestLoggingInterceptor_ClientErrorIsAUserError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	chain := observe.Chain(observe.Deps{Logger: logger})
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, svcerr.Wrap(svcerr.NotFound("item"))
	})
	for i := len(chain) - 1; i >= 0; i-- {
		next = chain[i].WrapUnary(next)
	}
	_, _ = next(context.Background(), connect.NewRequest(&struct{}{}))

	rec := findRecord(t, &buf, "rpc failed")
	if lvl, _ := rec["level"].(string); lvl != "INFO" {
		t.Errorf("level = %q, want INFO — a 404 is the API working, not an incident", lvl)
	}
	if rec["error_class"] != "user" || rec["code"] != "not_found" {
		t.Errorf("error_class/code = %v/%v, want user/not_found", rec["error_class"], rec["code"])
	}
	if rec["error"] == nil {
		t.Error("a user error must still carry its error")
	}
}

func findRecord(t *testing.T, buf *bytes.Buffer, msg string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if got, _ := rec["msg"].(string); got == msg {
			return rec
		}
	}
	t.Fatalf("no %q record in:\n%s", msg, buf.String())
	return nil
}

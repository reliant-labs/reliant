package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// deadline_origin_test.go — whose deadline a DeadlineExceeded ran into. The
// chain-level proof (logging, audit, streams, caller timeouts over a real
// connect server) is pkg/serverkit's deadline_level_test.go; these pin the
// pieces.

// stall is a handler that waits out its context and fails the way a query
// does when its deadline lands mid-flight.
func stall(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
	<-ctx.Done()
	return nil, svcerr.Wrap(fmt.Errorf("update orders: %w", ctx.Err()))
}

func TestDeadlineSlotMarksItsOwnExpiry(t *testing.T) {
	for name, tc := range map[string]struct {
		slot Deadline
		want slog.Level
	}{
		"default":        {Deadline{Unary: 20 * time.Millisecond, SlowAfter: -1}, slog.LevelWarn},
		"error (opt-in)": {Deadline{Unary: 20 * time.Millisecond, SlowAfter: -1, ExpiryIsError: true}, slog.LevelError},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.slot.WrapUnary(stall)(context.Background(), connect.NewRequest(&emptypb.Empty{}))
			if connect.CodeOf(err) != connect.CodeDeadlineExceeded || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v: the mark must leave the code and errors.Is unchanged", err)
			}
			if got := LevelForError(err); got != tc.want {
				t.Errorf("LevelForError = %v, want %v", got, tc.want)
			}
			if got := deadlineOf(context.Background(), err).String(); got != DeadlineServer {
				t.Errorf("deadline = %q, want %q", got, DeadlineServer)
			}
		})
	}
}

func TestDeadlineSlotMarksTheCallersExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := Deadline{SlowAfter: -1}.WrapUnary(stall)(ctx, connect.NewRequest(&emptypb.Empty{}))
	if got := deadlineOf(context.Background(), err).String(); got != DeadlineCaller {
		t.Errorf("deadline = %q, want %q", got, DeadlineCaller)
	}
	if got := LevelForError(err); got != slog.LevelWarn {
		t.Errorf("LevelForError = %v, want WARN", got)
	}
}

// A DeadlineExceeded from the handler's own inner timeout, while the call's
// context is live, is neither the server's budget nor the caller's: it keeps
// the retryable WARN and carries no deadline attribute.
func TestInnerTimeoutHasNoDeadlineOrigin(t *testing.T) {
	inner := func(ctx context.Context, _ connect.AnyRequest) (connect.AnyResponse, error) {
		c, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		<-c.Done()
		return nil, svcerr.Wrap(fmt.Errorf("call billing: %w", c.Err()))
	}
	_, err := Deadline{SlowAfter: -1}.WrapUnary(inner)(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if o := deadlineOf(context.Background(), err); o != deadlineUnknown {
		t.Errorf("deadline = %q, want none", o)
	}
	if got := LevelForError(err); got != slog.LevelWarn {
		t.Errorf("LevelForError = %v, want WARN", got)
	}
}

// A layer inside the slot sees the error before the slot marks it, and reads
// the origin from its context: a component failing under the server's
// budget (ExpiryIsError) is an ERROR with deadline=server, as is
// LevelForErrorContext.
func TestInsideTheSlotTheContextTellsTheOrigin(t *testing.T) {
	var buf bytes.Buffer
	mw := LogMiddleware(slog.New(slog.NewJSONHandler(&buf, nil)), slog.LevelDebug)
	var levelInside slog.Level
	handler := func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		err := mw.WrapComponent(ctx, "orders.Archive", func(ctx context.Context) error {
			<-ctx.Done()
			return fmt.Errorf("update orders: %w", ctx.Err())
		})
		levelInside = LevelForErrorContext(ctx, err)
		return nil, err
	}
	_, _ = Deadline{Unary: 20 * time.Millisecond, SlowAfter: -1, ExpiryIsError: true}.WrapUnary(handler)(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if levelInside != slog.LevelError {
		t.Errorf("LevelForErrorContext inside the slot = %v, want ERROR", levelInside)
	}
	r := decodeLines(t, &buf)[0]
	if r["level"] != "ERROR" || r[DeadlineKey] != DeadlineServer {
		t.Errorf("component record = %v, want ERROR with deadline=server", r)
	}
}

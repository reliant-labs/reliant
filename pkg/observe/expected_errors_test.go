package observe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// expected_errors_test.go — an error can be an ANSWER, not a failure.
//
// A static-site origin asks its storage adapter for /favicon.ico, the bucket
// says "no such object", and the origin serves a 404. That is the adapter
// doing its job, yet the component log layer wrote every one at ERROR: 288
// ERROR records in five hours of one prod workspace proxy, from crawlers and
// SPA paths alone, burying the real failures beside them. An adapter
// declares which of its errors are expected outcomes in its owned
// observe_chain.go seam (WithExpectedErrors); those are logged like a
// success, and everything else stays at ERROR.

// errObjectNotFound stands in for an adapter's domain sentinel
// (staticorigin.ErrObjectNotFound, registryclient.ErrNotFound).
var errObjectNotFound = errors.New("staticorigin: object not found")

const readObject = "gcsread.ReadObject"

// seamChain builds the chain the scaffolded observe_chain.go builds — every
// standard layer, tracer and meter unset as in a process with no OTel SDK —
// with opts appended to LogMiddleware, the way a seam declares them.
func seamChain(logger *slog.Logger, opts ...LogOption) *ComponentChain {
	return NewComponentChain(
		RecoverMiddleware(logger),
		TraceMiddleware(nil),
		MetricsMiddleware(nil, "gcsread"),
		LogMiddleware(logger, slog.LevelDebug, opts...),
	)
}

// readThrough routes one call through chain exactly as the generated
// decorator (middleware_gen.go) does: chain.Around around the inner method.
func readThrough(chain *ComponentChain, inner func() (string, error)) (string, error) {
	return chain.Around(context.Background(), readObject, func(context.Context) (string, error) {
		return inner()
	})
}

func missing() (string, error) {
	return "", fmt.Errorf("read sites/acme/favicon.ico: %w", errObjectNotFound)
}

func refused() (string, error) {
	return "", errors.New("gcsread: 403 Forbidden: the pod's identity lacks storage.objects.get")
}

// TestLogMiddleware_ExpectedErrorLogsBelowError is the incident under a DEBUG
// handler (dev): the declared not-found is written at the seam's success
// level, marked expected and carrying its error, while an undeclared error is
// still an ERROR with its error. The caller gets both errors back untouched.
func TestLogMiddleware_ExpectedErrorLogsBelowError(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	chain := seamChain(jsonLogger(&buf, slog.LevelDebug), WithExpectedErrors(errObjectNotFound))

	if _, err := readThrough(chain, missing); !errors.Is(err, errObjectNotFound) {
		t.Fatalf("the chain must return the inner error unchanged, got %v", err)
	}
	got := records(t, &buf, readObject)
	if len(got) != 1 {
		t.Fatalf("want one record for the expected not-found, got %d:\n%s", len(got), buf.String())
	}
	if got[0]["level"] != "DEBUG" || got[0]["expected"] != true {
		t.Fatalf("a declared expected error must log at the success level with expected=true, got %v", got[0])
	}
	if got[0]["error"] != "read sites/acme/favicon.ico: staticorigin: object not found" {
		t.Errorf("the expected record must still carry its error, got %v", got[0]["error"])
	}

	buf.Reset()
	if _, err := readThrough(chain, refused); err == nil {
		t.Fatal("the chain swallowed a real failure")
	}
	got = records(t, &buf, readObject)
	if len(got) != 1 || got[0]["level"] != "ERROR" {
		t.Fatalf("an undeclared error must stay a single ERROR record, got %v", got)
	}
	if _, marked := got[0]["expected"]; marked {
		t.Errorf("a real failure must not be marked expected: %v", got[0])
	}
	if got[0]["error"] == nil {
		t.Errorf("a real failure must keep its error: %v", got[0])
	}
}

// TestLogMiddleware_ExpectedErrorsAreQuietInProduction is the prod shape: an
// INFO handler drops the DEBUG expected records entirely — 288 crawler 404s
// write nothing — and the one real failure among them is the only line.
func TestLogMiddleware_ExpectedErrorsAreQuietInProduction(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	chain := seamChain(jsonLogger(&buf, slog.LevelInfo), WithExpectedErrors(errObjectNotFound))

	for range 288 {
		_, _ = readThrough(chain, missing)
	}
	_, _ = readThrough(chain, refused)

	got := records(t, &buf, readObject)
	if len(got) != 1 || got[0]["level"] != "ERROR" {
		t.Fatalf("want exactly the real failure at ERROR, got %d records:\n%s", len(got), buf.String())
	}
}

// TestLogMiddleware_UndeclaredErrorsStayErrors pins the default: without a
// declaration, a not-found is a failure like any other — forge cannot know
// which of an adapter's errors are answers, so it never guesses.
func TestLogMiddleware_UndeclaredErrorsStayErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	chain := seamChain(jsonLogger(&buf, slog.LevelDebug))

	_, _ = readThrough(chain, missing)

	got := records(t, &buf, readObject)
	if len(got) != 1 || got[0]["level"] != "ERROR" {
		t.Fatalf("an undeclared not-found must log at ERROR, got %v", got)
	}
}

// TestLogMiddleware_ExpectedErrorsAreSampledApart: with sampling on, expected
// errors are sampled like successes — but in their own slot, so a method that
// mostly succeeds still shows its expected outcomes, and the reverse.
func TestLogMiddleware_ExpectedErrorsAreSampledApart(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	chain := seamChain(jsonLogger(&buf, slog.LevelDebug),
		withClock(clock.Now), WithSuccessSampling(testWindow), WithExpectedErrors(errObjectNotFound))

	found := func() (string, error) { return "<html>", nil }
	for range 50 {
		_, _ = readThrough(chain, found)
		_, _ = readThrough(chain, missing)
	}
	clock.Advance(testWindow)
	_, _ = readThrough(chain, missing)

	var successes, expected []map[string]any
	for _, rec := range records(t, &buf, readObject) {
		if rec["expected"] == true {
			expected = append(expected, rec)
		} else {
			successes = append(successes, rec)
		}
	}
	if len(successes) != 1 {
		t.Errorf("want one sampled success record, got %d", len(successes))
	}
	if len(expected) != 2 {
		t.Fatalf("want the first expected record, then one for the next window, got %d:\n%s", len(expected), buf.String())
	}
	if n, _ := expected[1]["suppressed"].(float64); n != 49 {
		t.Errorf("the second expected record must count the 49 it stands for, got suppressed=%v", expected[1]["suppressed"])
	}
}

// statusError is a typed vendor error a sentinel cannot express: whether it
// is an answer depends on a field.
type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("registry answered %d", e.code) }

// TestLogMiddleware_ExpectedErrorFunc: the predicate form classifies what a
// sentinel cannot, and declarations accumulate — a second option adds to the
// first instead of replacing it.
func TestLogMiddleware_ExpectedErrorFunc(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	isUnknownName := func(err error) bool {
		var se *statusError
		return errors.As(err, &se) && se.code == 404
	}
	chain := seamChain(jsonLogger(&buf, slog.LevelDebug),
		WithExpectedErrorFunc(isUnknownName), WithExpectedErrors(errObjectNotFound))

	_, _ = readThrough(chain, func() (string, error) { return "", &statusError{code: 404} })
	_, _ = readThrough(chain, missing)
	_, _ = readThrough(chain, func() (string, error) { return "", &statusError{code: 500} })

	got := records(t, &buf, readObject)
	if len(got) != 3 {
		t.Fatalf("want 3 records, got %d:\n%s", len(got), buf.String())
	}
	for i, wantLevel := range []string{"DEBUG", "DEBUG", "ERROR"} {
		if got[i]["level"] != wantLevel {
			t.Errorf("record %d: level %v, want %s (%v)", i, got[i]["level"], wantLevel, got[i])
		}
	}
}

// TestLoggingInterceptor_ExpectedErrors: LogOption is shared by both logging
// layers, so the declaration works at the RPC edge too. A poll that answers
// NotFound by design is written at the success level (INFO), not WARN, and
// stays "rpc failed" with its error — the client did get an error. An
// undeclared server fault is still ERROR.
func TestLoggingInterceptor_ExpectedErrors(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelDebug), WithExpectedErrors(svcerr.ErrNotFound)),
	}, func(procedure string) error {
		if procedure == pollProcedure {
			return svcerr.Wrap(svcerr.NotFound("draft"))
		}
		return svcerr.Wrap(svcerr.Internal("write failed"))
	})

	call(pollProcedure)
	call(otherProcedure)

	got := records(t, &buf, "rpc failed")
	if len(got) != 2 {
		t.Fatalf("want 2 \"rpc failed\" records, got %d:\n%s", len(got), buf.String())
	}
	byProc := map[any]map[string]any{got[0]["procedure"]: got[0], got[1]["procedure"]: got[1]}
	poll := byProc[pollProcedure]
	if poll["level"] != "INFO" || poll["expected"] != true || poll["error"] == nil {
		t.Errorf("a declared NotFound must log at INFO with expected=true and its error, got %v", poll)
	}
	if other := byProc[otherProcedure]; other["level"] != "ERROR" {
		t.Errorf("an undeclared Internal must stay ERROR, got %v", other)
	}
}

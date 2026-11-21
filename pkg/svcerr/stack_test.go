package svcerr_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/debug"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// stack_test.go — a server fault carries a bounded call stack; nothing a
// client caused does, and none of it ever reaches the wire.

// serverFaults are the constructors whose errors map to Internal, Unknown,
// DataLoss or Unavailable — the codes forge classifies as a server fault that
// pages.
var serverFaults = map[string]func(string) error{
	"Internal":    svcerr.Internal,
	"Unknown":     svcerr.Unknown,
	"DataLoss":    svcerr.DataLoss,
	"Unavailable": svcerr.Unavailable,
}

// notServerFaults are every other constructor: client faults, cancellations
// and the retryable codes that log at WARN. None of them captures a stack.
var notServerFaults = map[string]func(string) error{
	"Canceled":            svcerr.Canceled,
	"InvalidArgument":     svcerr.InvalidArgument,
	"DeadlineExceeded":    svcerr.DeadlineExceeded,
	"NotFound":            svcerr.NotFound,
	"AlreadyExists":       svcerr.AlreadyExists,
	"PermissionDenied":    svcerr.PermissionDenied,
	"ResourceExhausted":   svcerr.ResourceExhausted,
	"FailedPrecondition":  svcerr.FailedPrecondition,
	"Aborted":             svcerr.Aborted,
	"OutOfRange":          svcerr.OutOfRange,
	"Unimplemented":       svcerr.Unimplemented,
	"Unauthenticated":     svcerr.Unauthenticated,
	"PlanLimit":           svcerr.PlanLimit,
	"InsufficientBalance": svcerr.InsufficientBalance,
	"Expired":             svcerr.Expired,
	"Draining":            svcerr.Draining,
}

func TestStack_EveryConstructorIsClassified(t *testing.T) {
	t.Parallel()
	// The two tables together are the full constructor set, so a constructor
	// added later must be placed on one side or the other.
	for name := range constructors {
		_, fault := serverFaults[name]
		_, other := notServerFaults[name]
		if name == "ScaffoldStub" {
			continue // takes an rpc name; always Unimplemented
		}
		if fault == other {
			t.Errorf("constructor %s is in %v of the stack tables, want exactly one", name, map[bool]string{true: "both", false: "neither"}[fault])
		}
	}
}

func TestStack_ServerFaultConstructorsCapture(t *testing.T) {
	t.Parallel()
	for name, ctor := range serverFaults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ctor("it broke")
			st, ok := svcerr.StackOf(err)
			if !ok {
				t.Fatalf("%s carries no stack", name)
			}
			if !strings.HasPrefix(st.Trace, "goroutine ") {
				t.Errorf("trace is not in Go's standard format (no goroutine header):\n%s", st.Trace)
			}
			if !strings.Contains(st.Trace, "TestStack_ServerFaultConstructorsCapture") {
				t.Errorf("trace does not name the creating function:\n%s", st.Trace)
			}
			if !strings.Contains(st.Trace, "stack_test.go:") {
				t.Errorf("trace has no file:line for the creating function:\n%s", st.Trace)
			}
			// The stack starts where the application made the error, not in
			// this package or the runtime.
			frames := parseGoStack(t, st.Trace)
			if len(frames) == 0 {
				t.Fatalf("no frames parsed from:\n%s", st.Trace)
			}
			if strings.Contains(frames[0].function, "/svcerr.") || strings.HasPrefix(frames[0].function, "runtime.") {
				t.Errorf("top frame is plumbing, not the application: %s", frames[0].function)
			}
			for _, f := range frames {
				if strings.HasPrefix(f.function, "runtime.") || strings.Contains(f.function, "/pkg/svcerr.") {
					t.Errorf("trace carries a plumbing frame: %s", f.function)
				}
			}
			if !strings.Contains(st.Origin.Function, "TestStack_ServerFaultConstructorsCapture") {
				t.Errorf("origin = %+v, want the creating function", st.Origin)
			}
			if st.Origin.Line == 0 || !strings.HasSuffix(st.Origin.File, "stack_test.go") {
				t.Errorf("origin file:line = %s:%d", st.Origin.File, st.Origin.Line)
			}
		})
	}
}

func TestStack_UnclassifiedErrorCapturesWhereFirstWrapped(t *testing.T) {
	t.Parallel()
	// A driver error carries no stack of its own; the first place svcerr sees
	// it — the handler's Wrap — is the best origin there is.
	err := svcerr.Wrap(errors.New("ERROR: relation \"jobs\" does not exist (SQLSTATE 42P01)"))
	st, ok := svcerr.StackOf(err)
	if !ok {
		t.Fatal("an unrecognised error becomes Internal and must carry a stack")
	}
	if !strings.Contains(st.Origin.Function, "TestStack_UnclassifiedErrorCapturesWhereFirstWrapped") {
		t.Errorf("origin = %+v, want the function that wrapped it", st.Origin)
	}
	// The raw text is withheld from the client; the stack must not change that.
	if got := svcerr.ToConnect(err).Message(); got != svcerr.InternalMessage {
		t.Errorf("client message = %q, want %q", got, svcerr.InternalMessage)
	}
}

func TestStack_BareSentinelCapturesAtTheBoundary(t *testing.T) {
	t.Parallel()
	if _, ok := svcerr.StackOf(svcerr.ErrInternal); ok {
		t.Error("a bare sentinel is a shared value and cannot carry a stack")
	}
	st, ok := svcerr.StackOf(svcerr.Wrap(svcerr.ErrInternal))
	if !ok {
		t.Fatal("Wrap of a bare server-fault sentinel must capture at the boundary")
	}
	if !strings.Contains(st.Origin.Function, "TestStack_BareSentinelCapturesAtTheBoundary") {
		t.Errorf("origin = %+v", st.Origin)
	}
}

func TestStack_ClientClassErrorsNeverCapture(t *testing.T) {
	t.Parallel()
	for name, ctor := range notServerFaults {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ctor("nope")
			if _, ok := svcerr.StackOf(err); ok {
				t.Errorf("%s carries a stack", name)
			}
			if _, ok := svcerr.StackOf(svcerr.Wrap(err)); ok {
				t.Errorf("Wrap(%s) carries a stack", name)
			}
			if _, ok := svcerr.StackOf(svcerr.WrapOr(err, svcerr.NotFound("x"))); ok {
				t.Errorf("WrapOr(%s) carries a stack", name)
			}
		})
	}
	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		for _, err := range []error{context.Canceled, fmt.Errorf("query: %w", context.Canceled), context.DeadlineExceeded} {
			if _, ok := svcerr.StackOf(svcerr.Wrap(err)); ok {
				t.Errorf("Wrap(%v) carries a stack", err)
			}
		}
	})
	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		if _, ok := svcerr.StackOf(nil); ok {
			t.Error("nil carries a stack")
		}
	})
	t.Run("a server fault the application declares a user problem", func(t *testing.T) {
		t.Parallel()
		// Unavailable is a server fault by kind; WithClass says this one is
		// the user's machine being offline.
		err := svcerr.WithClass(svcerr.Unavailable("your machine is not connected"), svcerr.ClassUser)
		if _, ok := svcerr.StackOf(err); ok {
			t.Error("a user-class error must not emit a stack")
		}
		if _, ok := svcerr.StackOf(svcerr.Wrap(err)); ok {
			t.Error("a user-class error must not emit a stack after Wrap")
		}
	})
	t.Run("a client verdict over a server-fault cause", func(t *testing.T) {
		t.Parallel()
		// The client reads NotFound; a cause deeper down is a server fault
		// that never reaches them. The outer verdict decides.
		err := svcerr.WithCause(svcerr.NotFound("job"), svcerr.Internal("db"))
		if _, ok := svcerr.StackOf(svcerr.Wrap(err)); ok {
			t.Error("the outer 4xx verdict must decide: no stack")
		}
	})
}

func TestStack_ClientClassErrorsAllocateNothingExtra(t *testing.T) {
	// The cost of a stack is paid by server faults only: a client-class
	// constructor allocates what it always did (its detailError), and a
	// server-fault one adds the captured program counters.
	client := testing.AllocsPerRun(200, func() { sink = svcerr.InvalidArgument("bad field") })
	server := testing.AllocsPerRun(200, func() { sink = svcerr.Internal("db failed") })
	if client > 1 {
		t.Errorf("InvalidArgument allocates %.0f, want 1 (no stack)", client)
	}
	if server > client+1 {
		t.Errorf("Internal allocates %.0f, want at most one more than a client error (%.0f)", server, client)
	}
	wrapClient := testing.AllocsPerRun(200, func() { sink = svcerr.Wrap(svcerr.ErrNotFound) })
	wrapClientBase := testing.AllocsPerRun(200, func() { sink = svcerr.ToConnect(svcerr.ErrNotFound) })
	if wrapClient > wrapClientBase {
		t.Errorf("Wrap of a client error allocates %.0f, ToConnect %.0f", wrapClient, wrapClientBase)
	}
}

var sink error

//go:noinline
func originA() error { return svcerr.Internal("get failed") }

//go:noinline
func originB() error { return svcerr.Internal("get failed") }

func TestStack_EarliestCaptureWins(t *testing.T) {
	t.Parallel()
	// The service layer made the error; the handler labelled it. The stack
	// that matters is where the failure began, not where it was relabelled.
	inner := fmt.Errorf("load job: %w", originA())
	for name, err := range map[string]error{
		"wrapped by Wrap":       svcerr.Wrap(inner),
		"relabelled via cause":  svcerr.Wrap(svcerr.WithCause(originB(), inner)),
		"relabelled by WrapOr":  svcerr.WrapOr(inner, originB()),
		"fmt wrapped then Wrap": svcerr.Wrap(fmt.Errorf("handler: %w", inner)),
	} {
		st, ok := svcerr.StackOf(err)
		if !ok {
			t.Errorf("%s: no stack", name)
			continue
		}
		if !strings.Contains(st.Origin.Function, "originA") {
			t.Errorf("%s: origin = %s, want originA (the first wrap)", name, st.Origin.Function)
		}
	}
	// Wrapping twice does not capture twice.
	once := svcerr.Wrap(originB())
	twice := svcerr.Wrap(once)
	a, _ := svcerr.StackOf(once)
	b, _ := svcerr.StackOf(twice)
	if a.Trace != b.Trace {
		t.Error("a second Wrap changed the captured stack")
	}
}

//go:noinline
func recurse(n int, f func() error) error {
	if n == 0 {
		return f()
	}
	return recurse(n-1, f)
}

func TestStack_IsBounded(t *testing.T) {
	t.Parallel()
	err := recurse(500, func() error { return svcerr.Internal("deep") })
	st, ok := svcerr.StackOf(err)
	if !ok {
		t.Fatal("no stack")
	}
	frames := parseGoStack(t, st.Trace)
	if len(frames) > svcerr.MaxStackFrames {
		t.Errorf("%d frames, want at most %d", len(frames), svcerr.MaxStackFrames)
	}
	if len(frames) < svcerr.MaxStackFrames-4 {
		t.Errorf("only %d frames of a 500-deep stack kept, want about %d", len(frames), svcerr.MaxStackFrames)
	}
	if len(st.Trace) > svcerr.MaxStackBytes {
		t.Errorf("trace is %d bytes, want at most %d", len(st.Trace), svcerr.MaxStackBytes)
	}
	if !strings.Contains(st.Trace, "...additional frames elided...") {
		t.Errorf("a cut stack must say so, as Go's own does:\n%s", st.Trace)
	}
	// The innermost frames — where it happened — are the ones kept.
	if !strings.Contains(frames[0].function, "TestStack_IsBounded") {
		t.Errorf("top frame = %s", frames[0].function)
	}

	short, _ := svcerr.StackOf(svcerr.Internal("shallow"))
	if strings.Contains(short.Trace, "elided") {
		t.Errorf("an uncut stack must not claim to be cut:\n%s", short.Trace)
	}
}

func TestStack_FormatMatchesGoStandard(t *testing.T) {
	t.Parallel()
	st, _ := svcerr.StackOf(svcerr.Internal("x"))
	lines := strings.Split(strings.TrimRight(st.Trace, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "goroutine ") || !strings.HasSuffix(lines[0], "[running]:") {
		t.Errorf("header = %q", lines[0])
	}
	frames := parseGoStack(t, st.Trace)
	for _, f := range frames {
		if f.file == "" || f.line == 0 {
			t.Errorf("frame %q has no file:line", f.function)
		}
	}
}

func TestStack_PathsAreModuleRelativeAndNeverAbsolute(t *testing.T) {
	t.Parallel()
	bi, ok := debug.ReadBuildInfo()
	if !ok || bi.Main.Path == "" {
		t.Skip("no main module in the build info: nothing to be relative to")
	}
	st, _ := svcerr.StackOf(svcerr.Internal("x"))
	wd, _ := os.Getwd()
	if wd != "" && strings.Contains(st.Trace, wd) {
		t.Errorf("trace carries the absolute build path %q:\n%s", wd, st.Trace)
	}
	if !strings.Contains(st.Trace, "\tpkg/svcerr/stack_test.go:") {
		t.Errorf("a module frame's path is repo-relative (pkg/svcerr/stack_test.go):\n%s", st.Trace)
	}
	if st.Origin.File != "pkg/svcerr/stack_test.go" {
		t.Errorf("origin file = %q, want pkg/svcerr/stack_test.go", st.Origin.File)
	}
}

type goFrame struct {
	function, file string
	line           int
}

// parseGoStack reads runtime/debug.Stack's layout the way the control plane's
// issue view does: a pair of lines per frame, "pkg.Func(args)" then
// "\tfile:LINE +0xOFF".
func parseGoStack(t *testing.T, trace string) []goFrame {
	t.Helper()
	lines := strings.Split(trace, "\n")
	var out []goFrame
	for i := 0; i+1 < len(lines); i++ {
		fn, loc := lines[i], lines[i+1]
		if strings.HasPrefix(fn, "goroutine ") || fn == "" || strings.HasPrefix(fn, "\t") || !strings.HasPrefix(loc, "\t") {
			continue
		}
		i++
		if j := strings.LastIndex(fn, "("); j > 0 && strings.HasSuffix(fn, ")") {
			fn = fn[:j]
		}
		file, _, _ := strings.Cut(strings.TrimSpace(loc), " +0x")
		f := goFrame{function: fn}
		if k := strings.LastIndex(file, ":"); k > 0 {
			fmt.Sscanf(file[k+1:], "%d", &f.line)
			f.file = file[:k]
		}
		out = append(out, f)
	}
	return out
}

// TestStack_NeverCrossesTheWire is the guard. The stack is for the operator:
// it names functions and files of a server the client is not meant to map. It
// must not appear in the body, the headers, the trailers, the error metadata
// or the details of the response — for a deliberate Internal, an unrecognised
// driver error, and a WithCause relabel alike.
func TestStack_NeverCrossesTheWire(t *testing.T) {
	t.Parallel()
	cases := map[string]error{
		"Internal":          svcerr.Internal("create order failed"),
		"driver error":      errors.New("ERROR: relation \"jobs\" does not exist"),
		"WithCause":         svcerr.WithCause(svcerr.Internal("create order failed"), errors.New("pg down")),
		"WrapOr fallback":   svcerr.WrapOr(errors.New("pg down"), svcerr.Internal("list failed")),
		"already wrapped":   svcerr.Wrap(svcerr.Unavailable("billing is unavailable")),
		"with reason+class": svcerr.WithReason(svcerr.Internal("x"), "some_reason"),
	}
	for name, handlerErr := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := svcerr.StackOf(svcerr.Wrap(handlerErr)); !ok {
				t.Fatalf("precondition: the server-side error has no stack to leak")
			}
			const procedure = "/svcerr.v1.Svc/Do"
			mux := http.NewServeMux()
			mux.Handle(procedure, connect.NewUnaryHandler(procedure,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return nil, handlerErr
				}, connect.WithInterceptors(svcerr.Interceptor())))
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			// Raw HTTP: whatever the server writes, byte for byte.
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+procedure, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			wire := fmt.Sprintf("%v\n%v\n%s", resp.Header, resp.Trailer, body)
			for _, leak := range []string{"stack_test.go", "goroutine", "pkg/svcerr", "TestStack_NeverCrossesTheWire", "stacktrace", "+0x"} {
				if strings.Contains(wire, leak) {
					t.Errorf("response carries %q:\n%s", leak, wire)
				}
			}

			// Through the Connect client: code, message, metadata and details.
			client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
			_, err = client.CallUnary(t.Context(), connect.NewRequest(&emptypb.Empty{}))
			var ce *connect.Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *connect.Error", err)
			}
			if got := len(ce.Details()); got != 0 {
				t.Errorf("error carries %d details", got)
			}
			for k, v := range ce.Meta() {
				if strings.Contains(strings.ToLower(k+strings.Join(v, ",")), "stack") {
					t.Errorf("metadata %s = %v", k, v)
				}
			}
			if strings.Contains(ce.Message(), "stack_test.go") || strings.Contains(ce.Message(), "goroutine") {
				t.Errorf("message carries the stack: %q", ce.Message())
			}
		})
	}
}

func BenchmarkConstructClientError(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink = svcerr.InvalidArgument("bad field")
	}
}

func BenchmarkConstructServerError(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink = svcerr.Internal("db failed")
	}
}

func BenchmarkWrapClientError(b *testing.B) {
	err := svcerr.NotFound("job")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink = svcerr.Wrap(err)
	}
}

func BenchmarkWrapUnrecognisedError(b *testing.B) {
	err := errors.New("ERROR: relation \"jobs\" does not exist")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		sink = svcerr.Wrap(err)
	}
}

// BenchmarkStackOfCold is what the operator path pays once per REPORTED
// server fault: resolving a full-depth stack's program counters to text.
func BenchmarkStackOfCold(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		err := recurse(40, func() error { return svcerr.Internal("deep") })
		if _, ok := svcerr.StackOf(err); !ok {
			b.Fatal("no stack")
		}
	}
}

// BenchmarkStackOfWarm is every report after the first of the same error: the
// log record, the log.error event and the span event of one failing request.
func BenchmarkStackOfWarm(b *testing.B) {
	err := recurse(40, func() error { return svcerr.Internal("deep") })
	svcerr.StackOf(err)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, ok := svcerr.StackOf(err); !ok {
			b.Fatal("no stack")
		}
	}
}

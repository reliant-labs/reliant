package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// log_sampling_test.go — an access log's volume is the traffic's volume.
//
// The logging interceptor writes one INFO "rpc completed" record per
// successful RPC by default. A frontend polling three RPCs every couple of
// seconds turns that into 47k lines in twelve hours of one dev stack, so a
// deployment can turn success SAMPLING on; when it does, successes are
// sampled per procedure and failures and slow calls never are. These tests
// pin the sampling mechanism; what the default is, and that only the
// caller's option sets the window, is log_sampling_config_test.go.

const (
	pollProcedure  = "/observe.test.v1.PollService/Poll"
	otherProcedure = "/observe.test.v1.PollService/Other"
)

// testWindow is the window the sampling tests opt into.
const testWindow = time.Minute

// fakeClock is a manually advanced time source shared by the policy and the
// test handler, so a test can make a call "slow" or step past the sampling
// window without sleeping.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// rpcEdge mounts a Connect handler for each procedure behind the given
// interceptors. handle runs inside the handler, so a test can fail the call
// or advance a fake clock mid-call.
func rpcEdge(t *testing.T, interceptors []connect.Interceptor, handle func(procedure string) error) func(procedure string) {
	t.Helper()
	mux := http.NewServeMux()
	for _, proc := range []string{pollProcedure, otherProcedure} {
		mux.Handle(proc, connect.NewUnaryHandler(
			proc,
			func(_ context.Context, req *connect.Request[structpb.Struct]) (*connect.Response[structpb.Struct], error) {
				if handle != nil {
					if err := handle(proc); err != nil {
						return nil, err
					}
				}
				return connect.NewResponse(req.Msg), nil
			},
			connect.WithInterceptors(interceptors...),
		))
	}
	return func(procedure string) {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, procedure, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}
}

// records parses every JSON log line whose msg is msg.
func records(t *testing.T, buf *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		if rec["msg"] == msg {
			out = append(out, rec)
		}
	}
	return out
}

func jsonLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

// TestLoggingInterceptor_RepeatedSuccessesAreSampled is the incident, with
// sampling on: a polled procedure, called far more often than anyone reads
// its log line, must not write one INFO record per call.
func TestLoggingInterceptor_RepeatedSuccessesAreSampled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelDebug), WithSuccessSampling(testWindow)),
	}, nil)

	for range 200 {
		call(pollProcedure)
	}

	got := records(t, &buf, "rpc completed")
	if len(got) != 1 {
		t.Fatalf("200 successful polls wrote %d \"rpc completed\" records, want 1 — "+
			"an unsampled access log grows with traffic and buries every other line", len(got))
	}
	if got[0]["procedure"] != pollProcedure || got[0]["level"] != "INFO" {
		t.Errorf("the sampled record must keep the old shape at INFO, got %v", got[0])
	}
}

// TestLoggingInterceptor_SampledRecordCountsSuppressedCalls: sampling must not
// make traffic invisible. The next record after the window says how many
// successes it stands for, so the rate survives in the log alone.
func TestLoggingInterceptor_SampledRecordCountsSuppressedCalls(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo), withClock(clock.Now), WithSuccessSampling(testWindow)),
	}, nil)

	call(pollProcedure) // first success of a procedure: always written
	for range 9 {
		call(pollProcedure)
	}
	call(otherProcedure) // sampling is per procedure, not global
	clock.Advance(testWindow)
	call(pollProcedure)

	var poll []map[string]any
	for _, rec := range records(t, &buf, "rpc completed") {
		if rec["procedure"] == pollProcedure {
			poll = append(poll, rec)
		}
	}
	if len(poll) != 2 {
		t.Fatalf("want 2 records for %s (first call, then the next window), got %d: %v", pollProcedure, len(poll), poll)
	}
	if n, _ := poll[0]["suppressed"].(float64); n != 0 {
		t.Errorf("first record suppressed = %v, want 0", poll[0]["suppressed"])
	}
	if n, _ := poll[1]["suppressed"].(float64); n != 9 {
		t.Errorf("second record suppressed = %v, want 9 — the 9 unlogged calls between the two records", poll[1]["suppressed"])
	}
	if len(records(t, &buf, "rpc completed")) != 3 {
		t.Errorf("a different procedure must get its own first record:\n%s", buf.String())
	}
}

// TestLoggingInterceptor_FailuresAreNeverSampled: every failure is a record,
// with full fields, at the level LevelForError picks. Sampling applies to
// successes only.
func TestLoggingInterceptor_FailuresAreNeverSampled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo), WithSuccessSampling(testWindow))},
		func(string) error { return svcerr.Wrap(svcerr.NotFound("item")) })

	for range 20 {
		call(pollProcedure)
	}

	got := records(t, &buf, "rpc failed")
	if len(got) != 20 {
		t.Fatalf("20 failed calls wrote %d \"rpc failed\" records, want 20", len(got))
	}
	for _, rec := range got {
		if rec["level"] != "INFO" || rec["error"] == nil || rec["procedure"] != pollProcedure {
			t.Fatalf("failure record lost its fields: %v", rec)
		}
		if _, sampled := rec["suppressed"]; sampled {
			t.Fatalf("failure record carries a sampling count: %v", rec)
		}
	}
}

// TestLoggingInterceptor_SlowSuccessIsAlwaysLogged: a slow call is an anomaly
// and is never folded into a sample — not inside a window, and not when the
// procedure's successes were turned down to DEBUG.
func TestLoggingInterceptor_SlowSuccessIsAlwaysLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	var slow atomic.Bool
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo),
			withClock(clock.Now),
			WithSuccessSampling(testWindow),
			WithSuccessLevel(otherProcedure, slog.LevelDebug)),
	}, func(string) error {
		if slow.Load() {
			clock.Advance(DefaultSlowThreshold)
		}
		return nil
	})

	call(pollProcedure) // sampled record
	call(pollProcedure) // suppressed
	slow.Store(true)
	call(pollProcedure)  // slow: written despite the open window
	call(otherProcedure) // slow: written at INFO despite a DEBUG success level

	got := records(t, &buf, "rpc completed")
	if len(got) != 3 {
		t.Fatalf("want the sampled record plus 2 slow records, got %d:\n%s", len(got), buf.String())
	}
	for _, rec := range got[1:] {
		if rec["slow"] != true || rec["level"] != "INFO" {
			t.Errorf("slow call must be logged at INFO with slow=true, got %v", rec)
		}
		if _, sampled := rec["suppressed"]; sampled {
			t.Errorf("slow record is not a sample and must not carry a count: %v", rec)
		}
	}
}

// TestLoggingInterceptor_SuccessLevelOverride is the per-procedure seam: an
// app can turn one procedure's successes down (a poll) without touching the
// rest.
func TestLoggingInterceptor_SuccessLevelOverride(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo), WithSuccessLevel(pollProcedure, slog.LevelDebug)),
	}, nil)

	call(pollProcedure)
	call(otherProcedure)

	got := records(t, &buf, "rpc completed")
	if len(got) != 1 || got[0]["procedure"] != otherProcedure {
		t.Fatalf("want only %s logged under an INFO handler, got %v", otherProcedure, got)
	}
}

// TestLoggingInterceptor_SamplingCanBeDisabled: WithSuccessSampling(0) keeps
// one record per success, in the unsampled shape, even after an earlier
// option turned sampling on — the last option wins, as for every LogOption.
func TestLoggingInterceptor_SamplingCanBeDisabled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	call := rpcEdge(t, []connect.Interceptor{
		LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo), WithSuccessSampling(time.Hour), WithSuccessSampling(0)),
	}, nil)

	for range 5 {
		call(pollProcedure)
	}

	got := records(t, &buf, "rpc completed")
	if len(got) != 5 {
		t.Fatalf("with sampling off, 5 calls must write 5 records, got %d", len(got))
	}
	if _, ok := got[0]["suppressed"]; ok {
		t.Errorf("with sampling off the record keeps its original shape, got %v", got[0])
	}
}

// TestChain_PassesLogOptions: apps build the interceptor through Chain or
// DefaultMiddlewares, so the seam must reach it from there. The option
// turns sampling ON — the opposite of the default — so it is visible only if
// it arrived.
func TestChain_PassesLogOptions(t *testing.T) {
	t.Parallel()
	opts := []LogOption{WithSuccessSampling(testWindow)}
	for name, build := range map[string]func(*slog.Logger) []connect.Interceptor{
		"Chain": func(l *slog.Logger) []connect.Interceptor {
			return Chain(Deps{Logger: l, LogOptions: opts})
		},
		"DefaultMiddlewares": func(l *slog.Logger) []connect.Interceptor {
			return DefaultMiddlewares(DefaultMiddlewareDeps{Logger: l, LogOptions: opts})
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			call := rpcEdge(t, build(jsonLogger(&buf, slog.LevelInfo)), nil)
			for range 3 {
				call(pollProcedure)
			}
			if got := len(records(t, &buf, "rpc completed")); got != 1 {
				t.Fatalf("LogOptions did not reach the logging interceptor: %d records, want 1", got)
			}
		})
	}
}

// TestLoggingInterceptor_StreamCompletionsAreSampled: a client reconnecting a
// stream in a tight loop floods exactly like a poll. Same policy, minus the
// slow rule — a stream's duration is its session length, not latency.
func TestLoggingInterceptor_StreamCompletionsAreSampled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	icep := LoggingInterceptor(jsonLogger(&buf, slog.LevelInfo), withClock(clock.Now), WithSuccessSampling(testWindow))
	wrapped := icep.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		clock.Advance(time.Hour) // a long session is not "slow"
		return nil
	})

	conn := &fakeStreamConn{procedure: "/observe.test.v1.PollService/Watch"}
	for range 3 {
		if err := wrapped(context.Background(), conn); err != nil {
			t.Fatal(err)
		}
	}

	got := records(t, &buf, "stream completed")
	// Each session advances the clock an hour, so every completion opens a
	// new window — none is suppressed and none is flagged slow.
	if len(got) != 3 {
		t.Fatalf("want 3 stream records, got %d", len(got))
	}
	for _, rec := range got {
		if _, slow := rec["slow"]; slow {
			t.Errorf("a stream's duration must not mark it slow: %v", rec)
		}
	}

	// The third session's record opened a window at its END; step past it so
	// the reconnect burst starts a fresh one.
	buf.Reset()
	clock.Advance(testWindow)
	quick := icep.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error { return nil })
	for range 10 {
		_ = quick(context.Background(), conn)
	}
	if got := records(t, &buf, "stream completed"); len(got) != 1 {
		t.Fatalf("10 reconnects inside one window wrote %d records, want 1", len(got))
	}
}

// TestSuccessSampler_ConcurrentAccounting: under contention every success is
// either written or counted in exactly one later record — none is lost or
// double-counted.
func TestSuccessSampler_ConcurrentAccounting(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	p := newLogPolicy(slog.LevelInfo, []LogOption{withClock(clock.Now), WithSuccessSampling(testWindow)})

	const workers, perWorker = 16, 250
	var emitted atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perWorker {
				if _, ok := p.admit(pollProcedure); ok {
					emitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if emitted.Load() != 1 {
		t.Fatalf("one window admitted %d records, want 1", emitted.Load())
	}

	clock.Advance(testWindow)
	suppressed, ok := p.admit(pollProcedure)
	if !ok || suppressed != workers*perWorker-1 {
		t.Fatalf("next window = (%d, %v), want (%d, true)", suppressed, ok, workers*perWorker-1)
	}
}

// TestSuccessSampling_WindowResolution pins where a layer's window comes
// from: the options its caller passed — the last WithSuccessSampling wins —
// else none.
func TestSuccessSampling_WindowResolution(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts []LogOption
		want time.Duration
	}{
		{name: "nothing set", want: 0},
		{name: "window", opts: []LogOption{WithSuccessSampling(2 * time.Minute)}, want: 2 * time.Minute},
		{name: "zero", opts: []LogOption{WithSuccessSampling(0)}, want: 0},
		{name: "negative", opts: []LogOption{WithSuccessSampling(-time.Minute)}, want: -time.Minute},
		{name: "last option wins", opts: []LogOption{WithSuccessSampling(time.Hour), WithSuccessSampling(0)}, want: 0},
		{name: "nil options are skipped", opts: []LogOption{nil, WithSuccessSampling(time.Hour), nil}, want: time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := newLogPolicy(slog.LevelInfo, c.opts).window; got != c.want {
				t.Fatalf("window = %v, want %v", got, c.want)
			}
		})
	}
}

// fakeStreamConn is the minimum StreamingHandlerConn the logging interceptor
// reads: a procedure and request headers.
type fakeStreamConn struct {
	connect.StreamingHandlerConn
	procedure string
}

func (c *fakeStreamConn) Spec() connect.Spec          { return connect.Spec{Procedure: c.procedure} }
func (c *fakeStreamConn) RequestHeader() http.Header  { return http.Header{} }
func (c *fakeStreamConn) ResponseHeader() http.Header { return http.Header{} }

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

// attemptCaller records every attempt call, as the JSON a control plane
// would receive.
type attemptCaller struct {
	mu      sync.Mutex
	calls   []string
	bodies  []map[string]any
	openErr error
}

func (c *attemptCaller) Call(_ context.Context, procedure string, req, _ any) error {
	raw, _ := json.Marshal(req)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, procedure)
	c.bodies = append(c.bodies, body)
	if procedure == procOpenDeployAttempt {
		return c.openErr
	}
	return nil
}

func (c *attemptCaller) last(procedure string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.calls) - 1; i >= 0; i-- {
		if c.calls[i] == procedure {
			return c.bodies[i]
		}
	}
	return nil
}

func (c *attemptCaller) count(procedure string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, p := range c.calls {
		if p == procedure {
			n++
		}
	}
	return n
}

// newTestAttempt is beginDeployAttempt without the output capture, which
// swaps the process's os.Stdout and cannot run beside parallel tests.
func newTestAttempt(env, mode string, warn io.Writer) (context.Context, *deployAttempt) {
	a := &deployAttempt{env: env, mode: mode, source: "terminal", warn: warn, nudge: make(chan struct{}, 1)}
	return context.WithValue(context.Background(), deployAttemptCtxKey{}, a), a
}

func hostedTestLedger(c cloudCaller) envLedger {
	return envLedger{Bindings: &hostedStore{client: c, project: "shop"}, Hosted: true}
}

func TestDeployAttemptModeSkipsInvocationsThatWriteNothing(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		f    deployCmdFlags
		want string
	}{
		"apply":          {deployCmdFlags{}, "apply"},
		"plan-only":      {deployCmdFlags{promote: promoteCmdFlags{planOnly: true}}, "plan"},
		"explain":        {deployCmdFlags{explain: true}, ""},
		"dry-run":        {deployCmdFlags{dryRun: true}, ""},
		"plan preview":   {deployCmdFlags{promote: promoteCmdFlags{plan: true}}, ""},
		"offline ledger": {deployCmdFlags{offlineLedger: true}, ""},
	}
	for name, tc := range cases {
		if got := deployAttemptMode(tc.f); got != tc.want {
			t.Errorf("%s: mode = %q, want %q", name, got, tc.want)
		}
	}
}

func TestDeploySource(t *testing.T) {
	t.Parallel()
	if got := deploySource("console", "github:a/b/1/1"); got != "console" {
		t.Errorf("a declared source wins: %q", got)
	}
	if got := deploySource("", "github:a/b/1/1"); got != "ci" {
		t.Errorf("a detected run is CI: %q", got)
	}
	if got := deploySource("bogus", ""); got != "terminal" {
		t.Errorf("an unknown declaration falls back: %q", got)
	}
}

// The whole lifecycle: opened on the hosted ledger, the phases, release and
// promotion it learns reported, and the outcome closed with the error and
// the --json document.
func TestDeployAttemptRecordsTheInvocation(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)

	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	if c.count(procOpenDeployAttempt) != 1 {
		t.Fatalf("open calls = %d, want 1", c.count(procOpenDeployAttempt))
	}
	open := c.last(procOpenDeployAttempt)
	if open["environmentName"] != "prod" || open["project"] != "shop" || open["mode"] != "apply" {
		t.Errorf("open = %v", open)
	}
	// A second resolution of the same env (forge resolves the ledger more
	// than once per deploy) opens nothing new.
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	if c.count(procOpenDeployAttempt) != 1 {
		t.Errorf("re-resolving the ledger re-opened the attempt")
	}

	noteDeployPhase(ctx, "build")
	noteDeployRelease(ctx, "20261026.010203")
	noteDeployPhase(ctx, "promote")
	noteDeployPromotion(ctx, "prom-1")
	a.appendOutput(&a.pending, []byte("building api\n"), attemptLogLimit)
	a.report.WriteString(`{"mode":"apply"}`)

	a.finish(ctx, &deployQueuedError{})
	closeReq := c.last(procCloseDeployAttempt)
	if closeReq == nil {
		t.Fatal("the attempt was never closed")
	}
	if closeReq["outcome"] != attemptOutcomeQueued {
		t.Errorf("outcome = %v, want queued for exit 7", closeReq["outcome"])
	}
	if closeReq["releaseVersion"] != "20261026.010203" || closeReq["promotionId"] != "prom-1" {
		t.Errorf("close = %v", closeReq)
	}
	if closeReq["reportJson"] != `{"mode":"apply"}` {
		t.Errorf("report = %v", closeReq["reportJson"])
	}
	if !strings.Contains(closeReq["logAppend"].(string)+allReported(c), "building api") {
		t.Errorf("the output never reached the control plane")
	}
}

// allReported concatenates every heartbeat's log chunk.
func allReported(c *attemptCaller) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for i, p := range c.calls {
		if p == procReportDeployAttempt {
			if s, ok := c.bodies[i]["logAppend"].(string); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// A failure names the phase it happened in.
func TestDeployAttemptFailureNamesThePhase(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	noteDeployPhase(ctx, "build")
	a.finish(ctx, errors.New("docker build exited 1"))
	closeReq := c.last(procCloseDeployAttempt)
	if closeReq["outcome"] != attemptOutcomeFailed || closeReq["failedPhase"] != "build" {
		t.Errorf("close = %v", closeReq)
	}
	if closeReq["errorMessage"] != "docker build exited 1" {
		t.Errorf("error = %v", closeReq["errorMessage"])
	}
}

// A machine-ledger env, or another env's ledger, records nothing.
func TestDeployAttemptOnlyOpensOnItsOwnHostedEnv(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	attachDeployAttempt(ctx, "staging", hostedTestLedger(c))
	attachDeployAttempt(ctx, "prod", envLedger{})
	a.finish(ctx, nil)
	if len(c.calls) != 0 {
		t.Errorf("calls = %v, want none", c.calls)
	}
}

// A control plane that predates attempts is skipped silently, and the
// deploy's outcome is never sent anywhere.
func TestDeployAttemptOldControlPlaneIsSilent(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{openErr: &cloud.Error{HTTPStatus: 404, Code: cloud.CodeUnimplemented}}
	var warn strings.Builder
	ctx, a := newTestAttempt("prod", "apply", &warn)
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	a.finish(ctx, nil)
	if warn.Len() != 0 {
		t.Errorf("warned %q for a control plane that keeps no attempts", warn.String())
	}
	if c.count(procCloseDeployAttempt) != 0 {
		t.Errorf("closed an attempt that never opened")
	}
}

// Any other failure to open is ONE line, and the deploy carries on.
func TestDeployAttemptOpenFailureWarnsOnce(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{openErr: errors.New("connection refused")}
	var warn strings.Builder
	ctx, a := newTestAttempt("prod", "apply", &warn)
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	a.finish(ctx, nil)
	if got := strings.Count(warn.String(), "\n"); got != 1 || !strings.Contains(warn.String(), "deploy itself is unaffected") {
		t.Errorf("warn = %q", warn.String())
	}
}

// A nil attempt (an invocation that writes nothing) is inert everywhere.
func TestDeployAttemptNilIsInert(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	noteDeployPhase(ctx, "build")
	noteDeployPromotion(ctx, "p")
	attachDeployAttempt(ctx, "prod", hostedTestLedger(&attemptCaller{}))
	var a *deployAttempt
	a.finish(ctx, nil)
}

func TestAttemptOutcomeOf(t *testing.T) {
	t.Parallel()
	if o, code := attemptOutcomeOf(context.Background(), nil); o != attemptOutcomeSucceeded || code != 0 {
		t.Errorf("nil = %s %d", o, code)
	}
	if o, _ := attemptOutcomeOf(context.Background(), context.Canceled); o != attemptOutcomeCancelled {
		t.Errorf("canceled = %s", o)
	}
	if o, code := attemptOutcomeOf(context.Background(), exitCodeError{code: 3}); o != attemptOutcomeFailed || code != 3 {
		t.Errorf("exit 3 = %s %d", o, code)
	}
}

// The tee passes every byte through to the real stream AND keeps the tail,
// and restoring drains the pipe before the stream is put back.
func TestDeployAttemptTeeKeepsTheTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real, err := os.Create(dir + "/out")
	if err != nil {
		t.Fatal(err)
	}
	defer real.Close()
	stream := real
	a := &deployAttempt{}
	a.tee(&stream, func(p []byte) { a.appendOutput(&a.pending, p, 8) })
	if stream == real {
		t.Fatal("a non-terminal stream was not captured")
	}
	_, _ = stream.WriteString("0123456789abcdef")
	a.release1()
	if stream != real {
		t.Fatal("the stream was not restored")
	}
	got, _ := os.ReadFile(dir + "/out")
	if string(got) != "0123456789abcdef" {
		t.Errorf("passed through %q", got)
	}
	if a.pending.String() != "89abcdef" {
		t.Errorf("kept %q, want the 8-byte tail", a.pending.String())
	}
}

// Heartbeats carry the phase without waiting for the ticker: a phase change
// nudges one out.
func TestDeployAttemptPhaseChangeNudgesAHeartbeat(t *testing.T) {
	t.Parallel()
	c := &attemptCaller{}
	ctx, a := newTestAttempt("prod", "apply", io.Discard)
	attachDeployAttempt(ctx, "prod", hostedTestLedger(c))
	noteDeployPhase(ctx, "build")
	deadline := time.Now().Add(5 * time.Second)
	for c.count(procReportDeployAttempt) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	a.finish(ctx, nil)
	if rep := c.last(procReportDeployAttempt); rep == nil || rep["phase"] != "build" {
		t.Errorf("heartbeat = %v", rep)
	}
}

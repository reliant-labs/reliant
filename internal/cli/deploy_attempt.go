package cli

// DEPLOY ATTEMPTS: every `forge env deploy` that can write (an apply, a
// --plan-only, a rollback) against an env whose ledger is a control plane is
// recorded there as one attempt — who, which env, which release, how far it
// got, how it ended, and its output.
//
// WHY THE CLI RECORDS IT. A deploy starts in three places — a terminal, CI,
// and the Reliant console's Deploy dialog, which runs this same command on
// the daemon — and only this process sees all of it: a build that fails, a
// plan the guard refuses, a promote the server turns down. None of those
// leave a ledger row, and they are the deploys a human comes looking for.
// One writer, here, covers every entry point.
//
// IT NEVER FAILS A DEPLOY. Every call is best-effort with a short timeout:
// a control plane that predates attempts (Unimplemented) is silently
// skipped, any other failure is one line on stderr, and the deploy carries
// on exactly as it would have. The record is a courtesy to whoever looks
// later; a deploy that depended on it would be a worse product.
//
// NOT A CLAIM ABOUT WHAT RUNS. The attempt says what this process was asked
// and how it exited. Whether the release then runs is the promotion's and the
// control plane's own observation; the attempt carries the promotion id so a
// reader can follow it there.
//
// OUTPUT IS CAPTURED ONLY WHEN IT IS NOT A TERMINAL. Teeing a TTY through a
// pipe would change what the human at it sees (children stop drawing
// progress), and that human already has the log on screen. The daemon and CI
// — where nobody is watching — get the full tail recorded.
//
// THE ATTEMPT OPENS LAZILY, when the env's hosted ledger is resolved
// (selectLedgerRendered): that is the first moment forge knows which control
// plane to record into, and it costs no extra render or credential lookup.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	procOpenDeployAttempt   = "controlplane.v1.DeployService/OpenDeployAttempt"
	procReportDeployAttempt = "controlplane.v1.DeployService/ReportDeployAttempt"
	procCloseDeployAttempt  = "controlplane.v1.DeployService/CloseDeployAttempt"

	// The control plane's enum names for CloseDeployAttempt.outcome.
	attemptOutcomeSucceeded = "DEPLOY_ATTEMPT_STATE_SUCCEEDED"
	attemptOutcomeFailed    = "DEPLOY_ATTEMPT_STATE_FAILED"
	attemptOutcomeQueued    = "DEPLOY_ATTEMPT_STATE_QUEUED"
	attemptOutcomeCancelled = "DEPLOY_ATTEMPT_STATE_CANCELLED"

	// deploySourceEnv lets the process that launched forge say where the
	// deploy came from ("console" for the daemon). Display only.
	deploySourceEnv = "FORGE_DEPLOY_SOURCE"
)

// Tunables, vars so tests can shrink them.
var (
	attemptHeartbeat   = 15 * time.Second
	attemptCallTimeout = 10 * time.Second
	// attemptLogLimit is the tail kept in memory between heartbeats; the
	// control plane keeps the same bound, so sending more is pointless.
	attemptLogLimit = 256 * 1024
	// attemptReportLimit bounds the captured --json document.
	attemptReportLimit = 1024 * 1024
)

type deployAttemptCtxKey struct{}

// deployAttempt is one invocation's record, carried in ctx.
type deployAttempt struct {
	env, mode, source, actor string
	runID, runURL            string
	warn                     io.Writer

	mu        sync.Mutex
	client    cloudCaller
	id        string
	opened    bool
	dead      bool // stop trying: the open failed, or the server refused a write
	phase     string
	release   string
	promotion string
	pending   bytes.Buffer // output not yet sent
	report    bytes.Buffer // stdout under --json: the deploy document
	nudge     chan struct{}
	stop      chan struct{}
	beatDone  chan struct{}
	restore   []func()
}

// deployAttemptMode is the attempt mode for a deploy invocation, or "" when
// the invocation writes nothing and records no attempt.
func deployAttemptMode(f deployCmdFlags) string {
	switch {
	case f.explain, f.dryRun, f.promote.plan:
		return ""
	case f.offlineLedger:
		// Its control plane is the thing that is down.
		return ""
	case f.promote.planOnly:
		return "plan"
	default:
		return "apply"
	}
}

// beginDeployAttempt starts the record of one invocation: it captures output
// from now on and waits for the hosted ledger to open the attempt. A nil
// attempt (an invocation that writes nothing) is a valid, inert value.
func beginDeployAttempt(ctx context.Context, env string, f deployCmdFlags) (context.Context, *deployAttempt) {
	mode := deployAttemptMode(f)
	if mode == "" {
		return ctx, nil
	}
	a := &deployAttempt{
		env:     env,
		mode:    mode,
		actor:   f.promote.actor,
		release: f.promote.version,
		warn:    os.Stderr,
		nudge:   make(chan struct{}, 1),
	}
	if run, err := f.promote.run.resolveRun(); err == nil {
		a.runID, a.runURL = run.ID, run.URL
	}
	a.source = deploySource(os.Getenv(deploySourceEnv), a.runID)
	a.capture(f.jsonOut)
	return context.WithValue(ctx, deployAttemptCtxKey{}, a), a
}

// deploySource names where the invocation came from: what the launcher
// said, else CI when a run was detected, else a terminal.
func deploySource(declared, runID string) string {
	switch declared {
	case "terminal", "ci", "console":
		return declared
	}
	if runID != "" {
		return "ci"
	}
	return "terminal"
}

func deployAttemptFrom(ctx context.Context) *deployAttempt {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(deployAttemptCtxKey{}).(*deployAttempt)
	return a
}

// attachDeployAttempt opens the attempt on the env's hosted ledger, the
// first time this invocation's env resolves to one.
func attachDeployAttempt(ctx context.Context, env string, l envLedger) {
	a := deployAttemptFrom(ctx)
	if a == nil || !l.Hosted || env != a.env {
		return
	}
	store, ok := l.Bindings.(*hostedStore)
	if !ok || store.client == nil {
		return
	}
	a.mu.Lock()
	if a.opened || a.dead {
		a.mu.Unlock()
		return
	}
	a.client = store.client
	a.id = newAttemptID()
	req := map[string]any{
		"id":              a.id,
		"project":         store.project,
		"environmentName": a.env,
		"mode":            a.mode,
		"source":          a.source,
		"releaseVersion":  a.release,
		"forgeVersion":    buildinfo.Version(),
		"startedByActor":  a.actor,
	}
	if a.runID != "" {
		req["run"] = map[string]any{"id": a.runID, "url": a.runURL}
	}
	a.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptCallTimeout)
	defer cancel()
	err := a.client.Call(callCtx, procOpenDeployAttempt, req, &struct{}{})

	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.dead = true
		a.warnLocked("not recorded", err)
		return
	}
	a.opened = true
	a.phase = "start"
	a.stop = make(chan struct{})
	a.beatDone = make(chan struct{})
	go a.heartbeat(context.WithoutCancel(ctx))
}

// noteDeployPhase records that the invocation entered a phase.
func noteDeployPhase(ctx context.Context, phase string) {
	deployAttemptFrom(ctx).set(func(a *deployAttempt) { a.phase = phase })
}

// noteDeployRelease records the release the invocation ships.
func noteDeployRelease(ctx context.Context, version string) {
	if version == "" {
		return
	}
	deployAttemptFrom(ctx).set(func(a *deployAttempt) { a.release = version })
}

// noteDeployPromotion records the promotion the invocation wrote.
func noteDeployPromotion(ctx context.Context, id string) {
	if id == "" {
		return
	}
	deployAttemptFrom(ctx).set(func(a *deployAttempt) { a.promotion = id })
}

func (a *deployAttempt) set(change func(*deployAttempt)) {
	if a == nil {
		return
	}
	a.mu.Lock()
	change(a)
	a.mu.Unlock()
	select {
	case a.nudge <- struct{}{}:
	default:
	}
}

func (a *deployAttempt) heartbeat(ctx context.Context) {
	defer close(a.beatDone)
	t := time.NewTicker(attemptHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		case <-a.nudge:
		}
		a.report1(ctx)
	}
}

// report1 sends one heartbeat with whatever changed and the pending output.
func (a *deployAttempt) report1(ctx context.Context) {
	a.mu.Lock()
	if !a.opened || a.dead {
		a.mu.Unlock()
		return
	}
	logChunk := a.pending.String()
	a.pending.Reset()
	req := map[string]any{
		"id":             a.id,
		"phase":          a.phase,
		"releaseVersion": a.release,
		"promotionId":    a.promotion,
		"logAppend":      logChunk,
	}
	client := a.client
	a.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, attemptCallTimeout)
	defer cancel()
	err := client.Call(callCtx, procReportDeployAttempt, req, &struct{}{})
	if err == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if hostedErrorHasCode(err, cloud.CodeFailedPrecondition) || hostedErrorHasCode(err, cloud.CodeNotFound) {
		// The server closed or lost it; nothing more can be written.
		a.dead = true
		return
	}
	// Transient: keep the chunk for the next beat, still bounded.
	rest := a.pending.String()
	a.pending.Reset()
	a.appendLocked(&a.pending, logChunk+rest, attemptLogLimit)
}

// finish closes the attempt with the invocation's outcome. Always safe to
// call, including on a nil attempt or one that never opened.
func (a *deployAttempt) finish(ctx context.Context, runErr error) {
	if a == nil {
		return
	}
	// Output first: restoring the streams drains the pipes, so the final
	// chunk is in pending before the close sends it.
	a.release1()

	a.mu.Lock()
	opened, dead := a.opened, a.dead
	stop := a.stop
	a.mu.Unlock()
	if !opened {
		return
	}
	close(stop)
	<-a.beatDone
	if dead {
		return
	}

	outcome, code := attemptOutcomeOf(ctx, runErr)
	a.mu.Lock()
	req := map[string]any{
		"id":             a.id,
		"outcome":        outcome,
		"exitCode":       code,
		"releaseVersion": a.release,
		"promotionId":    a.promotion,
		"logAppend":      a.pending.String(),
	}
	if outcome == attemptOutcomeFailed {
		req["failedPhase"] = a.phase
	}
	if runErr != nil {
		req["errorMessage"] = truncateRunes(runErr.Error(), 16*1024)
	}
	if doc := bytes.TrimSpace(a.report.Bytes()); len(doc) > 0 && json.Valid(doc) {
		req["reportJson"] = string(doc)
	}
	client := a.client
	a.mu.Unlock()

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptCallTimeout)
	defer cancel()
	if err := client.Call(callCtx, procCloseDeployAttempt, req, &struct{}{}); err != nil {
		a.mu.Lock()
		a.warnLocked("outcome not recorded", err)
		a.mu.Unlock()
	}
}

// attemptOutcomeOf maps how the invocation returned onto an outcome and the
// exit code the process will report.
func attemptOutcomeOf(ctx context.Context, err error) (string, int) {
	if err == nil {
		return attemptOutcomeSucceeded, 0
	}
	code := 1
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		code = coded.ExitCode()
	}
	switch {
	case code == exitQueued:
		return attemptOutcomeQueued, code
	case errors.Is(err, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)):
		return attemptOutcomeCancelled, code
	case code == 0:
		// An error that asked for exit 0 (a declined confirmation) wrote
		// nothing and failed nothing.
		return attemptOutcomeSucceeded, code
	default:
		return attemptOutcomeFailed, code
	}
}

func (a *deployAttempt) warnLocked(what string, err error) {
	if hostedErrorHasCode(err, cloud.CodeUnimplemented) {
		// A control plane that keeps no attempts. Nothing to say.
		return
	}
	if a.warn != nil {
		fmt.Fprintf(a.warn, "forge: deploy attempt %s (%v); the deploy itself is unaffected\n", what, err)
	}
}

// ─── Output capture ─────────────────────────────────────────────────────────

// capture tees os.Stdout and os.Stderr, each only when it is not a terminal.
// Under --json stdout is the deploy document and is kept as the report; the
// log is everything else.
func (a *deployAttempt) capture(jsonOut bool) {
	a.tee(&os.Stderr, func(p []byte) { a.appendOutput(&a.pending, p, attemptLogLimit) })
	if jsonOut {
		a.tee(&os.Stdout, func(p []byte) { a.appendOutput(&a.report, p, attemptReportLimit) })
	} else {
		a.tee(&os.Stdout, func(p []byte) { a.appendOutput(&a.pending, p, attemptLogLimit) })
	}
}

func (a *deployAttempt) tee(stream **os.File, sink func([]byte)) {
	orig := *stream
	if orig == nil || term.IsTerminal(int(orig.Fd())) { //nolint:gosec // a file descriptor fits in int
		return
	}
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	*stream = w
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				_, _ = orig.Write(buf[:n])
				sink(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()
	a.restore = append(a.restore, func() {
		// Put the real stream back FIRST, so nothing written from here on
		// can land in a pipe nobody reads; then close the write end and
		// wait for the copier to drain what is already in it.
		*stream = orig
		_ = w.Close()
		<-done
		_ = r.Close()
	})
}

// release1 restores the captured streams, once.
func (a *deployAttempt) release1() {
	for _, restore := range a.restore {
		restore()
	}
	a.restore = nil
}

func (a *deployAttempt) appendOutput(buf *bytes.Buffer, p []byte, limit int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.appendLocked(buf, string(p), limit)
}

// appendLocked appends s and keeps at most limit bytes of the tail.
func (a *deployAttempt) appendLocked(buf *bytes.Buffer, s string, limit int) {
	buf.WriteString(s)
	if over := buf.Len() - limit; over > 0 {
		tail := append([]byte(nil), buf.Bytes()[over:]...)
		buf.Reset()
		buf.Write(tail)
	}
}

// newAttemptID mints the attempt's id: random, so a retried open of this
// invocation is the same row and no two invocations share one.
func newAttemptID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "att_" + hex.EncodeToString(b[:])
}

// truncateRunes cuts s to at most n bytes on a rune boundary.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

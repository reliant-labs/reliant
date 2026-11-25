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
	"github.com/reliant-labs/forge/pkg/release"
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
	// attemptFlushBackoff is the first wait between the final reports that
	// deliver a code source still owed at finish; it doubles per try.
	attemptFlushBackoff = 250 * time.Millisecond
)

// attemptFlushTries bounds those final reports: each is one call of at most
// attemptCallTimeout, at the end of a deploy whose own work is done.
const attemptFlushTries = 3

type deployAttemptCtxKey struct{}

// deployAttempt is one invocation's record, carried in ctx.
type deployAttempt struct {
	env, mode, source, actor string
	runID, runURL            string
	warn                     io.Writer
	// shipsCheckout: the invocation deploys this checkout (no release
	// named, no --from env), so the checkout's HEAD is the commit it ships
	// (code_source.go, forAttempt).
	shipsCheckout bool

	mu sync.Mutex
	// codeSourceWarning is the two-repositories warning the cut or the open
	// returned, printed once by finish (code_source_warning.go).
	codeSourceWarning string
	client            cloudCaller
	id                string
	opened            bool
	dead              bool // stop trying: the open failed, or the server refused a write
	phase             string
	release           string
	promotion         string
	pending           bytes.Buffer // output not yet sent
	report            bytes.Buffer // stdout under --json: the deploy document
	nudge             chan struct{}
	stop              chan struct{}
	beatDone          chan struct{}
	restore           []func()

	// THE ATTEMPT'S CODE SOURCE (code_source.go, forAttempt) is sent once:
	// with the open when what ships is known then, else with the first
	// report after it is — a --from deploy learns its release only after
	// the attempt opened. codeSourceStore reads that release and the
	// checkout. codeSourceTried is the release last resolved, so a release
	// that cannot be read costs one read, not one per heartbeat.
	// codeSourcePending is a resolved source not yet delivered;
	// codeSourceDone ends it, delivered or known to say nothing.
	codeSourceStore   *hostedStore
	codeSourceTried   string
	codeSourcePending map[string]any
	codeSourceDone    bool

	// reportMu serializes report1, so one report at a time resolves the
	// code source and consumes it: two reports could otherwise both find it
	// unresolved, both read the release and both send it. Taken before mu,
	// never while holding it.
	reportMu sync.Mutex
	// flushBackoff is the first wait between final flush reports
	// (attemptFlushBackoff, which tests shrink); it doubles per try.
	flushBackoff time.Duration
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

		shipsCheckout: deployShipsCheckout(f),
		flushBackoff:  attemptFlushBackoff,
	}
	if run, err := f.promote.run.resolveRun(); err == nil {
		a.runID, a.runURL = run.ID, run.URL
	}
	a.source = deploySource(os.Getenv(deploySourceEnv), a.runID)
	a.capture(f.jsonOut)
	return context.WithValue(ctx, deployAttemptCtxKey{}, a), a
}

// deployShipsCheckout reports whether the invocation deploys this checkout:
// no release named and no --from / --from-promotion, so the release it ships is cut from
// the checkout's HEAD. A named release, or what another env runs, was built
// from whatever commit its own cut recorded.
func deployShipsCheckout(f deployCmdFlags) bool {
	from := f.promote.fromSource()
	return f.promote.version == "" && from.Env == "" && from.PromotionID == ""
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
	noteDeployHosted(ctx, env, l)
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
	shipsCheckout, version := a.shipsCheckout, a.release
	a.codeSourceStore = store
	a.mu.Unlock()
	// What this deploy ships (code_source.go). Read outside the lock: the
	// first read runs git, and a named release is read from the ledger.
	src, sourceKnown := attemptCodeSource(ctx, store, shipsCheckout, version)
	if src != nil {
		req["codeSource"] = src
	}

	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptCallTimeout)
	defer cancel()
	var resp struct {
		CodeSources *wireAppCodeSources `json:"codeSources"`
	}
	err := a.client.Call(callCtx, procOpenDeployAttempt, req, &resp)

	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.dead = true
		a.warnLocked("not recorded", err)
		return
	}
	if msg := resp.CodeSources.warning(); msg != "" {
		a.codeSourceWarning = msg
	}
	a.codeSourceTried, a.codeSourceDone = version, sourceKnown
	a.opened = true
	a.phase = "start"
	a.stop = make(chan struct{})
	a.beatDone = make(chan struct{})
	go a.heartbeat(context.WithoutCancel(ctx))
}

// attemptCodeSource is the wire source of what a deploy ships, and whether
// that is known yet (codeSource.forAttempt). nil and known: the source says
// nothing. nil and not known: the release is not resolved, or could not be
// read, so there is nothing coherent to send yet.
func attemptCodeSource(ctx context.Context, store *hostedStore, shipsCheckout bool, version string) (map[string]any, bool) {
	shipped := attemptRelease(ctx, store, shipsCheckout, version)
	if !shipsCheckout && shipped == nil {
		return nil, false
	}
	src, known := store.source.get(ctx).forAttempt(shipsCheckout, shipped)
	return src.wire(), known
}

// codeSourceOwedLocked reports whether the attempt still owes its code
// source: one resolved and undelivered, or a release noted since the last
// resolution.
func (a *deployAttempt) codeSourceOwedLocked() bool {
	if a.codeSourceDone {
		return false
	}
	return a.codeSourcePending != nil || a.codeSourceUnresolvedLocked()
}

// codeSourceUnresolvedLocked: a deploy of an already-cut release whose
// release is now known and has not been resolved yet.
func (a *deployAttempt) codeSourceUnresolvedLocked() bool {
	return !a.codeSourceDone && a.codeSourcePending == nil && !a.shipsCheckout &&
		a.codeSourceStore != nil && a.release != "" && a.release != a.codeSourceTried
}

// attemptRelease is the named release a deploy ships, as its ledger recorded
// it, so the attempt can send the source that release was cut from. nil when
// the deploy ships the checkout, names no release yet (--from resolves it
// after the attempt opens), or the read fails or finds none: the attempt is
// best-effort and a failed read only leaves its source unsent.
func attemptRelease(ctx context.Context, store *hostedStore, shipsCheckout bool, version string) *release.Release {
	if shipsCheckout || version == "" {
		return nil
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), attemptCallTimeout)
	defer cancel()
	r, err := store.Get(callCtx, version)
	if err != nil {
		return nil
	}
	return r
}

// progress reports whether the attempt opened on the control plane, and the
// last phase the invocation entered. A nil attempt never opens.
func (a *deployAttempt) progress() (opened bool, phase string) {
	if a == nil {
		return false, ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.opened, a.phase
}

// noteDeployPhase records that the invocation entered a phase.
func noteDeployPhase(ctx context.Context, phase string) {
	deployAttemptFrom(ctx).set(func(a *deployAttempt) { a.phase = phase })
	deployOutcomeFrom(ctx).set(func(o *deployOutcome) { o.phase = phase })
}

// noteDeployRelease records the release the invocation ships.
func noteDeployRelease(ctx context.Context, version string) {
	if version == "" {
		return
	}
	deployAttemptFrom(ctx).set(func(a *deployAttempt) { a.release = version })
	deployOutcomeFrom(ctx).set(func(o *deployOutcome) { o.release = version })
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
		// A heartbeat is best effort: a failed report keeps its output and
		// any pending code source for the next tick, and finish retries the
		// last one, so the error has nothing more to do here.
		_ = a.report1(ctx)
	}
}

// report1 sends one heartbeat with whatever changed and the pending output,
// and returns the call's error: nil when nothing was sent or the server took
// it. A pending code source is cleared only when the server took it.
func (a *deployAttempt) report1(ctx context.Context) error {
	a.reportMu.Lock()
	defer a.reportMu.Unlock()
	a.mu.Lock()
	if !a.opened || a.dead {
		a.mu.Unlock()
		return nil
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
	resolve, store, version := a.codeSourceUnresolvedLocked(), a.codeSourceStore, a.release
	a.mu.Unlock()

	// The source of what ships, once it is known (see codeSourceStore):
	// resolved outside the lock, since it reads the ledger and runs git.
	if resolve {
		src, known := attemptCodeSource(ctx, store, false, version)
		a.mu.Lock()
		a.codeSourceTried = version
		switch {
		case known && src == nil:
			a.codeSourceDone = true
		case known:
			a.codeSourcePending = src
		}
		a.mu.Unlock()
	}
	a.mu.Lock()
	sentSource := a.codeSourcePending
	a.mu.Unlock()
	if sentSource != nil {
		req["codeSource"] = sentSource
	}

	callCtx, cancel := context.WithTimeout(ctx, attemptCallTimeout)
	defer cancel()
	var resp struct {
		CodeSources *wireAppCodeSources `json:"codeSources"`
	}
	err := client.Call(callCtx, procReportDeployAttempt, req, &resp)
	if err == nil {
		a.mu.Lock()
		if sentSource != nil {
			a.codeSourceDone, a.codeSourcePending = true, nil
		}
		if msg := resp.CodeSources.warning(); msg != "" {
			a.codeSourceWarning = msg
		}
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if hostedErrorHasCode(err, cloud.CodeFailedPrecondition) || hostedErrorHasCode(err, cloud.CodeNotFound) {
		// The server closed or lost it; nothing more can be written.
		a.dead = true
		return err
	}
	// Transient: keep the chunk for the next beat, still bounded. A pending
	// code source stays pending for the same reason.
	rest := a.pending.String()
	a.pending.Reset()
	a.appendLocked(&a.pending, logChunk+rest, attemptLogLimit)
	return err
}

// flushCodeSource delivers a code source still owed when the deploy ends —
// its release became known too late for a heartbeat to carry it, or the
// heartbeat that carried it failed — on final reports: the close carries no
// source. A transient failure is retried, a bounded number of times with a
// doubling backoff; an answer that will not change (the attempt is gone, the
// server keeps no reports, the request is refused) ends it.
func (a *deployAttempt) flushCodeSource(ctx context.Context) {
	backoff := a.flushBackoff
	if backoff <= 0 {
		backoff = attemptFlushBackoff
	}
	for try := 1; ; try++ {
		err := a.report1(ctx)
		a.mu.Lock()
		owed := !a.dead && a.codeSourceOwedLocked()
		a.mu.Unlock()
		if !owed || err == nil || try >= attemptFlushTries || attemptErrorIsFinal(err) {
			return
		}
		t := time.NewTimer(backoff)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
		backoff *= 2
	}
}

// attemptErrorIsFinal: a report error that a retry would only repeat.
func attemptErrorIsFinal(err error) bool {
	for _, code := range []string{cloud.CodeUnimplemented, cloud.CodeInvalidArgument, cloud.CodePermissionDenied, cloud.CodeUnauthenticated} {
		if hostedErrorHasCode(err, code) {
			return true
		}
	}
	return false
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
	opened := a.opened
	stop := a.stop
	a.mu.Unlock()
	if opened {
		close(stop)
		<-a.beatDone
		a.mu.Lock()
		owed := !a.dead && a.codeSourceOwedLocked()
		a.mu.Unlock()
		if owed {
			a.flushCodeSource(context.WithoutCancel(ctx))
		}
	}
	// After the deploy's own output, and whatever its outcome: it is about
	// the app, not this deploy. Printed even if the attempt never opened, as
	// a cut's warning may be held here.
	a.printCodeSourceWarning()
	if !opened {
		return
	}
	a.mu.Lock()
	dead := a.dead
	a.mu.Unlock()
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

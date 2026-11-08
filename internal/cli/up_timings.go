package cli

// Per-phase timings of one `forge env up` run.
//
// runUp is a long sequential function with one overlapping lane (the infra
// pre-warm runs beside the build) and fan-out inside the build. Without a
// record of where its wall clock went, no claim that a change made the dev
// loop faster can be checked. So every run records its phases as SPANS — a
// start offset from the run's start plus a duration — rather than a list of
// durations: overlapping phases summed would overstate the run, and the
// overlap is exactly what a speedup changes.
//
// Each run ends (green or red) with:
//
//	stderr                                 a compact table, in start order
//	.forge/logs/<env>/up-timings.jsonl     one JSON line per run, appended
//
// The JSON line is a public surface (forge.env.up.timings/v1): fields are
// only ever added. FORGE_UP_TIMINGS=0 suppresses the table; the line is
// always written.
//
// Spans travel in the context, the way a tracer's do, so a phase deep in the
// build can open a child without a parameter threaded through every caller.
// A context with no recorder — `forge env build`, every test that does not
// opt in — yields nil spans whose methods do nothing.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// upTimingsSchema names the shape of one up-timings.jsonl line.
const upTimingsSchema = "forge.env.up.timings/v1"

// upTimingsFile is the record's file name inside .forge/logs/<env>/.
const upTimingsFile = "up-timings.jsonl"

// Span statuses. incomplete is a span the run never closed: it was still
// running when the run ended, which on a failed run is usually the phase
// that failed out from under it.
const (
	upSpanOK         = "ok"
	upSpanFailed     = "failed"
	upSpanSkipped    = "skipped"
	upSpanIncomplete = "incomplete"
)

// upTimingsRecord is one line of .forge/logs/<env>/up-timings.jsonl.
type upTimingsRecord struct {
	Schema       string        `json:"schema"`
	RunID        string        `json:"run_id"`
	StartedAt    time.Time     `json:"started_at"`
	Env          string        `json:"env"`
	ForgeVersion string        `json:"forge_version"`
	Targets      []string      `json:"targets"`
	Flags        upTimingFlags `json:"flags"`
	// Status is "ok" or "failed"; Error is the run's error, first line.
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	TotalMS int64  `json:"total_ms"`
	// Phases are in start order. Name is the full path ("build/go/api");
	// Parent is the path of the enclosing span, empty at the top level.
	Phases []upPhaseRecord `json:"phases"`
}

// upTimingFlags is the run's shape-changing flags. Options carries the -D
// option NAMES only: a value can be anything, a credential included.
type upTimingFlags struct {
	NoBuild    bool     `json:"no_build"`
	NoDeploy   bool     `json:"no_deploy"`
	NoGenerate bool     `json:"no_generate"`
	NoInstall  bool     `json:"no_install"`
	NoSeed     bool     `json:"no_seed"`
	Background bool     `json:"background"`
	Watch      bool     `json:"watch"`
	Options    []string `json:"options"`
}

// upPhaseRecord is one span of the run.
type upPhaseRecord struct {
	Name       string `json:"name"`
	Parent     string `json:"parent,omitempty"`
	StartMS    int64  `json:"start_ms"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// upTimings records the spans of one run. Safe for concurrent use: the infra
// pre-warm and the build's fan-out record from their own goroutines.
type upTimings struct {
	mu       sync.Mutex
	now      func() time.Time
	origin   time.Time
	spans    []*upSpan
	launched map[string]time.Time
	finished bool
}

// upSpan is one timed phase. A nil *upSpan is the no-recorder case and every
// method on it is a no-op.
type upSpan struct {
	t           *upTimings
	name        string
	parent      string
	start, stop time.Time
	status      string
	errMsg      string
	ended       bool
	discarded   bool
}

func newUpTimings(now func() time.Time) *upTimings {
	if now == nil {
		now = time.Now
	}
	return &upTimings{now: now, origin: now(), launched: map[string]time.Time{}}
}

type upSpanCtxKey struct{}

// upSpanCtx is what the context carries: the recorder, and the span new
// spans nest under (nil at the top level).
type upSpanCtx struct {
	t      *upTimings
	parent *upSpan
}

// withUpTimings arms ctx with a recorder; spans opened from it are top level.
func withUpTimings(ctx context.Context, t *upTimings) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, upSpanCtxKey{}, upSpanCtx{t: t})
}

func upSpanFrom(ctx context.Context) (upSpanCtx, bool) {
	if ctx == nil {
		return upSpanCtx{}, false
	}
	c, ok := ctx.Value(upSpanCtxKey{}).(upSpanCtx)
	return c, ok && c.t != nil
}

// startUpSpan opens a span named name under ctx's current span and returns a
// context whose spans nest under it. With no recorder in ctx it returns ctx
// unchanged and a nil span.
func startUpSpan(ctx context.Context, name string) (context.Context, *upSpan) {
	c, ok := upSpanFrom(ctx)
	if !ok {
		return ctx, nil
	}
	s := c.t.begin(c.parent, name)
	if s == nil {
		return ctx, nil
	}
	return context.WithValue(ctx, upSpanCtxKey{}, upSpanCtx{t: c.t, parent: s}), s
}

// recordUpSpan records a span that already happened — a build whose start
// was taken before it was dispatched — under ctx's current span.
func recordUpSpan(ctx context.Context, name string, start time.Time, status string, err error) {
	c, ok := upSpanFrom(ctx)
	if !ok {
		return
	}
	c.t.record(c.parent, name, start, c.t.now(), status, err)
}

// upNow is the recorder's clock, or the wall clock with no recorder. A span
// recorded after the fact must take its start from the same clock its end
// is read from.
func upNow(ctx context.Context) time.Time {
	if c, ok := upSpanFrom(ctx); ok {
		return c.t.now()
	}
	return time.Now()
}

// markUpLaunched notes when a workload's process was started, the origin of
// its time-to-ready span (observeUpReady).
func markUpLaunched(ctx context.Context, name string) {
	c, ok := upSpanFrom(ctx)
	if !ok {
		return
	}
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	if _, seen := c.t.launched[name]; !seen {
		c.t.launched[name] = c.t.now()
	}
}

// upLaunchedAt is when markUpLaunched saw name start.
func upLaunchedAt(ctx context.Context, name string) (time.Time, bool) {
	c, ok := upSpanFrom(ctx)
	if !ok {
		return time.Time{}, false
	}
	c.t.mu.Lock()
	defer c.t.mu.Unlock()
	at, seen := c.t.launched[name]
	return at, seen
}

func spanPath(parent *upSpan, name string) (path, parentPath string) {
	if parent == nil {
		return name, ""
	}
	return parent.name + "/" + name, parent.name
}

func (t *upTimings) begin(parent *upSpan, name string) *upSpan {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil
	}
	path, parentPath := spanPath(parent, name)
	s := &upSpan{t: t, name: path, parent: parentPath, start: t.now()}
	t.spans = append(t.spans, s)
	return s
}

func (t *upTimings) record(parent *upSpan, name string, start, end time.Time, status string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return
	}
	path, parentPath := spanPath(parent, name)
	s := &upSpan{t: t, name: path, parent: parentPath, start: start, stop: end, ended: true}
	s.status, s.errMsg = spanOutcome(status, err)
	t.spans = append(t.spans, s)
}

// spanOutcome resolves a span's status: an error always means failed, and an
// empty status otherwise means ok.
func spanOutcome(status string, err error) (string, string) {
	if err != nil {
		return upSpanFailed, upTimingsErrLine(err.Error())
	}
	if status == "" {
		status = upSpanOK
	}
	return status, ""
}

// end closes the span: failed when err is non-nil, ok otherwise. A second
// end is ignored, so `defer s.end(err)` after an explicit end is harmless.
func (s *upSpan) end(err error) { s.endWith("", err) }

// endWith closes the span with an explicit status (upSpanSkipped).
func (s *upSpan) endWith(status string, err error) {
	if s == nil {
		return
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	if s.ended || s.t.finished {
		return
	}
	s.ended, s.stop = true, s.t.now()
	s.status, s.errMsg = spanOutcome(status, err)
}

// discard drops the span from the record: a stage that turned out to have
// nothing in it is noise in the table, not a phase.
func (s *upSpan) discard() {
	if s == nil {
		return
	}
	s.t.mu.Lock()
	defer s.t.mu.Unlock()
	s.discarded = true
}

// finish stops recording and returns the spans as records, in start order
// (ties in the order they were opened). A span still open is closed here as
// incomplete. Only the first call returns records; the rest return nil.
func (t *upTimings) finish() (total time.Duration, phases []upPhaseRecord) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return 0, nil
	}
	t.finished = true
	end := t.now()
	kept := make([]*upSpan, 0, len(t.spans))
	for _, s := range t.spans {
		if s.discarded {
			continue
		}
		if !s.ended {
			s.ended, s.stop, s.status = true, end, upSpanIncomplete
		}
		kept = append(kept, s)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].start.Before(kept[j].start) })
	phases = make([]upPhaseRecord, 0, len(kept))
	for _, s := range kept {
		phases = append(phases, upPhaseRecord{
			Name: s.name, Parent: s.parent,
			StartMS:    s.start.Sub(t.origin).Milliseconds(),
			DurationMS: s.stop.Sub(s.start).Milliseconds(),
			Status:     s.status, Error: s.errMsg,
		})
	}
	return end.Sub(t.origin), phases
}

// upTimingsRun is everything about the run the record carries besides its
// spans, filled in by runUp as the facts become known.
type upTimingsRun struct {
	projectDir string
	opts       upOptions
	version    string
	runID      string
}

// finishUpTimings closes the run's record, appends it to the env's
// up-timings.jsonl and prints the table to errOut. Never fatal: a record
// that cannot be written is a warning, not a failed up. A run that never
// learned its project (it failed before loading one) has nowhere to write
// and prints nothing.
func finishUpTimings(t *upTimings, run upTimingsRun, runErr error, errOut io.Writer) {
	if t == nil {
		return
	}
	total, phases := t.finish()
	if phases == nil || run.projectDir == "" || run.opts.env == "" {
		return
	}
	rec := newUpTimingsRecord(t.origin, run, runErr, total, phases)
	path, perr := upTimingsPath(run.projectDir, run.opts.env)
	if perr == nil {
		perr = appendUpTimings(path, rec)
	}
	if os.Getenv("FORGE_UP_TIMINGS") == "0" {
		if perr != nil {
			_, _ = fmt.Fprintf(errOut, "[up] timings: %v\n", perr)
		}
		return
	}
	printUpTimings(errOut, rec, filepath.Join(upLogDir(run.opts.env), upTimingsFile))
	if perr != nil {
		_, _ = fmt.Fprintf(errOut, "[up] timings: %v\n", perr)
	}
}

func newUpTimingsRecord(origin time.Time, run upTimingsRun, runErr error, total time.Duration, phases []upPhaseRecord) upTimingsRecord {
	o := run.opts
	options := make([]string, 0, len(o.renderOptions))
	for _, kv := range o.renderOptions {
		name, _, _ := strings.Cut(kv, "=")
		options = append(options, name)
	}
	targets := append([]string{}, o.targets...)
	rec := upTimingsRecord{
		Schema: upTimingsSchema, RunID: run.runID, StartedAt: origin.UTC(),
		Env: o.env, ForgeVersion: run.version, Targets: targets,
		Flags: upTimingFlags{
			NoBuild: o.noBuild, NoDeploy: o.noDeploy, NoGenerate: o.noGenerate,
			NoInstall: o.noInstall, NoSeed: o.noSeed, Background: o.background,
			Watch: o.watch, Options: options,
		},
		Status: upSpanOK, TotalMS: total.Milliseconds(), Phases: phases,
	}
	if runErr != nil {
		rec.Status, rec.Error = upSpanFailed, upTimingsErrLine(runErr.Error())
	}
	return rec
}

// newUpRunID is a run id: random, so two worktrees' runs never collide.
func newUpRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "up_" + hex.EncodeToString(b[:])
}

// upTimingsPath is the env's timings record in the project.
func upTimingsPath(projectDir, env string) (string, error) {
	if env == "" || filepath.Base(env) != env || env == "." || env == ".." {
		return "", fmt.Errorf("env %q is not a file name", env)
	}
	return filepath.Join(projectDir, upLogDir(env), upTimingsFile), nil
}

// appendUpTimings appends rec as one line. One write of the whole line under
// O_APPEND, so concurrent runs of two envs (or a run racing a reader) never
// interleave partial lines.
func appendUpTimings(path string, rec upTimingsRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("record %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("record %s: %w", path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("record %s: %w", path, err)
	}
	return f.Close()
}

// printUpTimings renders the table: one row per span in start order, nested
// spans indented under their parent, each with its start offset and its
// duration. Offsets are what make overlap visible — the infra row starting
// before the build row ends is the pre-warm doing its job.
func printUpTimings(w io.Writer, rec upTimingsRecord, recordPath string) {
	_, _ = fmt.Fprintf(w, "\n[up] timings — %s in %s (record: %s)\n", rec.Status, fmtUpDuration(rec.TotalMS), recordPath)
	phases := upPhasesTreeOrder(rec.Phases)
	depth := map[string]int{}
	width := 0
	labels := make([]string, len(phases))
	for i, p := range phases {
		d, short := 0, p.Name
		if pd, ok := depth[p.Parent]; ok {
			d, short = pd+1, strings.TrimPrefix(p.Name, p.Parent+"/")
		}
		depth[p.Name] = d
		labels[i] = strings.Repeat("  ", d) + short
		if n := len([]rune(labels[i])); n > width {
			width = n
		}
	}
	if width > 48 {
		width = 48
	}
	for i, p := range phases {
		label := labels[i]
		if r := []rune(label); len(r) > width {
			label = string(r[:width-1]) + "…"
		}
		status := ""
		if p.Status != upSpanOK {
			status = "  " + p.Status
		}
		_, _ = fmt.Fprintf(w, "  %-*s  @%7s  %8s%s\n", width, label, fmtUpDuration(p.StartMS), fmtUpDuration(p.DurationMS), status)
	}
}

// upPhasesTreeOrder reorders start-ordered phases so each is followed by its
// children (recursively), siblings keeping their start order. Start order
// alone interleaves a concurrent top-level phase (infra) into the middle of
// another's children (build's).
func upPhasesTreeOrder(phases []upPhaseRecord) []upPhaseRecord {
	known := make(map[string]bool, len(phases))
	for _, p := range phases {
		known[p.Name] = true
	}
	children := map[string][]upPhaseRecord{}
	for _, p := range phases {
		parent := p.Parent
		if !known[parent] {
			parent = "" // an orphan (its parent was discarded) prints at the top
		}
		children[parent] = append(children[parent], p)
	}
	out := make([]upPhaseRecord, 0, len(phases))
	var walk func(parent string)
	walk = func(parent string) {
		for _, p := range children[parent] {
			out = append(out, p)
			walk(p.Name)
		}
	}
	walk("")
	return out
}

// fmtUpDuration renders milliseconds the way the table reads them: tenths
// of a second below a minute, minutes and seconds above.
func fmtUpDuration(ms int64) string {
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%dm%02ds", ms/60_000, (ms%60_000)/1000)
}

// firstLine is s up to its first newline, capped so one long error cannot
// swamp a record.
func upTimingsErrLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	const max = 240
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// timedBuild runs one build under ctx's current span and records it as a
// child span named after the result. The start is taken before the call, so
// a build's span covers exactly the builder's own work.
func timedBuild(ctx context.Context, build func() buildResult) buildResult {
	started := upNow(ctx)
	r := build()
	recordBuildResult(ctx, started, r)
	return r
}

// recordBuildResult records r as a span that began at start. A skipped
// external build is skipped, not ok: the table should not credit it with
// having built anything.
func recordBuildResult(ctx context.Context, start time.Time, r buildResult) {
	status := ""
	if r.kind == "external-skip" {
		status = upSpanSkipped
	}
	name := r.name
	if name == "" {
		name = r.kind
	}
	recordUpSpan(ctx, name, start, status, r.err)
}

// timedBuildStage runs one stage of runBuild as a span whose children are
// that stage's builds.
func timedBuildStage(ctx context.Context, name string, stage func(context.Context) []buildResult) []buildResult {
	rs, _ := timedBuildStageErr(ctx, name, func(ctx context.Context) ([]buildResult, error) { return stage(ctx), nil })
	return rs
}

// timedBuildStageErr is timedBuildStage for a stage that can also fail as a
// whole, before any build in it ran.
func timedBuildStageErr(ctx context.Context, name string, stage func(context.Context) ([]buildResult, error)) ([]buildResult, error) {
	stageCtx, s := startUpSpan(ctx, name)
	rs, err := stage(stageCtx)
	endBuildStage(s, rs, err)
	return rs, err
}

// endBuildStage closes a stage span: dropped when the stage built nothing,
// failed when any of its builds did.
func endBuildStage(s *upSpan, rs []buildResult, err error) {
	if err == nil && len(rs) == 0 {
		s.discard()
		return
	}
	if err == nil {
		failed := 0
		for _, r := range rs {
			if r.err != nil {
				failed++
			}
		}
		if failed > 0 {
			err = fmt.Errorf("%d of %d builds failed", failed, len(rs))
		}
	}
	s.end(err)
}

// upReadyTimer turns the readiness gate's polls into one span per workload:
// from the moment its process was launched (markUpLaunched) to the first
// poll that found every one of its ports bound by this run's child. That is
// time-to-ready — compile plus startup — which is the number a dev-loop
// change moves. A workload with no recorded launch is timed from the gate's
// start and is never ok: the gate passing it is recorded as skipped (this run
// did not launch it), giving up on it as failed. Its span is a child of the readiness span, so its start can lie
// BEFORE its parent's: it began when the process did, not when the gate did.
type upReadyTimer struct {
	ctx     context.Context
	opened  time.Time
	seen    map[string]bool
	order   []string
	settled map[string]bool
}

func newUpReadyTimer(ctx context.Context) *upReadyTimer {
	if _, ok := upSpanFrom(ctx); !ok {
		return nil
	}
	return &upReadyTimer{ctx: ctx, opened: upNow(ctx), seen: map[string]bool{}, settled: map[string]bool{}}
}

func (r *upReadyTimer) since(name string) time.Time {
	if at, ok := upLaunchedAt(r.ctx, name); ok {
		return at
	}
	return r.opened
}

// observe records each workload whose every result in rs is ready, once.
func (r *upReadyTimer) observe(rs []hostReadyResult) {
	if r == nil {
		return
	}
	unready := map[string]bool{}
	for _, u := range hostReadyUnready(rs) {
		unready[u.name] = true
	}
	for _, res := range rs {
		if !r.seen[res.name] {
			r.seen[res.name] = true
			r.order = append(r.order, res.name)
		}
		if r.settled[res.name] || unready[res.name] {
			continue
		}
		r.settled[res.name] = true
		// A workload this run never launched (its build or launch failed
		// before procs.start) has no time-to-ready to report: it is
		// skipped, never credited as ready from the gate's start.
		status := upSpanSkipped
		if _, launched := upLaunchedAt(r.ctx, res.name); launched {
			status = ""
		}
		recordUpSpan(r.ctx, res.name, r.since(res.name), status, nil)
	}
}

// close records every workload the gate gave up on as failed, carrying the
// gate's error.
func (r *upReadyTimer) close(err error) {
	if r == nil || err == nil {
		return
	}
	for _, name := range r.order {
		if !r.settled[name] {
			r.settled[name] = true
			recordUpSpan(r.ctx, name, r.since(name), "", err)
		}
	}
}

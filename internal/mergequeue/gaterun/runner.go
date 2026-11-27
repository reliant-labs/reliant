// Copyright (c) 2025 Reliant Labs
package gaterun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Fetcher fetches a candidate into a fresh directory, outside the boundary
// and with the machine's credentials (GitFetcher). It must write no
// credential into dir.
type Fetcher interface {
	Fetch(ctx context.Context, spec FetchSpec) error
}

// FetchSpec is one fetch.
type FetchSpec struct {
	Repo, Ref, SHA string
	// BaseSHA, when set, is fetched too (StartRequest.BaseSHA): the
	// candidate's history down to it is present, so the gate can diff
	// against it.
	BaseSHA string
	// Dir is an empty directory the tree is checked out into.
	Dir string
}

// ErrCandidateMoved: the candidate ref no longer points at the sha the queue
// built. Unproven, never red: a newer candidate exists.
var ErrCandidateMoved = errors.New("the candidate ref does not point at the candidate sha")

// Runner runs gates on this machine: fetch, gate, report, teardown.
type Runner struct {
	// Isolated are the isolated backends in order of preference (sysbox,
	// later vm). The first that passes Detect and Preflight runs the gate.
	Isolated []Backend
	// NotIsolated is the runc backend, used only when a gate allows it.
	NotIsolated Backend
	Fetcher     Fetcher
	// WorkRoot holds one directory per gate while it is fetched; each is
	// removed when its gate ends.
	WorkRoot string
	// Image is the gate image.
	Image string
	// VulnDBDir is the machine's grype database, mounted read-only.
	VulnDBDir string
	// Limits are the per-gate limits; the request's timeout sets Wall.
	Limits Limits
	// Log receives the gates' own output; nil discards it.
	Log io.Writer
}

// Select is the backend a gate runs in on this machine: the first isolated
// backend whose Detect and Preflight pass, else (only when allowNotIsolated)
// the runc backend. ErrNoIsolatedGateMachine otherwise: no pull-request code
// runs outside a boundary it did not ask for.
func (r *Runner) Select(ctx context.Context, allowNotIsolated bool) (Backend, error) {
	if r.Image == "" {
		return nil, fmt.Errorf("%w: this machine has no gate image (DaemonConfig gate_image, RELIANT_DAEMON_GATE_IMAGE)", ErrNoIsolatedGateMachine)
	}
	var why []string
	for _, b := range r.Isolated {
		if b == nil {
			continue
		}
		if err := ready(ctx, b); err != nil {
			why = append(why, err.Error())
			continue
		}
		return b, nil
	}
	if allowNotIsolated && r.NotIsolated != nil {
		if err := ready(ctx, r.NotIsolated); err != nil {
			why = append(why, err.Error())
		} else {
			return r.NotIsolated, nil
		}
	}
	if len(why) == 0 {
		why = append(why, "this machine has no isolated gate backend")
	}
	return nil, fmt.Errorf("%w: %v", ErrNoIsolatedGateMachine, why)
}

func ready(ctx context.Context, b Backend) error {
	if a := b.Detect(ctx); !a.Available {
		return fmt.Errorf("%s: %s", b.Isolation(), a.Reason)
	}
	if err := b.Preflight(ctx); err != nil {
		return fmt.Errorf("%s: %w", b.Isolation(), err)
	}
	return nil
}

// Run runs one gate in b to a GateResult. It never returns without tearing
// the gate down.
func (r *Runner) Run(ctx context.Context, b Backend, req StartRequest) GateResult {
	start := time.Now()
	out := func(g GateResult) GateResult {
		g.GateID, g.Isolation = req.GateID, b.Isolation()
		if g.Duration == 0 {
			g.Duration = time.Since(start)
		}
		return g
	}
	unproven := func(code string, err error) GateResult {
		if ctx.Err() != nil {
			code = ReasonCanceled
		}
		return out(GateResult{Class: ClassUnproven, ReasonCode: code, Reason: err.Error()})
	}
	plan := req.Stages
	if err := req.Validate(); err != nil {
		return unproven(ReasonBackendFailed, err)
	}
	env, err := GateEnv(req.GateID, req.gateEnv())
	if err != nil {
		return unproven(ReasonBackendFailed, err)
	}

	dir := filepath.Join(r.WorkRoot, req.GateID)
	if err := os.RemoveAll(dir); err != nil {
		return unproven(ReasonFetchFailed, fmt.Errorf("clear the gate directory: %w", err))
	}
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		return unproven(ReasonFetchFailed, fmt.Errorf("make the gate directory: %w", err))
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := r.Fetcher.Fetch(ctx, FetchSpec{Repo: req.Repo, Ref: req.Ref, SHA: req.SHA, BaseSHA: req.BaseSHA, Dir: src}); err != nil {
		code := ReasonFetchFailed
		if errors.Is(err, ErrCandidateMoved) {
			code = ReasonCandidateMoved
		}
		return unproven(code, err)
	}

	defer func() {
		tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		_ = b.Teardown(tctx, req.GateID)
	}()
	limits := r.Limits
	limits.Wall = req.Timeout()
	res, err := b.Run(ctx, Spec{
		RunID: req.GateID, Image: r.Image, Command: []string{"/bin/sh", "-c", Script(plan)},
		SourceDir: src, Env: env, VulnDBDir: r.VulnDBDir, CacheVolume: cacheVolume(b, req), Limits: limits,
		Stdout: r.Log, Stderr: r.Log,
	})
	if err != nil {
		return unproven(ReasonBackendFailed, err)
	}
	var buf bytes.Buffer
	if err := b.CopyOut(ctx, req.GateID, OutDir, &buf, reportMaxBytes); err != nil {
		if res.Class == ClassUnproven {
			return out(assemble(req.GateID, b.Isolation(), plan, res, output{}))
		}
		return unproven(ReasonResultInvalid, fmt.Errorf("copy the gate's output: %w", err))
	}
	o, err := readOutput(&buf)
	if err != nil {
		return unproven(ReasonResultInvalid, err)
	}
	return out(assemble(req.GateID, b.Isolation(), plan, res, o))
}

// cacheVolume is the warm cache volume req's gate gets in b: the
// repository's, only when the request asks for it and allows a not-isolated
// gate, and only when b is not isolated. Otherwise it is "" (see
// StartRequest.WarmCache).
func cacheVolume(b Backend, req StartRequest) string {
	if !req.WarmCache || !req.AllowNotIsolated || b.Isolation().Isolated() {
		return ""
	}
	return CacheVolumeName(req.Repo)
}

// Jobs is a daemon's gates by id: CommandStart starts one in the background
// (or answers for the one already running under that id), CommandStatus
// reads it, CommandCancel stops it. A finished gate's result is kept for
// Retain so a Dispatcher that lost its connection can still read it.
type Jobs struct {
	Runner *Runner
	// Retain is how long a finished result is kept; 0 is 2h.
	Retain time.Duration

	mu   sync.Mutex
	jobs map[string]*job
	now  func() time.Time
}

type job struct {
	cancel   context.CancelFunc // nil while its backend is being selected
	canceled bool
	done     bool
	result   GateResult
	finished time.Time
}

func (jb *job) state() JobState {
	if jb.done {
		return JobDone
	}
	return JobRunning
}

func (j *Jobs) clock() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
}

// Start starts req's gate unless one with its id exists, whose state it
// answers instead. A slot is held from the count to the start, so concurrent
// starts never run more than MaxConcurrent gates.
func (j *Jobs) Start(ctx context.Context, req StartRequest) StartReply {
	if err := req.Validate(); err != nil {
		return StartReply{State: JobRefused, Reason: err.Error()}
	}
	j.mu.Lock()
	j.pruneLocked()
	if jb, ok := j.jobs[req.GateID]; ok {
		j.mu.Unlock()
		return StartReply{State: jb.state()}
	}
	running := 0
	for _, jb := range j.jobs {
		if !jb.done {
			running++
		}
	}
	if running >= max(req.MaxConcurrent, 1) {
		j.mu.Unlock()
		return StartReply{State: JobBusy, Reason: fmt.Sprintf("%d gates running", running)}
	}
	if j.jobs == nil {
		j.jobs = map[string]*job{}
	}
	jb := &job{}
	j.jobs[req.GateID] = jb
	j.mu.Unlock()

	b, err := j.Runner.Select(ctx, req.AllowNotIsolated)
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil {
		delete(j.jobs, req.GateID)
		return StartReply{State: JobRefused, Reason: err.Error()}
	}
	if jb.canceled {
		jb.done, jb.finished = true, j.clock()
		jb.result = GateResult{GateID: req.GateID, Isolation: b.Isolation(), Class: ClassUnproven, ReasonCode: ReasonCanceled, Reason: "canceled before it started"}
		return StartReply{State: JobDone, Isolation: b.Isolation()}
	}
	runCtx, cancel := context.WithCancel(context.Background())
	jb.cancel = cancel
	go func() {
		defer cancel()
		res := j.Runner.Run(runCtx, b, req)
		j.mu.Lock()
		jb.done, jb.result, jb.finished = true, res, j.clock()
		j.mu.Unlock()
	}()
	return StartReply{State: JobRunning, Isolation: b.Isolation()}
}

// Status answers for gateID.
func (j *Jobs) Status(gateID string) StatusReply {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pruneLocked()
	jb, ok := j.jobs[gateID]
	switch {
	case !ok:
		return StatusReply{State: JobUnknown}
	case !jb.done:
		return StatusReply{State: JobRunning}
	}
	res := jb.result
	return StatusReply{State: JobDone, Result: &res}
}

// Cancel stops gateID's gate; its result (canceled, unproven) stays readable.
func (j *Jobs) Cancel(gateID string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	jb, ok := j.jobs[gateID]
	if !ok || jb.done {
		return
	}
	jb.canceled = true
	if jb.cancel != nil {
		jb.cancel()
	}
}

func (j *Jobs) pruneLocked() {
	retain := j.Retain
	if retain <= 0 {
		retain = 2 * time.Hour
	}
	now := j.clock()
	for id, jb := range j.jobs {
		if jb.done && now.Sub(jb.finished) > retain {
			delete(j.jobs, id)
		}
	}
}

// DefaultLimits are a gate's limits on a machine that takes two at once:
// half its CPUs, 16 GiB of memory, 16384 pids, 100 GiB of disk.
func DefaultLimits() Limits {
	return Limits{CPUs: float64(max(runtime.NumCPU()/2, 1)), MemoryBytes: 16 << 30, Pids: 16384, DiskBytes: 100 << 30}
}

// Handle answers one gate daemon command (CommandStart, CommandStatus,
// CommandCancel): the daemon's command handler is this, over its Jobs.
func (j *Jobs) Handle(ctx context.Context, command string, payload []byte) ([]byte, error) {
	switch command {
	case CommandStart:
		var req StartRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		return json.Marshal(j.Start(ctx, req))
	case CommandStatus, CommandCancel:
		var req StatusRequest
		if err := json.Unmarshal(payload, &req); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
		if command == CommandCancel {
			j.Cancel(req.GateID)
			return []byte("{}"), nil
		}
		return json.Marshal(j.Status(req.GateID))
	}
	return nil, fmt.Errorf("not a gate command: %q", command)
}

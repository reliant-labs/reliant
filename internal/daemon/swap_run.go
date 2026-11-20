// Copyright (c) 2026 Reliant Labs
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/cgroupmem"
	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/hotswap"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/osutil"
	"github.com/reliant-labs/reliant/internal/proctree"
)

// Foreground commands that survive a hot swap (internal/hotswap).
//
// With hot swap enabled (a self-hosted daemon started with self_update), a
// command RunCommand runs for a tool request is started on the hotswap
// spawner with output pipes this file owns, instead of os/exec's, so that at a
// swap the pipes can be handed to the next image of the daemon:
//
//   - Two pumps copy the pipes into the command's buffers. FreezeRuns stops
//     them between reads with a read deadline, so every byte is either in a
//     buffer (and travels in the handoff) or still in the pipe (and travels
//     with the descriptor). Nothing is read twice or lost.
//   - The exit is detected without reaping it (waitid WNOWAIT). A run frozen
//     for a swap is never reaped by this image: its exit status is the next
//     image's, which collects it with wait4 (same pid, still its child).
//   - In the next image, the tool request is executed again through the
//     normal path; RunCommand finds the request id among the adopted runs
//     (AdoptRuns) and, instead of starting anything, finishes the running
//     command: same buffers, same start time, same wall-clock deadline. The
//     result is the same shape a run with no swap returns.
//
// With hot swap off nothing here runs and RunCommand is unchanged.

type ctxKeyToolRequestID struct{}

// WithToolRequestID tags ctx with the tool request a command runs for; a
// command is handed over across a hot swap under that id.
func WithToolRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyToolRequestID{}, requestID)
}

func toolRequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyToolRequestID{}).(string)
	return id
}

// HandoverKindRun is the hotswap.Process kind of a foreground command.
const HandoverKindRun = "run"

// runHandover is what crosses the exec for one foreground command, besides
// its pid and pipes.
type runHandover struct {
	RequestID string    `json:"request_id"`
	Stdout    []byte    `json:"stdout,omitempty"`
	Stderr    []byte    `json:"stderr,omitempty"`
	Start     time.Time `json:"start"`
	// Deadline is the command's wall-clock deadline (ExecDeadline).
	Deadline time.Time `json:"deadline"`
	Heavy    bool      `json:"heavy,omitempty"`
	// Cgroup is the command's own cgroup (proctree), removed once it exits.
	Cgroup string `json:"cgroup,omitempty"`
}

const (
	runRunning = iota
	runFrozen
	runFinishing
)

// swapRun is one foreground command a hot swap can hand over.
type swapRun struct {
	requestID string
	cmd       *exec.Cmd
	pid       int
	outR      *os.File
	errR      *os.File
	outBuf    *bytes.Buffer
	errBuf    *bytes.Buffer
	activity  *shell.OutputActivity
	start     time.Time
	deadline  time.Time
	heavy     bool
	cgroup    string

	mu     sync.Mutex
	state  int
	thawCh chan struct{}
	pumps  sync.WaitGroup
}

// runRegistry is the process's swappable foreground commands, and the ones a
// previous image handed over that have not been resumed yet.
type runRegistry struct {
	mu      sync.Mutex
	runs    map[string]*swapRun
	adopted map[string]*swapRun
}

var swapRuns = &runRegistry{runs: map[string]*swapRun{}, adopted: map[string]*swapRun{}}

func (r *runRegistry) add(run *swapRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs[run.requestID] = run
}

func (r *runRegistry) remove(run *swapRun) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.runs[run.requestID] == run {
		delete(r.runs, run.requestID)
	}
}

func (r *runRegistry) takeAdopted(requestID string) *swapRun {
	r.mu.Lock()
	defer r.mu.Unlock()
	run := r.adopted[requestID]
	delete(r.adopted, requestID)
	return run
}

// SwappableRuns is how many foreground commands a hot swap can hand over now:
// started and not yet finished.
func SwappableRuns() int {
	swapRuns.mu.Lock()
	runs := make([]*swapRun, 0, len(swapRuns.runs))
	for _, run := range swapRuns.runs {
		runs = append(runs, run)
	}
	swapRuns.mu.Unlock()
	n := 0
	for _, run := range runs {
		run.mu.Lock()
		if run.state == runRunning {
			n++
		}
		run.mu.Unlock()
	}
	return n
}

// FreezeRuns stops every swappable foreground command's pumps and describes
// each for the handoff. thaw undoes it (the exec failed); after a successful
// exec nothing here runs again. It fails — with everything thawed — when a
// command is finishing (its exit was seen and its output is draining): the
// caller waits and tries again.
func FreezeRuns() (procs []hotswap.Process, thaw func(), err error) {
	swapRuns.mu.Lock()
	runs := make([]*swapRun, 0, len(swapRuns.runs))
	for _, run := range swapRuns.runs {
		runs = append(runs, run)
	}
	swapRuns.mu.Unlock()

	var frozen []*swapRun
	thaw = func() {
		for _, run := range frozen {
			run.thaw()
		}
	}
	for _, run := range runs {
		if !run.freeze() {
			thaw()
			return nil, func() {}, fmt.Errorf("command for request %s is finishing", run.requestID)
		}
		frozen = append(frozen, run)
		meta, merr := json.Marshal(runHandover{
			RequestID: run.requestID,
			Stdout:    run.outBuf.Bytes(),
			Stderr:    run.errBuf.Bytes(),
			Start:     run.start,
			Deadline:  run.deadline,
			Heavy:     run.heavy,
			Cgroup:    run.cgroup,
		})
		if merr != nil {
			thaw()
			return nil, func() {}, merr
		}
		procs = append(procs, hotswap.Process{
			Kind: HandoverKindRun, ID: run.requestID, PID: run.pid, Meta: meta,
			Files: map[string]*os.File{"stdout": run.outR, "stderr": run.errR},
		})
	}
	return procs, thaw, nil
}

// AdoptRuns takes over the foreground commands a previous image handed over.
// Each waits, still running, for its tool request to be executed again
// (RunCommand), which finishes it. It returns the request ids adopted.
func AdoptRuns(procs []hotswap.Process) []string {
	var ids []string
	for _, p := range procs {
		if p.Kind != HandoverKindRun {
			continue
		}
		var h runHandover
		if err := json.Unmarshal(p.Meta, &h); err != nil || h.RequestID == "" {
			logging.Warn("Hot swap: a handed-over command could not be read; it runs on unattended",
				"pid", p.PID, "error", err)
			continue
		}
		proc, err := os.FindProcess(p.PID)
		if err != nil {
			continue
		}
		run := &swapRun{
			requestID: h.RequestID,
			cmd:       &exec.Cmd{Process: proc},
			pid:       p.PID,
			outR:      p.Files["stdout"],
			errR:      p.Files["stderr"],
			outBuf:    bytes.NewBuffer(h.Stdout),
			errBuf:    bytes.NewBuffer(h.Stderr),
			activity:  shell.NewOutputActivity(nil),
			start:     h.Start,
			deadline:  h.Deadline,
			heavy:     h.Heavy,
			cgroup:    h.Cgroup,
			state:     runRunning,
		}
		swapRuns.mu.Lock()
		swapRuns.adopted[h.RequestID] = run
		swapRuns.mu.Unlock()
		ids = append(ids, h.RequestID)
	}
	return ids
}

// startPumps copies the pipes into the buffers until EOF or a read deadline.
func (run *swapRun) startPumps() {
	for _, p := range []struct {
		f *os.File
		w io.Writer
	}{{run.outR, run.activity.Writer(run.outBuf)}, {run.errR, run.activity.Writer(run.errBuf)}} {
		if p.f == nil {
			continue
		}
		run.pumps.Add(1)
		go func(f *os.File, w io.Writer) {
			defer run.pumps.Done()
			buf := make([]byte, 32<<10)
			for {
				n, err := f.Read(buf)
				if n > 0 {
					_, _ = w.Write(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}(p.f, p.w)
	}
}

// stopPumps stops the pumps between reads and waits for them.
func (run *swapRun) stopPumps() {
	for _, f := range []*os.File{run.outR, run.errR} {
		if f != nil {
			_ = f.SetReadDeadline(time.Now())
		}
	}
	run.pumps.Wait()
}

func (run *swapRun) freeze() bool {
	run.mu.Lock()
	if run.state != runRunning {
		run.mu.Unlock()
		return false
	}
	run.state = runFrozen
	run.thawCh = make(chan struct{})
	run.mu.Unlock()
	run.stopPumps()
	return true
}

func (run *swapRun) thaw() {
	for _, f := range []*os.File{run.outR, run.errR} {
		if f != nil {
			_ = f.SetReadDeadline(time.Time{})
		}
	}
	run.startPumps()
	run.mu.Lock()
	run.state = runRunning
	close(run.thawCh)
	run.mu.Unlock()
}

// wait reaps the command once it exits, unless a swap froze it first: then
// its exit status is the next image's. It sends cmd.Wait's error on waitCh.
func (run *swapRun) wait(waitCh chan<- error) {
	for {
		if err := waitExitNoReap(run.pid); err != nil {
			// ECHILD and the like: nothing left to watch for. Let Wait say.
			break
		}
		run.mu.Lock()
		if run.state == runFrozen {
			ch := run.thawCh
			run.mu.Unlock()
			<-ch // thawed: the swap did not happen
			continue
		}
		run.state = runFinishing
		run.mu.Unlock()
		break
	}
	waitCh <- run.cmd.Wait()
}

// drainAfterExit waits for the pumps to reach EOF, bounded like os/exec's
// WaitDelay: a grandchild holding the pipe open must not hold the result.
func (run *swapRun) drainAfterExit(err error) error {
	done := make(chan struct{})
	go func() {
		run.pumps.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(ExecWaitDelay):
		run.stopPumps()
		if err == nil {
			err = exec.ErrWaitDelay
		}
	}
	for _, f := range []*os.File{run.outR, run.errR} {
		if f != nil {
			_ = f.Close()
		}
	}
	return err
}

// newPipes makes the command's output pipes: the write ends go to cmd, the
// read ends stay here.
func newPipes(cmd *exec.Cmd) (outR, errR *os.File, closeWrite func(), err error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return nil, nil, nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	return outR, errR, func() { _ = outW.Close(); _ = errW.Close() }, nil
}

// errNotSwappable is runSwappable declining a command it cannot carry; the
// caller runs it the ordinary way.
var errNotSwappable = errors.New("not swappable")

// runSwappable runs cmd, prepared by RunCommand, as a command a hot swap can
// hand over. tree, heavy and oomSnap are RunCommand's.
func (c *LocalClient) runSwappable(ctx context.Context, req *RunCommandRequest, requestID string, cmd *exec.Cmd,
	timeout time.Duration, deadline *ExecDeadline, tree *proctree.Tree, heavy bool, stopGrace func()) (*CommandResult, error) {
	outR, errR, closeWrite, err := newPipes(cmd)
	if err != nil {
		return nil, errNotSwappable
	}
	// Only the escalation after a cancel: there are no copying goroutines.
	cmd.WaitDelay = ExecWaitDelay
	oomSnap := localMemReader.SnapshotOOMKills()
	start := time.Now()
	err = hotswap.StartCmd(cmd)
	closeWrite()
	// The child holds its own copies of the sandbox's descriptors now.
	credsandbox.Release(cmd)
	if err != nil {
		tree.Started(0)
		_ = outR.Close()
		_ = errR.Close()
		deadline.Release()
		stopGrace()
		outcome := ClassifyExecOutcome(err, deadline.Err(), "", "", localMemReader, oomSnap, diskwatch.Default())
		return &CommandResult{ExitCode: outcome.ExitCode, Stderr: outcome.Stderr, Combined: outcome.Stderr}, nil
	}
	tree.Started(cmd.Process.Pid)
	lowerChildPriorities(ctx, cmd.Process.Pid)
	wallDeadline := start.Add(timeout)
	run := &swapRun{
		requestID: requestID, cmd: cmd, pid: cmd.Process.Pid, outR: outR, errR: errR,
		outBuf: &bytes.Buffer{}, errBuf: &bytes.Buffer{}, activity: shell.NewOutputActivity(nil),
		start: start, deadline: wallDeadline, heavy: heavy, cgroup: tree.Cgroup(), state: runRunning,
	}
	return c.finishSwapRun(ctx, req, run, deadline, tree, stopGrace, oomSnap)
}

// resumeRun finishes a command a previous image handed over (AdoptRuns).
func (c *LocalClient) resumeRun(ctx context.Context, req *RunCommandRequest, run *swapRun) (*CommandResult, error) {
	remaining := time.Until(run.deadline)
	if remaining < time.Second {
		remaining = time.Second
	}
	deadline := StartExecDeadline(ctx, remaining)
	stopGrace := osutil.ApplyGracefulCancel(run.cmd, ExecGraceDelay)
	// The command was started by the previous image, so nothing in os/exec
	// watches this deadline for it: cancel it here as os/exec would.
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-deadline.Context().Done():
			_ = run.cmd.Cancel()
		case <-finished:
		}
	}()
	logging.Info("Hot swap: resuming a command the previous image started",
		"request_id", run.requestID, "pid", run.pid, "running_for", time.Since(run.start).Round(time.Second))
	res, err := c.finishSwapRun(ctx, req, run, deadline, nil, stopGrace, localMemReader.SnapshotOOMKills())
	if run.cgroup != "" {
		_ = os.Remove(run.cgroup) // empty once its command exited; best effort
	}
	return res, err
}

// finishSwapRun waits for run, keeping it swappable while it runs, and
// builds RunCommand's result.
func (c *LocalClient) finishSwapRun(ctx context.Context, req *RunCommandRequest, run *swapRun, deadline *ExecDeadline,
	tree *proctree.Tree, stopGrace func(), oomSnap cgroupmem.OOMSnapshot) (*CommandResult, error) {
	run.startPumps()
	swapRuns.add(run)
	waitCh := make(chan error, 1)
	go run.wait(waitCh)

	bgResp, backgrounded := PollForBackgroundDetach(ctx, DetachOptions{
		Cmd:        run.cmd,
		Command:    req.Command,
		WorkingDir: req.WorkingDir,
		StartTime:  run.start,
		StdoutBuf:  run.outBuf,
		StderrBuf:  run.errBuf,
		WaitErrCh:  waitCh,
		Tree:       tree,
		ChatID:     req.ChatID,
		ThreadID:   req.ThreadID,
		KeepAlive:  req.KeepAlive,
		Activity:   run.activity,
	})
	if backgrounded {
		// The background manager owns it now; a swap does not carry it as a
		// foreground command.
		swapRuns.remove(run)
		deadline.Release()
		return &bgResp, nil
	}
	err := <-waitCh
	swapRuns.remove(run)
	err = run.drainAfterExit(err)
	tree.Release()
	deadline.Release()
	stopGrace()

	stdout := run.outBuf.String()
	outcome := ClassifyExecOutcome(err, deadline.Err(), stdout, run.errBuf.String(), localMemReader, oomSnap, diskwatch.Default())
	outcome.NoteHeavyTimeout(run.heavy)
	outcome.NoteThreadLimit(tree.LimitNote())
	return &CommandResult{
		ExitCode:         outcome.ExitCode,
		Stdout:           stdout,
		Stderr:           outcome.Stderr,
		Combined:         CombineOutput(stdout, outcome.Stderr),
		DurationMs:       time.Since(run.start).Milliseconds(),
		TimedOut:         outcome.TimedOut,
		OOMKilled:        outcome.OOMKilled,
		OutputIncomplete: outcome.OutputIncomplete,
	}, nil
}

// lowerChildPriorities steers the kernel OOM killer toward the command and
// lowers its CPU and I/O priority, as RunCommand does; best effort.
func lowerChildPriorities(ctx context.Context, pid int) {
	if err := osutil.AdjustChildOOMScore(pid); err != nil {
		logging.DebugContext(ctx, "Failed to adjust command oom_score_adj", "pid", pid, "error", err)
	}
	if err := osutil.LowerChildPriority(pid); err != nil {
		logging.DebugContext(ctx, "Failed to lower command priority", "pid", pid, "error", err)
	}
	if err := osutil.LowerChildIOPriority(pid); err != nil {
		logging.DebugContext(ctx, "Failed to lower command I/O priority", "pid", pid, "error", err)
	}
}

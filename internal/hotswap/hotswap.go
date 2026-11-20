// Copyright (c) 2026 Reliant Labs

// Package hotswap replaces the running daemon's binary image without stopping
// the commands it runs: the daemon execs the new binary in its own process
// (same pid), and the new image takes over every running command — its
// process, its output pipes and its exit status — from a handoff the old
// image wrote just before the exec.
//
// Why it is safe. A workflow run's state lives on the server (the worker and
// Temporal), never in the daemon. What lives on the machine is the tool calls
// in flight and the processes they run. The workers already ride out a daemon
// that drops its stream: they wait for it to come back, ask it what became of
// each request (tool.status) and keep waiting while it says "running"
// (toolexec/tool_result_redelivery.go). So a daemon that comes back still
// running those commands, under the same request ids, finishes every call as
// if nothing had happened. No run has to be drained.
//
// What makes a command survive the exec, on Linux:
//
//   - Same pid. execve keeps the process, so a command stays this process's
//     child and the new image collects its exit status with wait4. A restart
//     by the supervisor would orphan it to init, and its exit status would be
//     lost.
//   - Its output pipes stay open. Go opens every descriptor close-on-exec, so
//     the read ends would close at the exec and the command would die of
//     SIGPIPE on its next write. [Exec] clears close-on-exec on exactly the
//     descriptors the handoff names, and [Restore] re-wraps them. The pipe
//     buffers what the command writes during the exec.
//   - Its parent-death signal does not fire. The credentials:none sandbox's
//     init sets PR_SET_PDEATHSIG (credsandbox_linux.go), which the kernel sends
//     when the THREAD that forked the init exits, and execve ends every thread
//     but the one calling it. [Spawner] starts every command on one locked OS
//     thread, and [Exec] runs execve on that same thread, so the parent of
//     every sandbox init survives the exec. A command started on any other
//     thread with a parent-death signal is killed by the exec (the test proves
//     both halves).
//
// Nothing else crosses: there is no in-memory state handoff. The new image
// starts from durable state (the result outbox, the background journal) plus
// the handoff's list of running processes.
//
//forge:exclude-contract: one concrete mechanism over this process's own threads and descriptors (execve, fcntl, memfd); there is no second implementation to substitute
package hotswap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"sync/atomic"
)

// StateEnv names the descriptor the new image reads its handoff from. Restore
// removes it from the environment, so nothing the new image runs inherits it.
const StateEnv = "RELIANT_HOTSWAP_STATE_FD"

// handoffVersion is the handoff's format. An image that reads a newer one
// than it knows takes over nothing and says so: the old image's commands then
// run on, orphaned, and their calls end as "interrupted by daemon restart".
const handoffVersion = 1

// ErrUnsupported is returned where a running image cannot hand over its
// commands (anywhere but Linux).
var ErrUnsupported = errors.New("hotswap: replacing the running image is supported on Linux only")

// Process is one running command handed from the old image to the new.
type Process struct {
	// Kind says which part of the daemon owns it ("run" for a synchronous
	// command, "background" for a background process); the owner reads Meta.
	Kind string `json:"kind"`
	// ID is the owner's key: a tool request id, a background process id.
	ID string `json:"id"`
	// PID is the command's process. It is a child of this process, before
	// and after the exec.
	PID int `json:"pid"`
	// Meta is whatever the owner needs to finish the call in the new image.
	Meta json.RawMessage `json:"meta,omitempty"`
	// Files are the descriptors that must stay open across the exec, by name
	// ("stdout", "stderr"). In the new image they are fresh *os.File values
	// over the same descriptors.
	Files map[string]*os.File `json:"-"`
}

// wireProcess is a Process as the handoff stores it.
type wireProcess struct {
	Process
	FDs map[string]int `json:"fds,omitempty"`
}

type handoff struct {
	Version   int           `json:"version"`
	Processes []wireProcess `json:"processes"`
	// Extra carries named descriptors that belong to no one process, such as
	// the sandbox liveness pipe every sandbox init holds the other end of.
	Extra map[string]int `json:"extra,omitempty"`
}

// Spawner runs command starts on one locked OS thread: the thread [Exec]
// later runs execve on. The zero value is not usable; call [NewSpawner].
type Spawner struct {
	work chan func()
}

// NewSpawner starts the spawner's thread. It runs for the life of the process.
func NewSpawner() *Spawner {
	s := &Spawner{work: make(chan func())}
	go func() {
		// Never unlocked: a locked goroutine that returns ends its thread,
		// and that would fire the parent-death signal of every command
		// started here.
		runtime.LockOSThread()
		for f := range s.work {
			f()
		}
	}()
	return s
}

// Do runs f on the spawner's thread and waits for it. f must not block for
// long: every command start on the machine queues behind it.
func (s *Spawner) Do(f func()) {
	done := make(chan struct{})
	s.work <- func() {
		defer close(done)
		f()
	}
	<-done
}

// Start starts cmd on the spawner's thread.
func (s *Spawner) Start(cmd *exec.Cmd) error {
	var err error
	s.Do(func() { err = cmd.Start() })
	return err
}

// Exec replaces this process's image with path, keeping every process in
// procs (and extra's descriptors) open across the exec, and returns only if
// the exec failed — in which case every descriptor is close-on-exec again and
// the caller still owns everything, unchanged.
//
// The caller must have stopped everything that reads from procs' Files
// before calling Exec: what the old image has not read stays in the pipe for
// the new one. argv and env are the new image's; Exec appends [StateEnv].
func (s *Spawner) Exec(path string, argv, env []string, procs []Process, extra map[string]*os.File) error {
	h := handoff{Version: handoffVersion, Extra: map[string]int{}}
	var keep []*os.File
	for _, p := range procs {
		w := wireProcess{Process: p, FDs: map[string]int{}}
		for _, name := range sortedNames(p.Files) {
			f := p.Files[name]
			fd, err := rawFD(f)
			if err != nil {
				return err
			}
			w.FDs[name] = fd
			keep = append(keep, f)
		}
		h.Processes = append(h.Processes, w)
	}
	for _, name := range sortedNames(extra) {
		fd, err := rawFD(extra[name])
		if err != nil {
			return err
		}
		h.Extra[name] = fd
		keep = append(keep, extra[name])
	}
	state, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("hotswap: encode the handoff: %w", err)
	}
	stateFile, err := stateCarrier(state)
	if err != nil {
		return err
	}
	defer stateFile.Close()
	keep = append(keep, stateFile)
	stateFD, err := rawFD(stateFile)
	if err != nil {
		return err
	}
	env = append(withoutEnv(env, StateEnv), StateEnv+"="+strconv.Itoa(stateFD))

	var execErr error
	s.Do(func() {
		if err := setInheritable(keep, true); err != nil {
			execErr = err
		} else {
			execErr = execve(path, argv, env)
		}
		// Only reached when the exec failed: the caller keeps serving.
		_ = setInheritable(keep, false)
	})
	return fmt.Errorf("hotswap: exec %s: %w", path, execErr)
}

// Restore reads the handoff the previous image left, if any. It returns the
// processes to take over and the extra descriptors, each descriptor re-opened
// close-on-exec. ok is false when this image was not started by a hot swap.
// It removes [StateEnv] from the environment either way.
func Restore() (procs []Process, extra map[string]*os.File, ok bool, err error) {
	raw, set := os.LookupEnv(StateEnv) //nolint:forbidigo // not configuration: the descriptor number one image of this process hands the next across execve

	if !set {
		return nil, nil, false, nil
	}
	_ = os.Unsetenv(StateEnv)
	fd, err := strconv.Atoi(raw)
	if err != nil || fd < 0 {
		return nil, nil, true, fmt.Errorf("hotswap: %s=%q is not a descriptor", StateEnv, raw)
	}
	stateFile := os.NewFile(uintptr(fd), "hotswap-state")
	defer stateFile.Close()
	state, err := readState(stateFile)
	if err != nil {
		return nil, nil, true, fmt.Errorf("hotswap: read the handoff: %w", err)
	}
	var h handoff
	if err := json.Unmarshal(state, &h); err != nil {
		return nil, nil, true, fmt.Errorf("hotswap: decode the handoff: %w", err)
	}
	// Wrap every descriptor first, so none leaks to a child even when the
	// handoff turns out to be one this image cannot take.
	extra = map[string]*os.File{}
	for name, fd := range h.Extra {
		extra[name] = adopt(fd, name)
	}
	for _, w := range h.Processes {
		p := w.Process
		p.Files = map[string]*os.File{}
		for name, fd := range w.FDs {
			p.Files[name] = adopt(fd, p.Kind+"-"+p.ID+"-"+name)
		}
		procs = append(procs, p)
	}
	if h.Version != handoffVersion {
		for _, p := range procs {
			closeAll(p.Files)
		}
		closeAll(extra)
		return nil, nil, true, fmt.Errorf("hotswap: handoff version %d, this image reads %d; taking over nothing", h.Version, handoffVersion)
	}
	return procs, extra, true, nil
}

// defaultSpawner is the process-wide spawner, set once by Enable.
var defaultSpawner atomic.Pointer[Spawner]

// Enable makes every command started through StartCmd start on one spawner
// thread, so this process can later hand them across an exec. It is opt-in
// (a self-hosted daemon started with self_update); with it off StartCmd is
// cmd.Start and nothing in this package runs. Call it before the first
// command starts; later calls return the same spawner.
func Enable() *Spawner {
	if !Supported {
		return nil
	}
	if s := defaultSpawner.Load(); s != nil {
		return s
	}
	defaultSpawner.CompareAndSwap(nil, NewSpawner())
	return defaultSpawner.Load()
}

// Enabled reports whether Enable was called.
func Enabled() bool { return defaultSpawner.Load() != nil }

// Default is the spawner Enable made, or nil.
func Default() *Spawner { return defaultSpawner.Load() }

// StartCmd starts cmd on the spawner when hot swap is enabled, and plainly
// otherwise. Every command a handover may carry must start through it.
func StartCmd(cmd *exec.Cmd) error {
	if s := defaultSpawner.Load(); s != nil {
		return s.Start(cmd)
	}
	return cmd.Start()
}

// rawFD is f's descriptor, read without f.Fd(), which would switch a pipe to
// blocking mode and take away its read deadlines for good.
func rawFD(f *os.File) (int, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("hotswap: %s: %w", f.Name(), err)
	}
	fd := -1
	if err := rc.Control(func(u uintptr) { fd = int(u) }); err != nil {
		return 0, fmt.Errorf("hotswap: %s: %w", f.Name(), err)
	}
	return fd, nil
}

// Wait collects pid's exit status. pid must be a child of this process —
// every handed-over process is.
func Wait(pid int) (*os.ProcessState, error) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil, err
	}
	return p.Wait()
}

func sortedNames(m map[string]*os.File) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func closeAll(m map[string]*os.File) {
	for _, f := range m {
		_ = f.Close()
	}
}

func withoutEnv(env []string, key string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if len(kv) > len(key) && kv[:len(key)] == key && kv[len(key)] == '=' {
			continue
		}
		out = append(out, kv)
	}
	return out
}

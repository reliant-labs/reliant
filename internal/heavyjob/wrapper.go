// Copyright (c) 2025 Reliant Labs
package heavyjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/reliant-labs/forge/pkg/hostslot"
)

// waitingMarker starts the line the wrapper prints when a command has to
// wait; WaitedFor looks for it.
const waitingMarker = "⏳ reliant: heavy command queued"

// Dispatch runs this process as the slot wrapper and exits when its first
// argument is WrapperArg, and returns at once otherwise. Call it first thing
// in main, before any command-line parsing: the wrapper's flags are its own.
func Dispatch() {
	if len(os.Args) < 2 || os.Args[1] != WrapperArg {
		return
	}
	// The wrapper polls a lock and then waits on its child: one P is all it
	// uses. Left at the machine's width, a waiter's scheduler and GC spread
	// over every CPU and it holds 15–19 threads, and a busy machine has
	// dozens of waiters against a thread limit (RLIMIT_NPROC counts
	// threads). The gate starts it with GOMAXPROCS=1 in its environment,
	// because by the time this runs package init has already grown those
	// threads; this call covers a wrapper started any other way. Neither
	// reaches the child: see --child-gomaxprocs.
	runtime.GOMAXPROCS(1)
	os.Exit(RunWrapper(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
}

// RunWrapper is `<self> __heavy-slot --dir D [--slots N] [--share N]
// [--child-gomaxprocs V] [--label L] -- argv...`: take a heavy slot, waiting
// while every slot is held, run argv as a child holding it, and return the
// child's exit status. The child runs on its share of the CPUs (see the
// package doc): GOMAXPROCS and UV_THREADPOOL_SIZE, each unless its
// environment already sets it, from --share or else DefaultShare of the
// pool. --child-gomaxprocs is the GOMAXPROCS the command was given before
// the gate started this wrapper on one P ("" for none); the child gets it
// back instead of the wrapper's 1.
//
// The child runs as a child rather than through exec(2) because the slot's
// lock is held by THIS process: a background process the command leaves
// behind cannot keep the slot after the command itself has finished.
//
// Nothing about the slot may fail the command. A slot directory that cannot
// be created or locked runs the command unqueued with a warning — a broken
// limiter is not a reason to refuse a build.
func RunWrapper(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	w, err := parseWrapperArgs(args)
	opt, argv := w.opt, w.argv
	if err != nil {
		fmt.Fprintf(stderr, "reliant %s: %v\n", WrapperArg, err)
		return 2
	}
	opt.Env = os.Environ() //nolint:forbidigo // forge hostslot reads its own FORGE_* slot variables from the environment it is given; not reliant configuration
	opt.Subtree = true
	notice := &waitNotice{w: stderr, label: opt.Label}
	opt.Out = notice

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	slot, err := hostslot.Acquire(ctx, hostslot.Heavy, opt)
	stop()
	switch {
	case err != nil && ctx.Err() != nil:
		// Stopped while waiting — the command's timeout, or its caller
		// cancelling it. It never started.
		fmt.Fprintf(stderr, "reliant: stopped while waiting for a heavy slot; `%s` never started\n", opt.Label)
		return 128 + int(syscall.SIGTERM)
	case err != nil:
		fmt.Fprintf(stderr, "⚠️  reliant: running without a heavy slot: %v\n", err)
		slot = nil
	}
	defer func() { _ = slot.Release() }()
	if slot != nil && notice.headed {
		fmt.Fprintf(stderr, "▶ reliant: running `%s` in %s after %s waiting\n", opt.Label, slot, slot.Waited().Round(time.Second))
	}

	share := shareOf(slot, w.share)
	// A hard cap, not only GOMAXPROCS: it binds what GOMAXPROCS does not
	// reach (cpumax_linux.go). Set before the child starts, so nothing of
	// the command runs uncapped.
	_ = limitCPU(share)
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(childEnv(os.Environ(), w.childGOMAXPROCS), slot.Env()...) //nolint:forbidigo // passes this process environment to a child process; not configuration
	c.Env = append(c.Env, shareEnv(c.Env, share)...)
	c.Stdin, c.Stdout, c.Stderr = stdin, stdout, stderr
	if err := c.Start(); err != nil {
		fmt.Fprintf(stderr, "reliant: %v\n", err)
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return 127
		}
		return 126
	}
	// The daemon signals the command's whole process group, which reaches the
	// child directly; forwarding covers a signal sent to this pid alone.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				_ = c.Process.Signal(s)
			case <-done:
				return
			}
		}
	}()
	err = c.Wait()
	signal.Stop(sigs)
	close(done)
	return exitStatus(err)
}

// exitStatus is the shell's view of how a child ended: its exit code, or
// 128+N when signal N killed it (so a SIGKILL from the OOM killer still reads
// as 137 to the daemon that attributes it).
func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 1
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	if code := ee.ExitCode(); code > 0 {
		return code
	}
	return 1
}

// wrapperArgs is the wrapper's command line.
type wrapperArgs struct {
	opt hostslot.Options
	// share is --share; zero derives it.
	share int
	// childGOMAXPROCS is --child-gomaxprocs; nil when absent (the wrapper
	// was not started on one P by the gate, so its environment is the
	// child's as it stands).
	childGOMAXPROCS *string
	argv            []string
}

func parseWrapperArgs(args []string) (wrapperArgs, error) {
	var w wrapperArgs
	for len(args) > 0 {
		flag := args[0]
		if flag == "--" {
			args = args[1:]
			break
		}
		if len(args) < 2 {
			return w, fmt.Errorf("flag %s needs a value", flag)
		}
		val := args[1]
		args = args[2:]
		switch flag {
		case "--dir":
			w.opt.Dir = val
		case "--label":
			w.opt.Label = val
		case "--child-gomaxprocs":
			w.childGOMAXPROCS = &val
		case "--slots", "--share":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return w, fmt.Errorf("%s %q: want a positive number", flag, val)
			}
			if flag == "--slots" {
				w.opt.Slots = n
			} else {
				w.share = n
			}
		default:
			return w, fmt.Errorf("unknown flag %s", flag)
		}
	}
	if len(args) == 0 {
		return w, errors.New("no command given after --")
	}
	if w.opt.Label == "" {
		w.opt.Label = fmt.Sprint(args)
	}
	w.argv = args
	return w, nil
}

// waitNotice is the writer hostslot prints its waiting lines to. Before the
// first one it says, once, in reliant's words, what is happening and why;
// hostslot's own line then names the holders and the queue.
//
// The header and that first line go out in ONE write. A reader of the
// command's output (an agent's tool result, or WaitedFor polling it) that sees
// the marker must also see who holds the slot: written separately, the header
// could be read on its own.
type waitNotice struct {
	w      io.Writer
	label  string
	headed bool
}

func (n *waitNotice) Write(p []byte) (int, error) {
	if n.headed {
		return n.w.Write(p)
	}
	n.headed = true
	header := fmt.Sprintf("%s: `%s` waits for one of this machine's heavy slots (shared by every agent and forge lane) before it runs\n", waitingMarker, n.label)
	written, err := n.w.Write(append([]byte(header), p...))
	// The caller asked to write p: report how much of it went out.
	return max(written-len(header), 0), err
}

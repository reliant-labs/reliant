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
	os.Exit(RunWrapper(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
}

// RunWrapper is `<self> __heavy-slot --dir D [--slots N] [--label L] -- argv...`:
// take a heavy slot, waiting while every slot is held, run argv as a child
// holding it, and return the child's exit status.
//
// The child runs as a child rather than through exec(2) because the slot's
// lock is held by THIS process: a background process the command leaves
// behind cannot keep the slot after the command itself has finished.
//
// Nothing about the slot may fail the command. A slot directory that cannot
// be created or locked runs the command unqueued with a warning — a broken
// limiter is not a reason to refuse a build.
func RunWrapper(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opt, argv, err := parseWrapperArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "reliant %s: %v\n", WrapperArg, err)
		return 2
	}
	opt.Env = os.Environ()
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

	c := exec.Command(argv[0], argv[1:]...)
	c.Env = append(os.Environ(), slot.Env()...)
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

func parseWrapperArgs(args []string) (hostslot.Options, []string, error) {
	var opt hostslot.Options
	for len(args) > 0 {
		flag := args[0]
		if flag == "--" {
			args = args[1:]
			break
		}
		if len(args) < 2 {
			return opt, nil, fmt.Errorf("flag %s needs a value", flag)
		}
		val := args[1]
		args = args[2:]
		switch flag {
		case "--dir":
			opt.Dir = val
		case "--label":
			opt.Label = val
		case "--slots":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return opt, nil, fmt.Errorf("--slots %q: want a positive number", val)
			}
			opt.Slots = n
		default:
			return opt, nil, fmt.Errorf("unknown flag %s", flag)
		}
	}
	if len(args) == 0 {
		return opt, nil, errors.New("no command given after --")
	}
	if opt.Label == "" {
		opt.Label = fmt.Sprint(args)
	}
	return opt, args, nil
}

// waitNotice is the writer hostslot prints its waiting lines to. Before the
// first one it says, once, in reliant's words, what is happening and why;
// hostslot's own line then names the holders and the queue.
type waitNotice struct {
	w      io.Writer
	label  string
	headed bool
}

func (n *waitNotice) Write(p []byte) (int, error) {
	if !n.headed {
		n.headed = true
		fmt.Fprintf(n.w, "%s: `%s` waits for one of this machine's heavy slots (shared by every agent and forge lane) before it runs\n", waitingMarker, n.label)
	}
	return n.w.Write(p)
}

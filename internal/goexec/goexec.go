// Package goexec configures the `go` subprocesses forge runs on its own
// behalf. Graceful makes a cancelled one clean up after itself; Env keeps a
// caller's GOFLAGS=-mod=mod from letting one rewrite go.mod and go.sum (see
// env.go).
//
// # The leak
//
// `cmd/go` and `cmd/link` do their work in scratch directories under
// TMPDIR — `go-build*` for the build cache staging area, `go-link-*` for
// the linker's object inputs, which for a large binary is hundreds of
// megabytes. Both tools remove their own scratch on exit, including on
// SIGINT and SIGTERM, which they trap.
//
// They cannot remove it on SIGKILL, because nothing can.
//
// exec.CommandContext's default cancellation is exactly SIGKILL: when the
// context is done it calls Process.Kill. So every forge build lane running
// under a cancellable context — a `forge env up` the developer ^C's, a
// deadline that fires, an agent that gives up on a run — orphans one
// scratch directory per interrupted `go` invocation, permanently. Measured
// on one developer machine: 128 `go-link-*` directories totalling 15 GB,
// plus 27 `go-build*` at 5.4 GB. Nothing ever reclaims them, because from
// the outside they are indistinguishable from the scratch of a build that
// is still running.
//
// # The fix
//
// Graceful replaces the default kill with SIGINT and bounds the wait, so
// the toolchain gets the signal it already handles and the few hundred
// milliseconds it needs to unlink its scratch. WaitDelay is the backstop:
// if the child has not exited when it elapses, Go kills the process and
// closes the pipes, so a wedged compiler cannot turn a cancelled build
// into a hang. A cancel that reclaims nothing is still strictly better
// than the SIGKILL it replaced.
//
// This is deliberately narrow. It is for subprocesses whose own exit path
// does cleanup worth waiting for — the Go toolchain is the case that
// motivated it. A process with no cleanup to do gains nothing and loses
// up to WaitDelay of shutdown latency, so do not reach for it by reflex.
//
//forge:exclude-contract: pure configuration of a caller-owned exec.Cmd and its environment; there is no component to bind and no I/O client to fake
package goexec

import (
	"os"
	"os/exec"
	"time"
)

// GraceDelay is how long a signalled child may take to exit before it is
// killed outright. Ten seconds is generous for unlinking a scratch
// directory and still short enough that a ^C feels like a ^C.
const GraceDelay = 10 * time.Second

// Graceful configures cmd so that cancelling its context asks the child
// to exit rather than killing it, and returns cmd for call-site brevity.
//
// Call it after exec.CommandContext and before Run/Output/CombinedOutput.
// It is a no-op on a nil cmd and on a cmd built without a context (Cancel
// is only consulted by CommandContext), so a call site that later loses
// its context does not silently change meaning.
//
// On Windows there is no SIGINT to deliver: os.Interrupt is unsupported
// for Process.Signal there, and returning that error from Cancel makes Go
// fall back to its own Kill. That is the pre-existing behaviour, so
// Windows is no worse off and needs no build-tagged variant.
func Graceful(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return nil
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// os.ErrProcessDone here is not a failure — the child won the
		// race with the cancellation. Wait treats it as such.
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = GraceDelay
	return cmd
}

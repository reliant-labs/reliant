package frontenddeps

import (
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/reliant-labs/forge/internal/buildlog"
)

// output is where an install reports: forge's own progress lines and the
// package manager's output. The zero value is the terminal — stdout, with the
// package manager's output through buildlog, which files it under a build's
// session when one is active — what `forge env up` and `forge build` show.
// A caller that must not print (`forge lint --install` under --quiet or
// --json) names a writer instead, io.Discard included. A failed install's
// error carries the package manager's output whichever it is, so silencing
// the progress never hides why an install failed.
type output struct{ w io.Writer }

func (o output) printf(format string, a ...any) {
	if o.w == nil {
		fmt.Printf(format, a...)
		return
	}
	fmt.Fprintf(o.w, format, a...)
}

// warn is a line the terminal shows on stderr.
func (o output) warn(msg string) {
	if o.w == nil {
		fmt.Fprintln(os.Stderr, msg)
		return
	}
	fmt.Fprintln(o.w, msg)
}

// step returns the writers one package-manager run streams to.
func (o output) step(label string) (stdout, stderr io.Writer, end func(error)) {
	if o.w == nil {
		return buildlog.Step(label)
	}
	return o.w, o.w, func(error) {}
}

// lockedWriter serializes writes through mu: exec copies a command's stdout
// and stderr on goroutines of their own, and both streams of an install land
// in one capture buffer (and, for a named writer, one destination).
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

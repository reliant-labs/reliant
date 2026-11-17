package cliutil

import (
	"io"
	"log"
	"os"
)

// redirectOutput points the process's output at f until the returned restore
// is called: os.Stdout, os.Stderr and the standard logger. That reaches all
// of it — forge's own fmt.Print* lines, the log and slog defaults, and every
// child process started meanwhile with the process's stdout or stderr (an
// exec.Cmd takes os.Stdout's value when it is built). A file, not a pipe: a
// child that leaves a grandchild holding the descriptor cannot stall a
// reader waiting for EOF, because there is no reader.
//
// It is process-wide, as the output is. The commands that use it hold the
// terminal for their whole run, and an in-process embedding that runs forge
// commands (`forge mcp`) swaps the same variables and runs one at a time.
func redirectOutput(f *os.File) (restore func()) {
	prevOut, prevErr, prevLog := os.Stdout, os.Stderr, log.Writer()
	os.Stdout, os.Stderr = f, f
	log.SetOutput(f)
	return func() {
		os.Stdout, os.Stderr = prevOut, prevErr
		log.SetOutput(prevLog)
	}
}

// HoldOutputOnTerminal holds back what the process prints, until the returned
// release is called, when both stdout and stderr are terminals; then release
// prints it all to stdout, in the order it was written. Anywhere else it
// holds nothing and release does nothing.
//
// It is what lets a step that prints its own progress run under a terminal
// heartbeat: the status line is redrawn in place with "\r", and a line the
// step printed meanwhile would be glued onto the end of it. Held, the step's
// lines arrive after the heartbeat has erased itself. Off a terminal there is
// no status line to collide with, and the output streams as it always did.
//
// The heartbeat itself must be given the terminal from BEFORE the hold
// (capture os.Stderr first), or it would be held too.
func HoldOutputOnTerminal() (release func()) {
	if !IsTerminal(os.Stdout) || !IsTerminal(os.Stderr) {
		return func() {}
	}
	f, err := os.CreateTemp("", "forge-held-output-*.log")
	if err != nil {
		return func() {}
	}
	restore := redirectOutput(f)
	return func() {
		restore()
		if _, err := f.Seek(0, io.SeekStart); err == nil {
			_, _ = io.Copy(os.Stdout, f)
		}
		_ = f.Close()
		_ = os.Remove(f.Name())
	}
}

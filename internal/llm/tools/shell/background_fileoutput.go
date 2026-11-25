// Copyright (c) 2026 Reliant Labs
package shell

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// File-backed output: what lets a background process outlive the daemon image
// that started it (forge docs/proposals/self-upgrade.md §5, phase R5).
//
// A process started with pipes dies with the image. Every descriptor Go opens
// is close-on-exec, so a self-update's exec closes the read ends of its output
// pipes, and the child takes EPIPE on its next write. A file-backed process
// instead writes its stdout and stderr straight into <journal>/<id>.stdout and
// <id>.stderr, opened O_APPEND and handed to it as its descriptors 1 and 2, so
// os/exec copies nothing and no descriptor of the daemon's sits between the
// child and its output. The daemon tails the two files into the same lines,
// subscribers and journal a piped process feeds. An exec loses nothing: the
// next image reopens the files at the offsets this one handed off
// (BackgroundManager.HandOffForExec) and goes on reading from there.
//
// Only where the daemon restarts in place (BackgroundManager.EnableFileOutput:
// an auto-updating daemon on Linux or macOS). Everywhere else processes keep
// their pipes, and with them output that arrives as it is written.

// tailInterval is how often a file-backed process's output files are read.
// A variable so tests can shorten it.
var tailInterval = 100 * time.Millisecond

const (
	stdoutFileSuffix = ".stdout"
	stderrFileSuffix = ".stderr"
	// tailReadBytes is one read of an output file.
	tailReadBytes = 64 << 10
	// punchEvery: once this much more of an output file has been read and
	// emitted, that part is released from disk (punchRead).
	punchEvery = 1 << 20
)

// outputFilePaths are id's two output files in dir: stdout, then stderr.
func outputFilePaths(dir, id string) [2]string {
	return [2]string{filepath.Join(dir, id+stdoutFileSuffix), filepath.Join(dir, id+stderrFileSuffix)}
}

// fileStream is one output file being tailed.
type fileStream struct {
	f       *os.File // read handle (read-write: punchRead needs write access)
	off     int64    // bytes read
	punched int64    // bytes released from disk
	w       *outputLineWriter
}

// emitted is the offset just past the last byte emitted as a line: what an
// unterminated last line holds is read again by whoever reads on from here.
func (s *fileStream) emitted() int64 { return s.off - int64(len(s.w.partial)) }

// fileOutput is a file-backed process's output.
type fileOutput struct {
	paths [2]string

	// mu serializes reads, and with them the writers' unterminated lines.
	mu      sync.Mutex
	streams [2]*fileStream
	buf     []byte
	closed  bool

	// loopMu guards the tail loop's channels; nil when it is not running.
	loopMu  sync.Mutex
	stop    chan struct{}
	stopped chan struct{}
}

// createFileOutput creates id's output files in dir and returns the
// descriptors the child writes them through (close them once it started)
// and the daemon's side, which reads them.
func createFileOutput(dir, id string) (child [2]*os.File, out *fileOutput, err error) {
	out = &fileOutput{paths: outputFilePaths(dir, id)}
	defer func() {
		if err != nil {
			closeFiles(child[:])
			out.discard()
		}
	}()
	for i, path := range out.paths {
		w, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return child, nil, err
		}
		child[i] = w
		r, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return child, nil, err
		}
		out.streams[i] = &fileStream{f: r}
	}
	return child, out, nil
}

// openFileOutput reopens id's output files in dir, to read on from offsets
// (stdout, stderr): a process this image adopted from the one before it.
func openFileOutput(dir, id string, offsets [2]int64) (*fileOutput, error) {
	out := &fileOutput{paths: outputFilePaths(dir, id)}
	for i, path := range out.paths {
		r, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			out.discardKeepFiles()
			return nil, err
		}
		off := max(offsets[i], 0)
		out.streams[i] = &fileStream{f: r, off: off, punched: off &^ (punchAlign - 1)}
	}
	return out, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// bind makes process the one the output's lines go to.
func (o *fileOutput) bind(process *BackgroundProcess) {
	o.streams[0].w = &outputLineWriter{process: process, buffer: process.stdout, outputType: "stdout"}
	o.streams[1].w = &outputLineWriter{process: process, buffer: process.stderr, outputType: "stderr"}
}

// pump reads what the files hold now and emits it as lines. Each file is read
// only up to the size it had when the pump began, so a process writing faster
// than this reads never holds the pump (or a finish) forever.
func (o *fileOutput) pump() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	if o.buf == nil {
		o.buf = make([]byte, tailReadBytes)
	}
	for _, s := range o.streams {
		info, err := s.f.Stat()
		if err != nil {
			continue
		}
		for end := info.Size(); s.off < end; {
			n, err := s.f.ReadAt(o.buf[:min(int64(len(o.buf)), end-s.off)], s.off)
			if n > 0 {
				_, _ = s.w.Write(o.buf[:n])
				s.off += int64(n)
			}
			if err != nil && !errors.Is(err, io.EOF) {
				logging.Debug("Background process output file unreadable", "path", s.f.Name(), "error", err)
				break
			}
			if n == 0 {
				break
			}
		}
		if s.emitted()-s.punched >= punchEvery {
			s.punched = punchRead(s.f, s.punched, s.emitted())
		}
	}
}

// startTailing starts reading the files every tailInterval, unless the
// output is finished.
func (o *fileOutput) startTailing() {
	o.loopMu.Lock()
	defer o.loopMu.Unlock()
	o.mu.Lock()
	closed := o.closed
	o.mu.Unlock()
	if o.stop != nil || closed {
		return
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	o.stop, o.stopped = stop, stopped
	go func() {
		defer close(stopped)
		tick := time.NewTicker(tailInterval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				o.pump()
			}
		}
	}()
}

// stopTailing stops the tail loop and waits for it to return.
func (o *fileOutput) stopTailing() {
	o.loopMu.Lock()
	stop, stopped := o.stop, o.stopped
	o.stop, o.stopped = nil, nil
	o.loopMu.Unlock()
	if stop != nil {
		close(stop)
		<-stopped
	}
}

// pause stops tailing and reads what is there, for a handoff: it returns the
// offsets (stdout, stderr) the next reader goes on from. An unterminated last
// line is not emitted: it is read again from those offsets.
func (o *fileOutput) pause() [2]int64 {
	o.stopTailing()
	o.pump()
	o.mu.Lock()
	defer o.mu.Unlock()
	return [2]int64{o.streams[0].emitted(), o.streams[1].emitted()}
}

// finish reads the rest once the process has been reaped and emits the
// unterminated last lines: every line is then in memory and goes to the
// journal's .out with the final flush. The files stay until remove, which the
// caller runs after that flush: a crash between the two finds the files, not
// a journal missing the last lines. Only the image that reaped the process
// calls it (waitForFileProcess): one that handed the process off never
// touches its files again.
func (o *fileOutput) finish() {
	if o == nil {
		return
	}
	o.stopTailing()
	o.pump()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	for _, s := range o.streams {
		s.w.flush()
	}
	o.closeLocked()
}

// remove deletes the output files, once finish has emitted their lines and the
// journal has them.
func (o *fileOutput) remove() {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, path := range o.paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			logging.Debug("Background process output file not removed", "path", path, "error", err)
		}
	}
}

func (o *fileOutput) closeLocked() {
	o.closed = true
	for _, s := range o.streams {
		if s != nil {
			_ = s.f.Close()
		}
	}
}

// discard closes and removes the files of a process that never started.
func (o *fileOutput) discard() {
	o.discardKeepFiles()
	for _, path := range o.paths {
		_ = os.Remove(path)
	}
}

// discardKeepFiles closes the daemon's side and leaves the files.
func (o *fileOutput) discardKeepFiles() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closeLocked()
}

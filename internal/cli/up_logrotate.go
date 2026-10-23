package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dev-log rotation for `forge env up`.
//
// The up tee writes every host process's output to a stable, well-known path
// (`.forge/logs/<env>/<svc>.log`) that every developer, agent and doc greps.
// That file is truncated on each `up`, but a single long-running stack can grow
// it without bound. rotatingLogWriter caps the CURRENT file: once it passes the
// cap it is renamed aside and a fresh file opens at the same path, so the grep
// target never moves.
//
// The rotated name is deliberately shaped to match the `rotatedLog` regexp in
// internal/storage/logs.go (`<stream>.<YYYY-MM-DD>T<rest>.log`) so that
// `forge storage gc` expires it under the existing log policy — keep the newest
// 5 per stream, drop anything older than 7 days or over the log budget. Change
// the shape here and rotated files become immortal.

const (
	// defaultLogRotateBytes is the size at which the current stream rotates.
	defaultLogRotateBytes int64 = 50 << 20 // 50 MiB

	// logRotateEnvVar overrides defaultLogRotateBytes. 0 disables rotation
	// (the pre-rotation behaviour: one unbounded file per stream).
	logRotateEnvVar = "FORGE_LOG_ROTATE_BYTES"

	// maxBufferedPartialLine bounds the partial-line buffer. Rotation only
	// happens on line boundaries, so a child that emits megabytes with no
	// newline would otherwise pin unbounded memory and defeat the cap. Past
	// this much un-terminated output we treat the buffer as a line and flush.
	maxBufferedPartialLine = 1 << 20 // 1 MiB
)

// upLogRotateBytes resolves the rotation cap once, at up time, so a run can be
// tuned (and tests can use a tiny cap) without a rebuild. A malformed or
// negative value falls back to the default rather than failing the up: losing
// rotation is survivable, failing to start the stack is not.
func upLogRotateBytes() int64 {
	raw, ok := os.LookupEnv(logRotateEnvVar)
	if !ok {
		return defaultLogRotateBytes
	}
	raw = strings.TrimSpace(raw)
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		fmt.Printf("[up] warning: ignoring %s=%q (want a non-negative byte count)\n", logRotateEnvVar, raw)
		return defaultLogRotateBytes
	}
	return n
}

// rotatingLogWriter writes to path, rotating the file aside once it passes
// capBytes. It is NOT safe for concurrent use on its own; the up tee wraps it
// in a lockedWriter, which is what serialises the stdout and stderr goroutines.
type rotatingLogWriter struct {
	path     string
	capBytes int64

	file *os.File
	size int64

	// partial holds bytes after the last newline in the input seen so far.
	// They are written through to the file (the live stream must not lag),
	// but they hold rotation back until the line completes.
	partial []byte

	// now is swappable so tests get deterministic rotated names.
	now func() time.Time
}

// newRotatingLogWriter truncates (or creates) path and returns a writer that
// rotates past capBytes. capBytes <= 0 disables rotation. The truncate matches
// the os.Create the up tee used before rotation existed: each `up` starts the
// current stream fresh.
func newRotatingLogWriter(path string, capBytes int64) (*rotatingLogWriter, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &rotatingLogWriter{path: path, capBytes: capBytes, file: file, now: time.Now}, nil
}

// Write appends p, rotating only at line boundaries so no log line is ever
// split across two files.
func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	if _, err := w.file.Write(p); err != nil {
		return 0, err
	}
	w.size += int64(len(p))

	// Track whether the stream currently sits on a line boundary. Everything
	// up to the last newline is complete; the tail is a partial line.
	if idx := bytes.LastIndexByte(p, '\n'); idx >= 0 {
		w.partial = append(w.partial[:0], p[idx+1:]...)
	} else {
		w.partial = append(w.partial, p...)
	}

	atLineBoundary := len(w.partial) == 0 || len(w.partial) >= maxBufferedPartialLine
	if w.capBytes > 0 && w.size >= w.capBytes && atLineBoundary {
		if err := w.rotate(); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// rotate closes the current file, renames it to a timestamped sibling, and
// opens a fresh file at the stable path.
func (w *rotatingLogWriter) rotate() error {
	if err := w.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(w.path, w.rotatedPath()); err != nil {
		// Reopen the stream rather than leaving the writer dead: a failed
		// rotation must not silence the log for the rest of the run.
		if file, oerr := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644); oerr == nil {
			w.file = file
		}
		return err
	}
	file, err := os.Create(w.path)
	if err != nil {
		return err
	}
	w.file = file
	w.size = 0
	w.partial = w.partial[:0]
	return nil
}

// rotatedPath builds `<stream>.<UTC timestamp>.log` next to the current file.
// Colons are illegal-ish on some filesystems and awkward in shell globs, so the
// time portion uses dashes; storage's rotatedLog regexp requires only the
// `<YYYY-MM-DD>T` prefix and accepts the rest.
func (w *rotatingLogWriter) rotatedPath() string {
	dir := filepath.Dir(w.path)
	stream := strings.TrimSuffix(filepath.Base(w.path), ".log")
	stamp := w.now().UTC().Format("2006-01-02T15-04-05.000Z")
	candidate := filepath.Join(dir, stream+"."+stamp+".log")
	// Two rotations inside the same millisecond would collide and the rename
	// would silently destroy the older file.
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s.%s-%d.log", stream, stamp, i))
	}
}

// Close closes the current file. Rotated files are already closed.
func (w *rotatingLogWriter) Close() error {
	return w.file.Close()
}

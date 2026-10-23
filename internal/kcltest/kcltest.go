// Package kcltest runs the external `kcl` binary for tests that need to
// assert against a REAL render (as opposed to internal/kclrender, which is
// the embedded kcl-go seam production uses).
//
// It exists because one bug keeps coming back. When two `kcl` processes
// contend for the shared package cache — which a parallel `go test ./...`
// makes routine — kcl prints
//
//	waiting for package-cache lock...
//
// as a plain line on STDOUT, ahead of the JSON document. json.Unmarshal then
// fails with `invalid character 'w' looking for beginning of value`, and a
// perfectly green invariant is reported as a broken one. The failure appears
// only under concurrency and vanishes on a re-run in isolation, which is the
// most expensive kind to debug.
//
// internal/templates fixed this locally; internal/codegen then hit the exact
// same failure because the fix lived in another package's test file and was
// invisible from there. Duplication is what let it recur, so the helper lives
// in one importable place now.
package kcltest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// Run executes `kcl <args...>` in dir and returns stdout with kcl's non-JSON
// preamble stripped.
//
// Stderr is kept OUT of the returned bytes and folded into the error instead:
// the caller unmarshals the result, so anything that is not the document is
// contamination. That alone is not sufficient — the lock notice goes to
// stdout — hence TrimNoise on the way out.
func Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kcl", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w\nstderr:\n%s", err, stderr.String())
	}
	return TrimNoise(out), err
}

// TrimNoise drops kcl's non-JSON preamble so a document that is valid apart
// from progress chatter parses.
//
// It returns from the first '{' or '[' onward. Output with no JSON at all is
// returned UNTOUCHED rather than emptied, so a caller's error message still
// shows what kcl actually said instead of an unhelpful blank.
func TrimNoise(out []byte) []byte {
	i := bytes.IndexAny(out, "{[")
	if i <= 0 {
		return out
	}
	return out[i:]
}

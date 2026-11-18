// Package kcleval reads values out of a forge project's KCL from Go, so a
// test can assert against what the project DECLARES instead of restating it.
//
// # Why a Go package and not `exec.Command("kcl", ...)`
//
// A forge project's KCL resolves `import forge` from the forge binary, not
// from a kcl.mod dependency or a copy on disk. A plain `kcl run` therefore
// cannot evaluate a project file at all, and the workaround — make forge
// materialize its module into a cache directory, dig the path out, pass
// `-E forge=<dir>` — reconstructs forge's private render setup inside a test.
// It breaks whenever the cache layout, the module name or the plugin namespace
// changes, and it breaks in a consumer's CI with an error about KCL that names
// nothing leading back to the cause. It also needs `kcl` on PATH, which forge
// itself has not needed since it embedded the runtime.
//
// This package calls `forge kcl eval` instead. One consequence is worth being
// explicit about: the values come from the forge binary the project is pinned
// to, which is the same binary `forge env deploy` renders with. A test and a
// deploy therefore cannot disagree about what the KCL says — the property that
// makes such a test worth writing.
//
// # Why it drives the binary rather than linking the evaluator
//
// Linking forge's evaluator into a consumer's test binary would put the KCL
// runtime there too, and a hard dependency from the test binary onto a specific forge
// VERSION through go.mod. That is wrong for the job. A project pins its forge
// in CI with `go install .../cmd/forge@vX.Y.Z`; the value a test reads should
// come from THAT forge, not from whatever version the test binary happened to
// compile against. Driving the binary keeps the pin in one place and keeps the
// test binary light.
//
// # Typical use
//
//	func TestDaemonPlacementCoversEveryEnv(t *testing.T) {
//	    var placement map[string]struct {
//	        ClientID string `json:"client_id"`
//	        Context  string `json:"context"`
//	    }
//	    kcleval.MustSelect(t, "deploy/kcl/lib/daemon_placement.k", "daemon_placement", &placement)
//	    // assert against placement
//	}
package kcleval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// DefaultTimeout bounds one evaluation. A KCL evaluation of a library file is
// well under a second; the bound exists so a test that somehow blocks reports
// a timeout naming this call rather than hanging until the whole package's
// deadline expires, which says nothing about where it stopped.
const DefaultTimeout = 2 * time.Minute

// Options configures an evaluation. The zero value is valid: the project is
// found by walking up from the working directory, the forge binary is found on
// PATH, and DefaultTimeout applies.
type Options struct {
	// ProjectDir is the project root (or any directory inside it — forge
	// walks up to the forge.yaml). Empty means the working directory.
	ProjectDir string
	// Binary is the forge executable to drive. Empty means "forge" on PATH.
	//
	// Set it when a test must pin an exact build. Nothing here guesses a
	// path: a test that silently drove a different forge than CI does would
	// assert against a different module version, which is the whole failure
	// this package exists to prevent.
	Binary string
	// Options are `key=value` KCL option bindings, passed as -D.
	Options []string
	// Timeout bounds the evaluation. Zero means DefaultTimeout.
	Timeout time.Duration
}

// ErrForgeNotFound is returned when no forge binary can be run. It is a
// distinct error so a test can choose to skip on it — a developer laptop
// without forge installed — while CI treats it as the failure it is.
var ErrForgeNotFound = errors.New("no forge binary")

// Select evaluates file and decodes the value at path into out.
//
// file is relative to the project root (or absolute inside it). path is a
// dotted field path, kcl-style, where a numeric segment indexes a list. out is
// any encoding/json target: a *string and a *int work for scalars, and a
// struct or map for a composite.
//
// A path the document does not have is an error, never a zero value. That
// distinction is the point: a selector typo that returned "" would be read as
// a legitimately empty declaration, and a test asserting on it would pass
// while checking nothing.
func Select(ctx context.Context, file, path string, out any, opts Options) error {
	raw, err := run(ctx, file, []string{path}, opts)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s -S %s into %T: %w\nvalue was: %s", file, path, out, err, raw)
	}
	return nil
}

// Eval evaluates file whole and decodes the document into out.
func Eval(ctx context.Context, file string, out any, opts Options) error {
	raw, err := run(ctx, file, nil, opts)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode %s into %T: %w", file, out, err)
	}
	return nil
}

// MustSelect is Select for a test: it fails the test on any error and needs no
// context or options.
//
// It is the form nearly every call site wants, and it exists so that reading a
// declared value is a one-line assertion rather than five lines of plumbing —
// the friction that made restating the value in Go the easier choice, which is
// how a test and a deploy drift apart in the first place.
func MustSelect(t testing.TB, file, path string, out any) {
	t.Helper()
	if err := Select(context.Background(), file, path, out, Options{}); err != nil {
		t.Fatalf("read %s -S %s from the project's KCL: %v", file, path, err)
	}
}

// MustSelectString is MustSelect for a string field, returned rather than
// written through a pointer.
func MustSelectString(t testing.TB, file, path string) string {
	t.Helper()
	var s string
	MustSelect(t, file, path, &s)
	return s
}

// run invokes `forge kcl eval` and returns the JSON on stdout.
//
// stdout carries only the value — `forge kcl eval` guarantees that and sends
// every diagnostic to stderr — so the bytes go straight to json.Unmarshal with
// no preamble-trimming. That is deliberate: the trimming helpers that
// surrounded direct `kcl` invocations existed because kcl prints a
// package-cache lock notice on STDOUT, which turns a green assertion into
// "invalid character 'w' looking for beginning of value" under a parallel
// test run.
func run(ctx context.Context, file string, selectors []string, opts Options) ([]byte, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "forge"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not on PATH; install it with "+
			"`go install github.com/reliant-labs/forge/cmd/forge@<version>` "+
			"(pin the version your project's CI pins)", ErrForgeNotFound, bin)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"kcl", "eval", file, "--format", "json"}
	for _, s := range selectors {
		args = append(args, "-S", s)
	}
	for _, d := range opts.Options {
		args = append(args, "-D", d)
	}
	if opts.ProjectDir != "" {
		args = append(args, "-C", opts.ProjectDir)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// forge's own message names the file, the selector and the fields the
		// document does have. Surface it verbatim — rewriting it here would
		// lose the part that says how to fix the call.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("`forge kcl eval %s` timed out after %s: %s", file, timeout, msg)
		}
		return nil, fmt.Errorf("`forge kcl eval %s`: %s", file, msg)
	}
	return stdout.Bytes(), nil
}

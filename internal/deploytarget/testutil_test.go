package deploytarget

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what got written. Used by the dry-run tests to assert the provider
// printed "[DRY-RUN]" lines without actually exec'ing anything.
// fmt.Printf in the providers writes to os.Stdout, so the redirect
// catches it directly.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	return <-done
}

// fakeRunner is the shared test double for commandRunner. It records
// each call's name+args and returns either a canned error or a canned
// Output blob keyed by the joined argv prefix. Used by external_test
// and compose_test alike — provider tests live in this package so the
// fake is unexported, and pulling it into one of the provider test
// files would create an artificial dependency between sibling test
// files.
type fakeRunner struct {
	calls []string

	// envCalls captures the per-call env overlay so tests can confirm
	// env_file contents were threaded through to the exec'd process.
	// Indices stay ALIGNED with calls — every recorded call appends an
	// entry, nil for the ones that carry no overlay (Run, Output) — so
	// a test that finds a call at index i can read its env at the same
	// index. Appending only on RunWithEnv would silently skew the two
	// slices apart the moment a provider added an Output call.
	envCalls []map[string]string

	// dirCalls records the dir of each RunInDir call (not index-aligned
	// with calls; only RunInDir appends).
	dirCalls []string

	// runErrs returns canned errors keyed by the joined argv prefix.
	// Empty map = always succeed.
	runErrs map[string]error

	// outputs returns canned combined-output blobs keyed by joined
	// argv prefix. Empty map = empty output.
	outputs map[string]string
}

func (f *fakeRunner) record(name string, args []string) string {
	full := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, full)
	return full
}

func (f *fakeRunner) lookupErr(joined string) error {
	for prefix, err := range f.runErrs {
		if strings.HasPrefix(joined, prefix) {
			return err
		}
	}
	return nil
}

func (f *fakeRunner) lookupOutput(joined string) string {
	// Quiet compose inventory returns IDs, not the tabular readiness fixture.
	if strings.Contains(joined, " ps --all --quiet") {
		return ""
	}
	var best, result string
	for prefix, out := range f.outputs {
		if strings.HasPrefix(joined, prefix) && len(prefix) > len(best) {
			best, result = prefix, out
		}
	}
	return result
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	full := f.record(name, args)
	f.envCalls = append(f.envCalls, nil)
	return f.lookupErr(full)
}

func (f *fakeRunner) RunWithEnv(_ context.Context, env map[string]string, name string, args ...string) error {
	full := f.record(name, args)
	f.envCalls = append(f.envCalls, copyEnvMap(env))
	return f.lookupErr(full)
}

// RunInDir records the call as `name args` (the dir is kept in dirCalls) so
// assertions on the command stay shell-free.
func (f *fakeRunner) RunInDir(_ context.Context, dir string, env map[string]string, name string, args ...string) error {
	full := f.record(name, args)
	f.envCalls = append(f.envCalls, copyEnvMap(env))
	f.dirCalls = append(f.dirCalls, dir)
	return f.lookupErr(full)
}

// Output and OutputWithEnv return the canned blob ALONGSIDE any canned
// error, because that is what the production execRunner does: its
// implementation returns `buf.Bytes(), fmt.Errorf(...)` on a non-zero
// exit, so a caller that inspects output on the error path is exercising
// a real shape.
//
// The double previously returned (nil, err), which made that path
// untestable and, worse, made a test of it pass against a provider that
// ignored the output entirely. K8sClusterProvider.Observe depends on the
// real behaviour: kubectl exits 1 for every failure and distinguishes
// "not found" from "cannot connect" only in the message it writes to
// stderr — which execRunner captures into the same buffer it returns.
func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	full := f.record(name, args)
	f.envCalls = append(f.envCalls, nil)
	return []byte(f.lookupOutput(full)), f.lookupErr(full)
}

func (f *fakeRunner) OutputWithEnv(_ context.Context, env map[string]string, name string, args ...string) ([]byte, error) {
	full := f.record(name, args)
	f.envCalls = append(f.envCalls, copyEnvMap(env))
	return []byte(f.lookupOutput(full)), f.lookupErr(full)
}

// copyEnvMap snapshots a call's env overlay so a test mutating f.envCalls
// does not pun on the caller's live map.
func copyEnvMap(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

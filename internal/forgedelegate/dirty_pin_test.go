package forgedelegate

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// dirtyPin is what `forge project new` stamps when the forge that ran it was
// built from a modified working tree.
const dirtyPin = pin + "+dirty"

// Verifying forge#2034: a project pinned to a +dirty build kept running a
// cached build of the clean commit — `go install …@<pin>+dirty` drops the
// metadata — after the dirty forge was rebuilt with the fix, so the fix "did
// not work". A +dirty pin is neither built nor served from the cache, and the
// failure names the stale entry an older forge left.
func TestPinned_DirtyPinIsNeverBuiltOrServedFromTheCache(t *testing.T) {
	r, calls, _ := newResolver(t)
	stale := filepath.Join(os.Getenv(CacheDirEnv), safeName(dirtyPin), runtime.GOOS+"-"+runtime.GOARCH, exeName())
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(stale, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	res, err := r.Pinned(context.Background(), t.TempDir(), dirtyPin)
	if err == nil {
		t.Fatalf("a +dirty pin resolved to %s", res.Path)
	}
	for _, want := range []string{dirtyPin, "modified working tree", "commit ee3bded463bc", "Not running " + stale, "built "} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q:\n%v", want, err)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("built %v: `go install` of a +dirty pin builds the clean commit", *calls)
	}

	// With nothing cached, the failure still explains itself, and builds nothing.
	r2, calls2, _ := newResolver(t)
	if _, err := r2.Pinned(context.Background(), t.TempDir(), dirtyPin); err == nil || strings.Contains(err.Error(), "Not running") {
		t.Errorf("empty cache: %v", err)
	}
	if len(*calls2) != 0 {
		t.Errorf("built %v", *calls2)
	}
}

// The project's own .tools/bin/forge stands for a +dirty pin only when it
// reports that exact version: a clean build of the pin's commit (what `task
// tools` installs for it) lacks the edits the pin names, though Same, which
// compares commits, calls them one forge.
func TestPinned_DirtyPinTakesOnlyAProjectBinaryReportingItExactly(t *testing.T) {
	r, calls, _ := newResolver(t)
	project := t.TempDir()
	local := filepath.Join(project, ".tools", "bin", exeName())
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(local, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	reported := pin
	r.Version = func(context.Context, string) (string, error) { return reported, nil }
	r.Same = func(want, got string) bool { // commits, as the cli's pinSkew compares them
		return strings.Split(want, "+")[0] == strings.Split(got, "+")[0]
	}

	if res, err := r.Pinned(context.Background(), project, dirtyPin); err == nil {
		t.Fatalf("a clean build of the commit stood for the +dirty pin: %s", res.Path)
	}

	reported = dirtyPin
	res, err := r.Pinned(context.Background(), project, dirtyPin)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != local || len(*calls) != 0 {
		t.Fatalf("got %s after builds %v, want the project's own binary", res.Path, *calls)
	}

	// A clean pin keeps comparing commits: a +dirty build of its commit is it.
	if _, ok := r.LocalPinned(context.Background(), project, pin); !ok {
		t.Error("a clean pin no longer accepts a binary of its commit")
	}
}

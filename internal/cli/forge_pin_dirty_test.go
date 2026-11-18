package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// dirtySkewPin is the pin `forge project new` stamps when run by a forge built
// from a modified working tree.
const dirtySkewPin = skewTestPin + "+dirty"

func dirtyPinnedProject(t *testing.T) string {
	t.Helper()
	skewedProject(t)
	root, _ := os.Getwd()
	writeForgeYAML(t, root, "name: demo\nmodule_path: github.com/example/demo\nforge_version: "+dirtySkewPin+"\n")
	return root
}

// Verifying forge#2034: a project pinned to a +dirty forge build was handed
// to a cached build of the clean commit (which `go install …@<pin>+dirty`
// produces), so a forge rebuilt with a fix ran the old code until
// FORGE_ALLOW_VERSION_SKEW=1. The hand-off now fails instead, naming the
// stale build, and runs nothing.
func TestDelegation_DirtyPinNeverRunsACachedCleanBuild(t *testing.T) {
	dirtyPinnedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)
	stale := filepath.Join(os.Getenv(forgedelegate.CacheDirEnv), strings.ReplaceAll(dirtySkewPin, "+", "_"),
		runtime.GOOS+"-"+runtime.GOARCH, forgeExeName())
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(stale, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := runCheck(t, "generate")
	if err == nil {
		t.Fatal("generate ran although the +dirty pin cannot be obtained")
	}
	for _, want := range []string{dirtySkewPin, "modified working tree", "Not running " + stale, "Nothing ran", "--allow-version-skew"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q:\n%v", want, err)
		}
	}
	if h.path != "" || len(*installs) != 0 {
		t.Errorf("exec'd %q after builds %v", h.path, *installs)
	}

	// The explicit "run THIS forge" still does, and its warning offers no
	// `go run …@<pin>+dirty`, which would build the clean commit too.
	t.Setenv(allowVersionSkewEnv, "1")
	out, err := runCheck(t, "skill", "list")
	if err != nil {
		t.Fatalf("with %s=1: %v", allowVersionSkewEnv, err)
	}
	if h.path != "" || strings.Contains(out, "go run ") || !strings.Contains(out, "modified working tree") {
		t.Errorf("warning under the override:\n%s", out)
	}
}

// A +dirty pin the project's .tools/bin/forge reports exactly is run from
// that file, and its cache key follows the file: rebuilt with new code under
// the same version, it is a new key, never the "pin:" key a host keeps
// output under for good.
func TestResolvePinnedForge_DirtyPinKeyFollowsTheBinary(t *testing.T) {
	root := dirtyPinnedProject(t)
	armDelegation(t)
	local := filepath.Join(root, ".tools", "bin", forgeExeName())
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.WriteFile(local, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	pinDelegateResolver = func(w io.Writer) *forgedelegate.Resolver {
		return &forgedelegate.Resolver{
			Log:     w,
			Version: func(context.Context, string) (string, error) { return dirtySkewPin, nil },
			Same: func(want, got string) bool {
				skewed, ok := pinSkew(want, got, "")
				return ok && !skewed
			},
		}
	}

	first, ok, err := ResolvePinnedForge(context.Background(), root, "skill load", io.Discard)
	if err != nil || !ok || first.Path != local {
		t.Fatalf("got %+v, ok %v, err %v; want %s", first, ok, err, local)
	}
	if strings.HasPrefix(first.Key, "pin:") || !strings.Contains(first.Key, dirtySkewPin) {
		t.Errorf("key %q: a +dirty pin must not be keyed as a module version", first.Key)
	}

	if err := atomicfile.WriteFile(local, []byte("two, rebuilt"), 0o755); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(local, later, later); err != nil {
		t.Fatal(err)
	}
	second, _, err := ResolvePinnedForge(context.Background(), root, "skill load", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if second.Key == first.Key {
		t.Errorf("a rebuilt binary kept the key %q", first.Key)
	}
}

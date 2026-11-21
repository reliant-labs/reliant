package codegen

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/controller-tools/pkg/deepcopy"
	"sigs.k8s.io/controller-tools/pkg/genall"
	"sigs.k8s.io/controller-tools/pkg/loader"
)

// memOutput captures what a controller-tools generator writes, keyed by path.
type memOutput struct{ files map[string]*bytes.Buffer }

type memFile struct{ *bytes.Buffer }

func (memFile) Close() error { return nil }

func (m *memOutput) Open(_ *loader.Package, path string) (io.WriteCloser, error) {
	b := &bytes.Buffer{}
	m.files[path] = b
	return memFile{b}, nil
}

// TestDeployTierDeepCopyIsCurrent regenerates pkg/deploy/v1alpha1's deepcopy
// with controller-tools' own generator — in-process, from the version pinned in
// forge's go.mod, so there is no separately-installed controller-gen whose
// version could differ — and fails if the committed file differs.
//
// Run with FORGE_UPDATE_GENERATED=1 to rewrite the file.
func TestDeployTierDeepCopyIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("loads Go packages (seconds); full mode only")
	}
	root := forgeRepoRoot(t)
	restore, err := chdir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()

	var gen genall.Generator = deepcopy.Generator{}
	rt, err := genall.Generators{&gen}.ForRoots("./pkg/deploy/v1alpha1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := &memOutput{files: map[string]*bytes.Buffer{}}
	rt.OutputRules = genall.OutputRules{Default: out}
	if failed := rt.Run(); failed {
		t.Fatal("deepcopy generation reported errors (see log above)")
	}
	got, ok := out.files["zz_generated.deepcopy.go"]
	if !ok {
		t.Fatalf("generator produced no zz_generated.deepcopy.go (got %d files)", len(out.files))
	}

	path := filepath.Join(root, "pkg", "deploy", "v1alpha1", "zz_generated.deepcopy.go")
	if os.Getenv("FORGE_UPDATE_GENERATED") == "1" {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed deepcopy: %v (run with FORGE_UPDATE_GENERATED=1)", err)
	}
	if !bytes.Equal(want, got.Bytes()) {
		t.Fatalf("%s is stale; run: FORGE_UPDATE_GENERATED=1 go test ./internal/codegen -run TestDeployTierDeepCopyIsCurrent", path)
	}
}

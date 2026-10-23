package codegen

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/tools/go/packages"
	"sigs.k8s.io/controller-tools/pkg/deepcopy"
	"sigs.k8s.io/controller-tools/pkg/genall"
	"sigs.k8s.io/controller-tools/pkg/loader"

	"github.com/reliant-labs/forge/internal/goexec"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// DeepCopyFile is the file controller-tools' object generator owns in each
// API package.
const DeepCopyFile = "zz_generated.deepcopy.go"

// GenerateAPIDeepCopy regenerates zz_generated.deepcopy.go for every package
// under the project's api/ directory, in-process, with the controller-tools
// version pinned in forge's go.mod.
//
// WHY forge OWNS THIS. forge already projects the CRDs (crd_gen.k) from these
// same Go types, but it never regenerated their deepcopy. So deleting a type —
// the normal way to retire a kind — left a deepcopy file referencing it, the
// package stopped compiling, and the only way out was a controller-gen binary
// the project had no pinned version of. One source (the Go types), both
// projections, one command.
//
// THE STALE FILE IS REMOVED BEFORE LOADING, because it is exactly what makes
// the package fail to type-check after a type is deleted. It is restored if
// generation fails, so a failed run never leaves the tree worse than it found
// it. It returns the paths it wrote, relative to projectDir.
func GenerateAPIDeepCopy(projectDir string) ([]string, error) {
	// The loader reports symlink-resolved paths (/var -> /private/var on
	// macOS); resolve ours the same way so the reported paths relativize.
	if resolved, err := filepath.EvalSymlinks(projectDir); err == nil {
		projectDir = resolved
	}
	apiDir := filepath.Join(projectDir, CRDAPIDir)
	if info, err := os.Stat(apiDir); err != nil || !info.IsDir() {
		return nil, nil
	}

	var stale []string
	saved := map[string][]byte{}
	err := filepath.WalkDir(apiDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != DeepCopyFile {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		saved[path] = b
		stale = append(stale, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", apiDir, err)
	}
	restore := func() {
		for p, b := range saved {
			_ = atomicfile.WriteFile(p, b, 0o644)
		}
	}
	for _, p := range stale {
		if err := os.Remove(p); err != nil {
			restore()
			return nil, err
		}
	}

	back, err := chdir(projectDir)
	if err != nil {
		restore()
		return nil, err
	}
	defer back()

	var gen genall.Generator = deepcopy.Generator{}
	// WithConfig only to scrub the go list env: ForRoots' own empty config
	// would hand go/packages the caller's GOFLAGS=-mod=mod (goexec.Env).
	rt, err := genall.Generators{&gen}.ForRootsWithConfig(&packages.Config{Env: goexec.Env()}, "./"+CRDAPIDir+"/...")
	if err != nil {
		restore()
		return nil, fmt.Errorf("load api packages: %w", err)
	}
	out := &dirOutput{files: map[string]*bytes.Buffer{}}
	rt.OutputRules = genall.OutputRules{Default: out}
	if failed := rt.Run(); failed {
		restore()
		return nil, fmt.Errorf("deepcopy generation for %s reported errors (see above)", apiDir)
	}

	var written []string
	for path, buf := range out.files {
		if err := atomicfile.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			restore()
			return nil, err
		}
		rel, _ := filepath.Rel(projectDir, path)
		written = append(written, rel)
	}
	sort.Strings(written)
	return written, nil
}

// dirOutput buffers each generated file at its package directory, so nothing
// is written until the whole run has succeeded.
type dirOutput struct{ files map[string]*bytes.Buffer }

type bufFile struct{ *bytes.Buffer }

func (bufFile) Close() error { return nil }

func (o *dirOutput) Open(pkg *loader.Package, name string) (io.WriteCloser, error) {
	if pkg == nil || len(pkg.GoFiles) == 0 {
		return nil, fmt.Errorf("deepcopy output for %s has no package directory", name)
	}
	b := &bytes.Buffer{}
	o.files[filepath.Join(filepath.Dir(pkg.GoFiles[0]), name)] = b
	return bufFile{b}, nil
}

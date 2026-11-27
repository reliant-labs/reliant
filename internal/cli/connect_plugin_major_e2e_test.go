//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EV1ScaffoldIgnoresV2ConnectPluginOnPath: a connect-go v1 project
// scaffolds and builds with connect-go v2's protoc-gen-connect-go first on
// PATH. Both majors name the binary protoc-gen-connect-go, and v2's emits
// Register<Svc>Handler where v1's handlers call New<Svc>Handler.
//
// The e2e suite hit exactly this: `forge tools install` in forge's own
// checkout (whose go.mod requires both majors directly) put v2's plugin on
// every shard's PATH, addCorpusForgePkgReplace leaves the project's go.mod
// needing a tidy so `go list -m` cannot name the pin, and generate fell back
// to PATH's plugin: `undefined: itemv1connect.NewItemServiceHandler` in about
// a hundred tests. Building v2's plugin here makes the case explicit instead
// of depending on what the runner's PATH holds.
func TestE2EV1ScaffoldIgnoresV2ConnectPluginOnPath(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)

	// connect-go v2's plugin, built from forge's own module graph (its go.mod
	// requires connectrpc.com/connect/v2 directly).
	v2PluginDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(v2PluginDir, "protoc-gen-connect-go"+exeSuffix()),
		connectmajor.V2.PluginModule())
	build.Dir = findRepoRoot(t)
	build.Env = goexec.Env("GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build connect-go v2's protoc-gen-connect-go: %v\n%s", err, out)
	}

	dir := t.TempDir()
	// --connect v1 explicitly: fresh projects default to v2, and this test is
	// about a v1 project.
	runCmd(t, dir, forgeBin, "project", "new", "majorapp", "--mod", "example.com/majorapp", "--connect", "v1")
	projectDir := filepath.Join(dir, "majorapp")
	addCorpusForgePkgReplace(t, projectDir)
	if m, err := connectmajor.Detect(projectDir); err != nil || m != connectmajor.V1 {
		t.Fatalf("precondition: the fresh project must be connect-go v1, got %s (%v)", m, err)
	}

	// The PATH every forge child below sees: v2's plugin first.
	path := v2PluginDir + string(os.PathListSeparator) + os.Getenv("PATH")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = projectDir
		cmd.Env = goexec.WithTrimpath(append(os.Environ(),
			"PATH="+path,
			"GOFLAGS=",
			"GONOSUMCHECK=*",
			"GOPROXY=https://proxy.golang.org,direct",
			"GONOSUMDB=*",
		))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("command %q failed with connect-go v2's plugin first on PATH: %v\n%s", args, err, out)
		}
	}
	run(forgeBin, "scaffold", "service", "item")
	run(forgeBin, "generate")

	stub := readFileE2E(t, filepath.Join(projectDir, "gen", "services", "item", "v1", "itemv1connect", "item.connect.go"))
	if !strings.Contains(stub, "func NewItemServiceHandler(") || strings.Contains(stub, connectmajor.ModuleV2) {
		t.Fatalf("generate ran connect-go v2's plugin for a v1 project; the stub is:\n%s", stub)
	}
	run("go", "build", "./...")
}

//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EGenerateVendorsForgeProtoOnDemand pins, on a scaffolded project,
// that `forge generate` supplies forge/v1/forge.proto to a project that
// imports it but has no copy — the shape of a project that adopted forge in
// place and whose only forge-annotated protos are config protos, where buf
// failed on the import until the file was copied in by hand — and that a
// project's own Go buf template targeting proto/ wholesale is refused before
// it generates a second Go registration of the descriptor forgepb owns.
func TestE2EGenerateVendorsForgeProtoOnDemand(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "vendorapp", "--mod", "example.com/vendorapp", "--service", "item")
	projectDir := filepath.Join(dir, "vendorapp")
	addCorpusForgePkgReplace(t, projectDir)

	vendored := filepath.Join(projectDir, "proto", "forge", "v1", "forge.proto")
	if err := os.RemoveAll(filepath.Join(projectDir, "proto", "forge")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFileE2E(t, filepath.Join(projectDir, "proto", "vendorapp", "config", "v1", "config.proto")), `import "forge/v1/forge.proto";`) {
		t.Fatal("the scaffolded config proto no longer imports forge/v1/forge.proto; this test's premise is gone")
	}

	runCmd(t, projectDir, forgeBin, "generate")
	if got := readFileE2E(t, vendored); !strings.Contains(got, `option go_package = "github.com/reliant-labs/forge/pkg/forgepb;forgepb";`) {
		t.Fatalf("generate did not vendor forge.proto with the forgepb go_package:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "gen", "forge")); err == nil {
		t.Error("generate wrote Go for the vendored forge.proto (gen/forge): forgepb already registers it")
	}
	runCmd(t, projectDir, "go", "build", "./...")
	// The vendored copy is forge's own bytes, so the drift guard is clean.
	runCmd(t, projectDir, forgeBin, "lint", "--vendored-protos")

	// A Go template of the project's own with no inputs targets the whole
	// workspace, vendored copy included.
	writeFileE2E(t, filepath.Join(projectDir, "buf.gen.yaml"), `version: v2
plugins:
  - local: protoc-gen-go
    out: gen
    opt: [paths=source_relative]
  - local: protoc-gen-connect-go
    out: gen
    opt: [paths=source_relative]
`)
	cmd := exec.Command(forgeBin, "generate")
	cmd.Dir = projectDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("generate ran a Go template that compiles the vendored forge.proto:\n%s", out)
	}
	for _, want := range []string{"buf template pre-flight", "proto/forge/v1/forge.proto", "exclude_paths:"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("refusal missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(projectDir, "gen", "forge")); err == nil {
		t.Error("the refused template still wrote gen/forge")
	}
}

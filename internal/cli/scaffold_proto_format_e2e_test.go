//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EScaffoldedProtosPassBufFormat is the evaluator's first commit after
// `forge scaffold`: `forge lint` said every gating linter passed, then the
// scaffolded pre-commit hook (`buf format -d --exit-code`) refused the commit
// over text forge had just written into the service proto — an import out of
// order, one-field `(forge.v1.method)` literals spread over three lines, and
// the entity's validate rules folded onto one line in the born Create
// request.
//
// So: a fresh project, an entity carrying the rule shapes forge copies (a
// multi-option list and a braced aggregate), `forge scaffold`, and then the
// hook's own command must pass with the binary on PATH — and `forge lint
// --proto-format`, the lint lane that runs that command, must agree with it
// both ways.
func TestE2EScaffoldedProtosPassBufFormat(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "buf")
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "fmtapp", "--mod", "example.com/fmtapp", "--service", "counters")
	projectDir := filepath.Join(dir, "fmtapp")
	addCorpusForgePkgReplace(t, projectDir)
	bufFormatCheck := func(stage string) {
		t.Helper()
		cmd := exec.Command("buf", "format", "-d", "--exit-code")
		cmd.Dir = projectDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: the pre-commit hook's `buf format -d --exit-code` refuses the tree (%v):\n%s", stage, err, out)
		}
	}
	bufFormatCheck("after project new")

	protoPath := filepath.Join(projectDir, "proto", "services", "counters", "v1", "counters.proto")
	proto := readFileE2E(t, protoPath)
	proto = strings.Replace(proto, `import "forge/v1/forge.proto";`,
		"import \"buf/validate/validate.proto\";\nimport \"forge/v1/forge.proto\";\nimport \"google/protobuf/timestamp.proto\";", 1)
	proto += "\n// forge:entity\n" +
		"message Counter {\n" +
		"  string id = 1;\n" +
		"  string name = 2 [\n" +
		"    (buf.validate.field).string.min_len = 1,\n" +
		"    (buf.validate.field).string.max_len = 64\n" +
		"  ];\n" +
		"  int64 value = 3 [(buf.validate.field).int64 = {\n" +
		"    gte: 0\n" +
		"    lte: 1000\n" +
		"  }];\n" +
		"  google.protobuf.Timestamp created_at = 4;\n" +
		"  google.protobuf.Timestamp updated_at = 5;\n" +
		"}\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	bufFormatCheck("the authored entity (the test's own input)")

	runCmd(t, projectDir, forgeBin, "scaffold")
	if got := readFileE2E(t, protoPath); !strings.Contains(got, "message CreateCounterRequest") {
		t.Fatalf("the CRUD quintet was not injected:\n%s", got)
	}
	bufFormatCheck("after forge scaffold")
	runCmd(t, projectDir, forgeBin, "lint", "--proto-format")

	// The lane reports where the hook refuses, and --fix clears it. (Whether
	// the finding fails the run depends on the project's pin: the rule warns
	// for one release before it gates — lint_rollout_table.go.)
	if err := os.WriteFile(protoPath, []byte(readFileE2E(t, protoPath)+"message   Stray {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lint := exec.Command(forgeBin, "lint", "--proto-format")
	lint.Dir = projectDir
	if out, _ := lint.CombinedOutput(); !strings.Contains(string(out), "forge-proto-format") {
		t.Fatalf("forge lint --proto-format did not report a proto buf format rewrites:\n%s", out)
	}
	runCmd(t, projectDir, forgeBin, "lint", "--proto-format", "--fix")
	bufFormatCheck("after forge lint --proto-format --fix")
}

//go:build e2e

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EGenerateStepsHarness pins what makes `forge generate --steps
// harness` safe to recommend after a Deps edit: it regenerates the edited
// service's helpers_gen_test.go to exactly the bytes a full `forge
// generate` would write, and writes nothing else.
//
//  1. A Deps field typed with a new local interface gets its Stub<Field>
//     double and its default install, and the handler package's tests
//     compile against it.
//  2. No other file in the project changes (forge's own .forge/ state
//     aside).
//  3. A full generate afterwards leaves the harness byte-identical — the
//     preset is the full run, narrowed, not a second renderer that can
//     drift from it.
func TestE2EGenerateStepsHarness(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "harnessapp", "--mod", "example.com/harnessapp", "--service", "widget")
	projectDir := filepath.Join(dir, "harnessapp")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")

	widgetDir := filepath.Join(projectDir, "internal", "handlers", "widget")
	servicePath := filepath.Join(widgetDir, "service.go")
	src := readFileE2E(t, servicePath)
	const depsOpen = "type Deps struct {\n"
	if !strings.Contains(src, depsOpen) {
		t.Fatalf("service.go has no %q — fixture is stale:\n%s", depsOpen, src)
	}
	src = strings.Replace(src, depsOpen, depsOpen+"\tPinger Pinger\n", 1) +
		"\n// Pinger reaches the upstream the widget service checks.\ntype Pinger interface {\n\tPing() error\n}\n"
	if err := os.WriteFile(servicePath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	before := treeDigestE2E(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate", "--steps", "harness")
	after := treeDigestE2E(t, projectDir)

	// ── (1) the harness follows the Deps edit ────────────────────────────
	helpersRel := "internal/handlers/widget/helpers_gen_test.go"
	harness := readFileE2E(t, filepath.Join(projectDir, helpersRel))
	for _, want := range []string{"type StubPinger struct", "deps.Pinger = &StubPinger{}"} {
		if !strings.Contains(harness, want) {
			t.Errorf("%s after --steps harness lacks %q:\n%s", helpersRel, want, harness)
		}
	}
	runCmd(t, projectDir, "go", "vet", "./internal/handlers/widget/")

	// ── (2) and nothing else moves ───────────────────────────────────────
	for rel, sum := range after {
		if rel == helpersRel {
			continue
		}
		if before[rel] != sum {
			t.Errorf("--steps harness changed %s; it must write only the test harness", rel)
		}
	}
	for rel := range before {
		if _, ok := after[rel]; !ok {
			t.Errorf("--steps harness removed %s", rel)
		}
	}

	// ── (3) a full run agrees ─────────────────────────────────────────────
	runCmd(t, projectDir, forgeBin, "generate")
	if full := readFileE2E(t, filepath.Join(projectDir, helpersRel)); full != harness {
		t.Errorf("a full generate rewrote the harness --steps harness wrote, so the preset is not the full run narrowed.\n"+
			"--steps harness:\n%s\nfull generate:\n%s", harness, full)
	}
}

// treeDigestE2E maps every project file outside forge's own state, the
// vendored forge module and VCS metadata to its content hash.
func treeDigestE2E(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case ".forge", ".forge-pkg", ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

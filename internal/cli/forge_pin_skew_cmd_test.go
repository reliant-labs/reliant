package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
)

// 2026-10-07: ~/go/bin/forge was 3b4499b3 while every repo pinned d6b5d722,
// and nothing said so. A build/release/deploy command under a forge that is
// not the pinned one is refused, naming both.
func TestForgePinSkew_DeployUnderAnotherForgeIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\n"+
		"forge_version: v0.1.44-0.20261007082641-d6b5d722b839\n")
	t.Chdir(dir)
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set("v0.1.44-0.20261007053821-3b4499b384de", "", "3b4499b384de")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })

	root := newTestRootCmd(t)
	root.SetArgs([]string{"env", "deploy", "prod", "--explain"})
	err := root.Execute()
	if err == nil {
		t.Fatal("a deploy under a forge other than the pinned one ran")
	}
	for _, want := range []string{"v0.1.44-0.20261007053821-3b4499b384de", "forge_version v0.1.44-0.20261007082641-d6b5d722b839"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n%v", want, err)
		}
	}
}

const (
	skewTestPin    = "v0.1.45-0.20261012195649-377b61532c0e"
	skewTestBinary = "v0.1.45-0.20261011192000-6c0c3edb1322"
)

func skewedProject(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: demo\nmodule_path: github.com/example/demo\nforge_version: "+skewTestPin+"\n")
	t.Chdir(dir)
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set(skewTestBinary, "", "6c0c3edb1322")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
}

// guardVerdict runs the root PersistentPreRun check for args without running
// the command itself.
func guardVerdict(t *testing.T, args ...string) error {
	t.Helper()
	root := newTestRootCmd(t)
	cmd, rest, err := root.Find(args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatalf("parse flags %v: %v", args, err)
	}
	allow, _ := cmd.Flags().GetBool(allowVersionSkewFlag)
	var warn strings.Builder
	return checkForgePinSkew(cmd, root, &warn, allow)
}

// 2026-10-12: two agents ran `forge generate` under a stale forge on PATH; it
// silently rewrote unrelated generated files and broke typecheck. Every
// project-mutating command is refused, with a runbook naming both versions
// and the literal fix.
func TestForgePinSkew_MutatingCommandsAreRefusedWithRunbook(t *testing.T) {
	skewedProject(t)
	for _, args := range [][]string{
		{"generate"},
		{"scaffold"},
		{"scaffold", "service", "billing"},
		{"scaffold", "rpc", "billing", "Refund"},
		{"package", "new", "ledger"},
		{"component", "install", "button"},
		{"env", "new", "qa"},
		{"project", "rescaffold"},
		{"project", "delete", "service", "billing"},
		{"project", "migrate", "tdd"},
	} {
		err := guardVerdict(t, args...)
		if err == nil {
			t.Errorf("forge %s ran under a forge other than the pinned one", strings.Join(args, " "))
			continue
		}
		for _, want := range []string{
			skewTestBinary, skewTestPin,
			"go run github.com/reliant-labs/forge/cmd/forge@" + skewTestPin,
			"GOPRIVATE='github.com/reliant-labs/*' go install github.com/reliant-labs/forge/cmd/forge@" + skewTestPin,
			"--allow-version-skew", "FORGE_ALLOW_VERSION_SKEW=1",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("forge %s: refusal does not contain %q:\n%v", strings.Join(args, " "), want, err)
			}
		}
	}
}

// Reading is never blocked: help, plans, checks and every inspection verb.
func TestForgePinSkew_ReadOnlyCommandsAreNotRefused(t *testing.T) {
	skewedProject(t)
	for _, args := range [][]string{
		{"generate", "--dry-run"},
		{"generate", "--check"},
		{"scaffold", "--dry-run"},
		{"project", "migrate", "tdd", "--dry-run"},
		{"env", "new", "qa", "--check"},
		{"project", "delete", "service", "billing", "--dry-run"},
		{"lint"},
		{"lint", "--no-fix"},
		{"skill", "list"},
		{"project", "audit"},
		{"project", "shapes"},
		{"project", "capabilities"},
		{"project", "upgrade", "list"},
		{"env", "status", "dev"},
		{"env", "list"},
		{"version"},
	} {
		if err := guardVerdict(t, args...); err != nil {
			t.Errorf("forge %s was refused but writes nothing:\n%v", strings.Join(args, " "), err)
		}
	}
}

func TestForgePinSkew_EscapeHatches(t *testing.T) {
	skewedProject(t)
	t.Run("flag", func(t *testing.T) {
		if err := guardVerdict(t, "generate", "--allow-version-skew"); err != nil {
			t.Errorf("--allow-version-skew did not lift the refusal: %v", err)
		}
	})
	t.Run("env", func(t *testing.T) {
		t.Setenv(allowVersionSkewEnv, "1")
		if err := guardVerdict(t, "generate"); err != nil {
			t.Errorf("%s=1 did not lift the refusal: %v", allowVersionSkewEnv, err)
		}
	})
	t.Run("matching pin", func(t *testing.T) {
		buildinfo.Set(skewTestPin, "", "377b61532c0e")
		if err := guardVerdict(t, "generate"); err != nil {
			t.Errorf("generate refused under the pinned forge: %v", err)
		}
	})
	t.Run("dev build against a published-tag pin", func(t *testing.T) {
		// forge's own e2e lanes and every forge developer: a binary built from
		// the checkout generating into a project on the published floor.
		writeForgeYAML(t, ".", "name: demo\nmodule_path: github.com/example/demo\nforge_version: v0.1.44\n")
		buildinfo.Set("v0.0.0-20261013000226-65c48bf95a17+dirty", "", "65c48bf95a17")
		if err := guardVerdict(t, "generate"); err != nil {
			t.Errorf("a dev build was refused against a published-tag pin: %v", err)
		}
		if err := guardVerdict(t, "env", "deploy", "prod"); err == nil {
			t.Error("a deploy under a dev build against a tag pin must stay refused")
		}
	})
	t.Run("dev build naming no forge", func(t *testing.T) {
		buildinfo.Set("(devel)", "", "")
		if err := guardVerdict(t, "generate"); err != nil {
			t.Errorf("a (devel) build with no commit was refused: %v", err)
		}
	})
}

// `forge env up` / `forge build` regenerate stale code on the user's behalf;
// that path rewrites the tree exactly as `forge generate` does and needs the
// same guard.
func TestForgePinSkew_AutoGenerateIsRefused(t *testing.T) {
	skewedProject(t)
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// A go.work listing a module with no go.mod is the "generated code is
	// missing" trigger for ensureGeneratedCode.
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("go 1.22\n\nuse ./gen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = ensureGeneratedCode(root, false)
	if err == nil || !strings.Contains(err.Error(), skewTestPin) || !strings.Contains(err.Error(), "FORGE_ALLOW_VERSION_SKEW=1") {
		t.Fatalf("automatic generate under another forge was not refused with the runbook: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "gen", "go.mod")); statErr == nil {
		t.Error("the refused generate still wrote gen/go.mod")
	}
	if err := ensureGeneratedCode(root, true); err != nil {
		t.Errorf("--no-generate must skip the pipeline and the guard: %v", err)
	}
}

// A command that is not refused still warns, and the warning gives the
// refusal's remedy. It used to say only "go install forge@<pin>", which does
// nothing for an embedded forge: `reliant forge` kept running the forge
// compiled into reliant.
func TestForgePinSkew_WarningGivesTheRefusalsRemedy(t *testing.T) {
	skewedProject(t)
	root := newTestRootCmd(t)
	cmd, _, err := root.Find([]string{"lint"})
	if err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	if err := checkForgePinSkew(cmd, root, &warn, false); err != nil {
		t.Fatalf("lint was refused: %v", err)
	}
	if want := "go run github.com/reliant-labs/forge/cmd/forge@" + skewTestPin; !strings.Contains(warn.String(), want) {
		t.Errorf("the warning does not give the go run form %q:\n%s", want, warn.String())
	}

	embedded := pinSkewWarning(".", skewTestPin, skewTestBinary,
		buildinfo.Build{Embedded: true, Embedder: "github.com/reliant-labs/reliant"})
	for _, want := range []string{
		"go run github.com/reliant-labs/forge/cmd/forge@" + skewTestPin,
		"embedded in reliant",
		"upgrade reliant",
	} {
		if !strings.Contains(embedded, want) {
			t.Errorf("the embedded warning does not contain %q:\n%s", want, embedded)
		}
	}
	if strings.Contains(embedded, "go install") {
		t.Errorf("the embedded warning still advises go install, which does not change the embedded forge:\n%s", embedded)
	}
}

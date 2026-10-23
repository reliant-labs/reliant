//go:build e2e

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
)

// TestE2EVerifyGeneratedNeverWritesTheCheckout runs the real
// `forge ci verify-generated` on a scaffolded project. It used to regenerate
// the checkout in place and read `git status`, so a concurrent gate had to
// hold every reader of the tree back for it, and a run that found drift (or
// was killed) left the regenerated files in the tree. Pinned here:
//  1. an up-to-date tree passes, and the checkout is byte-identical after;
//  2. the run records the tree's fixed point in the checkout's generate
//     cache, so the next run skips the regenerate's steps;
//  3. a stale tree fails with the changed generated files listed, and the
//     checkout is still byte-identical after.
func TestE2EVerifyGeneratedNeverWritesTheCheckout(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "vgapp", "--mod", "example.com/vgapp", "--service", "widget")
	projectDir := filepath.Join(dir, "vgapp")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")
	commitAllE2E(t, projectDir, "generated")

	verify := func() (string, error) {
		cmd := exec.Command(forgeBin, "ci", "verify-generated")
		cmd.Dir = projectDir
		cmd.Env = goexec.WithTrimpath(append(os.Environ(), "GOFLAGS=", "GONOSUMCHECK=*", "GONOSUMDB=*"))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// (1) Up to date: passes, writes nothing.
	before := checkoutHashE2E(t, projectDir)
	out, err := verify()
	if err != nil {
		t.Fatalf("verify-generated failed on a freshly generated tree: %v\n%s", err, out)
	}
	if after := checkoutHashE2E(t, projectDir); after != before {
		t.Fatalf("verify-generated wrote the checkout on a passing run:\n%s", out)
	}

	// (2) The fixed point it verified is the checkout's too: the next run
	// skips every step.
	if out, err = verify(); err != nil || !strings.Contains(out, "nothing to do") {
		t.Errorf("a second run over the same tree did not reuse the generate cache (err=%v):\n%s", err, out)
	}

	// (3) Stale: a proto change committed without regenerating.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\nmessage WidgetNote { string text = 1; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAllE2E(t, projectDir, "stale proto")
	before = checkoutHashE2E(t, projectDir)
	out, err = verify()
	if err == nil {
		t.Fatalf("verify-generated passed a proto change that was never regenerated:\n%s", out)
	}
	if !strings.Contains(out, "generated code is out of date") || !strings.Contains(out, ".pb.go") {
		t.Errorf("drift report does not name the stale generated files:\n%s", out)
	}
	if after := checkoutHashE2E(t, projectDir); after != before {
		t.Errorf("verify-generated left its regenerate in the checkout:\n%s", out)
	}
	if status := runCmdOutput(t, projectDir, "git", "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the checkout is dirty after verify-generated:\n%s", status)
	}
}

// commitAllE2E commits every change in dir.
func commitAllE2E(t *testing.T, dir, msg string) {
	t.Helper()
	runCmd(t, dir, "git", "add", "-A")
	runCmd(t, dir, "git", "-c", "user.name=forge-test", "-c", "user.email=forge-test@example.com",
		"-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "--allow-empty", "-m", msg)
}

// checkoutHashE2E hashes what verify-generated must never write: every file
// git shows (tracked, plus untracked-not-ignored), and the clone's index and
// config. The generate cache's manifest is gitignored state and may change.
func checkoutHashE2E(t *testing.T, dir string) string {
	t.Helper()
	listing := runCmdOutput(t, dir, "git", "--no-optional-locks", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	h := sha256.New()
	for _, rel := range append(strings.Split(listing, "\x00"), ".git/index", ".git/config") {
		if rel == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		h.Write([]byte(rel + "\x00"))
		h.Write(body)
		h.Write([]byte("\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

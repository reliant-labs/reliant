package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/forgecompat"
)

func TestBridgeSkewChecked_GenerateAndLintOnly(t *testing.T) {
	root := newTestRootCmd(t)
	for name, want := range map[string]bool{"generate": true, "lint": true, "doctor": false, "env": false} {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("find %q: %v", name, err)
		}
		if got := bridgeSkewChecked(cmd, root); got != want {
			t.Errorf("bridgeSkewChecked(%s) = %v, want %v", name, got, want)
		}
	}
	// A subcommand that happens to share the name is not the top-level one.
	envDown, _, err := root.Find([]string{"env", "down"})
	if err != nil {
		t.Fatal(err)
	}
	if bridgeSkewChecked(envDown, root) {
		t.Error("only top-level generate/lint carry the skew warning")
	}
}

func TestLinkForgeRequested(t *testing.T) {
	t.Setenv(linkForgeEnv, "")
	if linkForgeRequested(false) {
		t.Error("no flag, no env: the bridge must not be written")
	}
	if !linkForgeRequested(true) {
		t.Error("--link-forge must opt in")
	}
	t.Setenv(linkForgeEnv, "1")
	if !linkForgeRequested(false) {
		t.Errorf("%s=1 must opt in", linkForgeEnv)
	}
}

func TestBridgeCheckResult(t *testing.T) {
	head := "750833054b78ab216150bdaad19c389cb91cbc53"
	bridge := forgecompat.Bridge{Dir: "/src/forge", File: "/p/go.work"}
	checkout := forgecompat.Checkout{Dir: "/src/forge", Head: head}
	bin := forgecompat.Binary{Name: "reliant", BuiltAt: time.Date(2026, 10, 1, 1, 31, 0, 0, time.UTC), Embedded: true}

	tests := []struct {
		name string
		rep  forgecompat.BridgeReport
		want doctor.Status
		msg  string
	}{
		{"unbridged", forgecompat.BridgeReport{}, doctor.StatusSkip, "no local forge bridge"},
		{"bridged to a non-git dir", forgecompat.BridgeReport{Bridged: true, Bridge: bridge}, doctor.StatusUnknown, "not a readable git checkout"},
		{"in sync", forgecompat.BridgeReport{Bridged: true, Bridge: bridge, CheckoutOK: true, Checkout: checkout, Binary: bin}, doctor.StatusPass, "reliant was built from that source"},
		{"skewed", forgecompat.BridgeReport{Bridged: true, Bridge: bridge, CheckoutOK: true, Checkout: checkout, Binary: bin, Skewed: true, Kind: forgecompat.SkewUnverifiable},
			doctor.StatusWarn, "reliant (built 2026-10-01 01:31) records no forge commit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := bridgeCheckResult(tc.rep)
			if r.Name != bridgeCheckName || r.Status != tc.want || !strings.Contains(r.Message, tc.msg) {
				t.Errorf("check = %+v; want status %s with %q", r, tc.want, tc.msg)
			}
			if strings.HasPrefix(r.Message, "⚠️") {
				t.Errorf("doctor renders its own status glyph; the message must not carry one: %q", r.Message)
			}
		})
	}
}

// TestWarnBridgeSkew_RealProject drives the whole check against real files
// and real git: a project whose go.work bridges a checkout this test binary
// was not built from warns in one line; the same project unbridged is silent.
func TestWarnBridgeSkew_RealProject(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GOWORK", "")
	base := t.TempDir()
	checkout := filepath.Join(base, "forge")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module github.com/reliant-labs/forge\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd := exec.Command("sh", "-c", "git init -q && git add go.mod && git -c user.name=t -c user.email=t@e commit -q -m init")
	gitCmd.Dir = checkout
	if out, err := gitCmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v\n%s", err, out)
	}

	project := filepath.Join(base, "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var quiet bytes.Buffer
	warnBridgeSkew(&quiet, project)
	if quiet.Len() != 0 {
		t.Fatalf("an unbridged project warned:\n%s", quiet.String())
	}

	work := "go 1.26\n\nuse (\n\t.\n\t" + checkout + "\n)\n"
	if err := os.WriteFile(filepath.Join(project, "go.work"), []byte(work), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	warnBridgeSkew(&out, project)
	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasPrefix(line, "⚠️  forge skew: ") {
		t.Fatalf("want exactly one skew line, got:\n%s", line)
	}
	for _, want := range []string{"go.work bridges " + checkout, "go work edit -dropuse=" + checkout} {
		if !strings.Contains(line, want) {
			t.Errorf("skew line lacks %q:\n%s", want, line)
		}
	}
}

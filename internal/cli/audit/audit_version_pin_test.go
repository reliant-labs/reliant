package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/config"
)

// A PRISTINE `forge project new` must not be reported as divergent.
//
// `forge project new` writes both pins itself, in one run, in two
// vocabularies on purpose:
//
//	forge.yaml  forge_version: v0.0.4-0.20260726192353-613340e38be3+dirty
//	ci.yml      go install …/cmd/forge@613340e38be372ec4014c96e714653d712073fa7
//
// The CI ref has to be resolvable by `go install`, and a `+dirty`
// pseudo-version is not, so a dev build falls back to the raw SHA. The audit
// compared the two as STRINGS and warned "divergent forge version pins
// across the project — run `forge project upgrade` to converge them" on a
// project the running binary had created seconds earlier. The remedy it
// named cannot converge two spellings of one commit.
//
// This is the first command an agent runs on a fresh project.
func TestAuditVersion_PristineDevScaffoldIsNotDivergent(t *testing.T) {
	const (
		commit  = "613340e38be372ec4014c96e714653d712073fa7"
		pseudo  = "v0.0.4-0.20260726192353-613340e38be3+dirty"
		wfInner = "jobs:\n  verify:\n    steps:\n      - run: go install github.com/reliant-labs/forge/cmd/forge@"
	)
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	buildinfo.Set(pseudo, commit, "unknown")

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(wfInner+commit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := auditVersion(&config.ProjectConfig{ForgeVersion: pseudo}, dir)

	if strings.Contains(cat.Summary, "divergent") {
		t.Errorf("a pristine dev scaffold was reported as divergent — the CI SHA and the\n"+
			"forge.yaml pseudo-version are the SAME commit (%s):\n  %s", commit[:12], cat.Summary)
	}
	if cat.Status != audittype.StatusOK {
		t.Errorf("status = %q, want OK on a project this binary just created (summary=%q)", cat.Status, cat.Summary)
	}
}

// The check must still catch REAL divergence — two different commits.
func TestAuditVersion_GenuinelyDifferentCommitsStillWarn(t *testing.T) {
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	buildinfo.Set("v0.0.4-0.20260726192353-613340e38be3", "613340e38be372ec4014c96e714653d712073fa7", "unknown")

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ci := "jobs:\n  verify:\n    steps:\n      - run: go install github.com/reliant-labs/forge/cmd/forge@09863e5d16f4aa11bb22cc33dd44ee55ff667788\n"
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(ci), 0o644); err != nil {
		t.Fatal(err)
	}

	cat := auditVersion(&config.ProjectConfig{ForgeVersion: "v0.0.4-0.20260726192353-613340e38be3"}, dir)
	if !strings.Contains(cat.Summary, "divergent") {
		t.Errorf("two genuinely different commits were NOT flagged: %q", cat.Summary)
	}
	// The message must name the pins AS WRITTEN, not their reduced identity.
	if !strings.Contains(cat.Summary, "v0.0.4-0.20260726192353-613340e38be3") {
		t.Errorf("summary does not show the pin as written in forge.yaml: %q", cat.Summary)
	}
}

func TestPinIdentity(t *testing.T) {
	cases := map[string]string{
		// Same commit, three spellings.
		"613340e38be372ec4014c96e714653d712073fa7":   "613340e38be3",
		"v0.0.4-0.20260726192353-613340e38be3+dirty": "613340e38be3",
		"v0.0.4-0.20260726192353-613340e38be3":       "613340e38be3",
		// A short SHA is still that commit.
		"613340e38be3": "613340e38be3",
		// Release tags and branches are returned verbatim — two different
		// tags must never collapse.
		"v1.2.3": "v1.2.3",
		"main":   "main",
		"":       "",
	}
	for in, want := range cases {
		if got := pinIdentity(in); got != want {
			t.Errorf("pinIdentity(%q) = %q, want %q", in, got, want)
		}
	}
	if pinIdentity("v1.2.3") == pinIdentity("v1.2.4") {
		t.Error("two different release tags collapsed to one identity")
	}
}

// A workflow that installs forge from go.mod at run time carries no pin of
// its own — it cannot diverge from the project — so audit must not read the
// shell variable in it as a version and report "divergent pins".
func TestAuditVersion_RunTimeInstallIsNotAPin(t *testing.T) {
	t.Cleanup(func() { buildinfo.Set("dev", "unknown", "unknown") })
	const pin = "v0.1.18-0.20260927181843-7355bcb3af9c"
	buildinfo.Set(pin, "7355bcb3af9c", "unknown")

	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ci := "jobs:\n  verify:\n    steps:\n      - run: |\n" +
		"          v=$(GOWORK=off go list -m -f '{{.Version}}' github.com/reliant-labs/forge)\n" +
		"          go install \"github.com/reliant-labs/forge/cmd/forge@${v}\"\n"
	if err := os.WriteFile(filepath.Join(wfDir, "ci.yml"), []byte(ci), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ciForgePin(dir); got != "" {
		t.Errorf("ciForgePin read %q out of a run-time install", got)
	}
	cat := auditVersion(&config.ProjectConfig{ForgeVersion: pin}, dir)
	if strings.Contains(cat.Summary, "divergent") {
		t.Errorf("a run-time CI install was reported as a divergent pin: %q", cat.Summary)
	}
}

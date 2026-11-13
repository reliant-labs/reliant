package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A scaffold-written exemption has to satisfy the same contract a hand-written
// one does — id, reason, expiry — and has to land in the user's forge.yaml
// without disturbing anything already there. These tests pin both, plus the
// cases where forge must NOT write: an entry the project already decided
// about, a `ci:` section it would have to merge into, and a frontend type
// whose template carries no such advisory.

const scaffoldTestForgeYAML = `# my notes stay exactly where they are
name: demo
module_path: github.com/example/demo
forge_version: v0.0.0-test
`

var scaffoldTestNow = time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)

func writeScaffoldTestForgeYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readScaffoldTestForgeYAML(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The day-0 case from #754: a Next.js frontend carries braces' no-fix
// advisory, and forge.yaml must come out accepting it — reviewably.
func TestEnsureScaffoldNPMExemptions_NextjsRecordsAReviewedAcceptance(t *testing.T) {
	t.Parallel()

	dir := writeScaffoldTestForgeYAML(t, scaffoldTestForgeYAML)
	EnsureScaffoldNPMExemptions(dir, "nextjs", scaffoldTestNow)

	got := readScaffoldTestForgeYAML(t, dir)
	if !strings.HasPrefix(got, scaffoldTestForgeYAML) {
		t.Fatalf("the user's forge.yaml must survive byte for byte, comments included; got:\n%s", got)
	}

	cfg, err := ReadProjectConfig(filepath.Join(dir, "forge.yaml"))
	if err != nil {
		t.Fatalf("forge.yaml must still load: %v\n%s", err, got)
	}
	ex := cfg.CI.VulnScan.Exemptions
	if len(ex) != 1 || ex[0].ID != "GHSA-vfj7-8cjw-p6xm" {
		t.Fatalf("exemptions = %+v, want exactly the braces advisory", ex)
	}
	if !strings.Contains(ex[0].Reason, "braces") || !strings.Contains(ex[0].Reason, "dev lint toolchain") {
		t.Errorf("the reason must carry the reachability argument, got %q", ex[0].Reason)
	}
	// The review window starts at scaffold time, so a project scaffolded by
	// an old forge binary still gets the full window, not a date that was
	// fixed when that binary was built.
	if want := "2027-01-12"; ex[0].Expires != want {
		t.Errorf("expires = %q, want scaffold date + 90 days = %s", ex[0].Expires, want)
	}
	// Accepting an advisory must not switch any scanner off: the section
	// names only exemptions, so go/docker/npm derive exactly as they did
	// without it.
	before, err := ReadProjectConfig(filepath.Join(writeScaffoldTestForgeYAML(t, scaffoldTestForgeYAML), "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, a := before.CI.VulnScan, cfg.CI.VulnScan
	if a.Go != b.Go || a.Docker != b.Docker || a.NPM != b.NPM || !a.Go {
		t.Errorf("an exemptions-only ci section must leave scanner selection as derived; before %+v, after %+v", b, a)
	}
	if !strings.Contains(got, "# Written when the frontend was scaffolded") {
		t.Errorf("the section must explain itself to whoever finds it in forge.yaml; got:\n%s", got)
	}
}

// A second scaffold (or a re-run) writes nothing: the entry is there.
func TestEnsureScaffoldNPMExemptions_Idempotent(t *testing.T) {
	t.Parallel()

	dir := writeScaffoldTestForgeYAML(t, scaffoldTestForgeYAML)
	EnsureScaffoldNPMExemptions(dir, "nextjs", scaffoldTestNow)
	once := readScaffoldTestForgeYAML(t, dir)
	EnsureScaffoldNPMExemptions(dir, "nextjs", scaffoldTestNow.AddDate(0, 1, 0))
	if twice := readScaffoldTestForgeYAML(t, dir); twice != once {
		t.Errorf("a second run must not touch forge.yaml; before:\n%s\nafter:\n%s", once, twice)
	}
}

// What forge must leave alone: a project that already decided about the
// advisory (in any case spelling), a `ci:` section forge would have to merge
// into, and frontend types whose template carries no no-fix advisory.
func TestEnsureScaffoldNPMExemptions_LeavesTheProjectsDecisionsAlone(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		body, frontendType string
	}{
		"already accepted, re-dated and re-worded": {
			body: scaffoldTestForgeYAML + `ci:
  vuln_scan:
    exemptions:
      - id: ghsa-VFJ7-8cjw-p6xm
        reason: our own words
        expires: "2026-11-01"
`,
			frontendType: "nextjs",
		},
		"a ci section forge would have to merge into": {
			body:         scaffoldTestForgeYAML + "ci:\n  test:\n    coverage: true\n",
			frontendType: "nextjs",
		},
		"vite-spa carries no no-fix advisory": {
			body:         scaffoldTestForgeYAML,
			frontendType: "vite-spa",
		},
		"react-native carries no recorded advisory": {
			body:         scaffoldTestForgeYAML,
			frontendType: "react-native",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := writeScaffoldTestForgeYAML(t, tc.body)
			EnsureScaffoldNPMExemptions(dir, tc.frontendType, scaffoldTestNow)
			if got := readScaffoldTestForgeYAML(t, dir); got != tc.body {
				t.Errorf("forge.yaml must be untouched; got:\n%s", got)
			}
		})
	}
}

// Every recorded advisory must be something the gate can honor: a GHSA id
// (npm's namespace), a reason, and a known frontend type. A typo here would
// write an entry that fails the very gate it exists to pass.
func TestScaffoldNPMAdvisories_AreWellFormed(t *testing.T) {
	t.Parallel()

	known := map[string]bool{"nextjs": true, "vite-spa": true, "react-native": true}
	for _, a := range scaffoldNPMAdvisories {
		if !strings.HasPrefix(a.ID, "GHSA-") || len(a.ID) != len("GHSA-xxxx-xxxx-xxxx") {
			t.Errorf("%q is not a GHSA id, so npm audit could never match it", a.ID)
		}
		if strings.TrimSpace(a.Reason) == "" {
			t.Errorf("%s has no reason; the gate does not honor an unexplained exemption", a.ID)
		}
		if len(a.FrontendTypes) == 0 {
			t.Errorf("%s names no frontend type, so it is never written", a.ID)
		}
		for _, ft := range a.FrontendTypes {
			if !known[ft] {
				t.Errorf("%s names unknown frontend type %q", a.ID, ft)
			}
		}
	}
}

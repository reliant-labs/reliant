// File: internal/cli/release_forge_sbom_test.go
//
// scripts/release-forge.sh attaches a CycloneDX SBOM per published archive
// (<archive>.cdx.json, from `forge vuln sbom --file`) to the GitHub release,
// and scans the binaries for vulnerabilities once, as an audit that never
// fails the release. The forge that runs both is faked
// (FORGE_RELEASE_SBOM_FORGE), like gh: no test needs syft, grype or a network.
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeSBOMForge writes a forge stand-in that answers `vuln sbom --file` with
// a CycloneDX document naming its --artifact and `vuln scan` with a one-line
// summary, logging each call with its working directory. sbomFails names an
// archive whose SBOM it refuses; scanExit is `vuln scan`'s exit code.
func fakeSBOMForge(t *testing.T, sbomFails string, scanExit string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "forge")
	log = filepath.Join(dir, "forge.log")
	script := `#!/usr/bin/env bash
echo "ARGS: $* PWD: $PWD DO_NOT_TRACK: ${DO_NOT_TRACK:-}" >> "` + log + `"
if [ "$1 $2" = "vuln sbom" ]; then
  artifact=""; file=""; prev=""
  for a in "$@"; do
    [ "$prev" = "--artifact" ] && artifact="$a"
    [ "$prev" = "--file" ] && file="$a"
    prev="$a"
  done
  [ -f "$file" ] || { echo "no such file $file" >&2; exit 1; }
  if [ "$artifact" = "` + sbomFails + `" ]; then echo "syft: cataloging failed" >&2; exit 2; fi
  printf '{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"component":{"name":"%s"}}}\n' "$artifact"
  exit 0
fi
if [ "$1 $2" = "vuln scan" ]; then
  echo "fake-scan · 1 of 1 artifacts scanned"
  echo "0 critical, 1 high, 0 medium, 0 low, 0 unknown"
  exit ` + scanExit + `
fi
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestReleaseForgeScript_AttachesAnSBOMPerArchiveAndScansOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo, _ := githubFixtureRepo(t)
	gh, ghLog := fakeGH(t, "", false)
	forge, forgeLog := fakeSBOMForge(t, "", "1")
	host := "forge_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh, "FORGE_RELEASE_SBOM_FORGE=" + forge}, "v0.2.0")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}

	ghCalls, _ := os.ReadFile(ghLog)
	var create string
	for _, l := range strings.Split(string(ghCalls), "\n") {
		if strings.HasPrefix(l, "ARGS: release create v0.2.0 ") {
			create = l
		}
	}
	if create == "" {
		t.Fatalf("no gh release create:\n%s", ghCalls)
	}
	for _, want := range []string{"/" + host + " ", "/checksums.txt", "/" + host + ".cdx.json"} {
		if !strings.Contains(create+" ", want) {
			t.Errorf("the GitHub release does not attach %q:\n%s", strings.Trim(want, "/ "), create)
		}
	}

	forgeCalls, _ := os.ReadFile(forgeLog)
	calls := string(forgeCalls)
	sbomCall := "ARGS: vuln sbom --file "
	if n := strings.Count(calls, sbomCall); n != 1 {
		t.Errorf("want one `vuln sbom --file` per archive (1), got %d:\n%s", n, calls)
	}
	if !strings.Contains(calls, "--artifact "+host+" --release v0.2.0") {
		t.Errorf("the SBOM does not name its archive and release:\n%s", calls)
	}
	if n := strings.Count(calls, "ARGS: vuln scan "); n != 1 {
		t.Errorf("want ONE vulnerability scan, got %d:\n%s", n, calls)
	}
	// Outside the checkout (no forge.yaml pin hands the forge off), and
	// with telemetry off.
	for _, l := range strings.Split(strings.TrimSpace(calls), "\n") {
		if !strings.Contains(l, " PWD: ") || !strings.Contains(l, " DO_NOT_TRACK:") {
			continue
		}
		pwd := l[strings.Index(l, " PWD: ")+len(" PWD: ") : strings.Index(l, " DO_NOT_TRACK:")]
		if strings.HasPrefix(pwd, repo) || !strings.HasSuffix(l, "DO_NOT_TRACK: 1") {
			t.Errorf("forge ran in the checkout or with telemetry on: %s", l)
		}
	}
	// The SBOM is published before the scan runs, and the scan's blocking
	// finding (exit 1) is a warning with its summary, not a failure.
	if !strings.Contains(out, "1 high") || !strings.Contains(out, "the release is out regardless") {
		t.Errorf("the scan's summary and warning are missing:\n%s", out)
	}
	if strings.Index(out, "creating the GitHub release") > strings.Index(out, "scanning the published binaries") {
		t.Errorf("the scan ran before the GitHub release was created:\n%s", out)
	}
}

// An archive whose SBOM cannot be generated, and a scan that cannot run, are
// warnings: the release still publishes every archive and checksums.txt.
func TestReleaseForgeScript_SBOMAndScanFailuresNeverFailTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a build and a git push")
	}
	repo, _ := githubFixtureRepo(t)
	gh, ghLog := fakeGH(t, "", false)
	host := "forge_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	forge, _ := fakeSBOMForge(t, host, "2")

	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_GH=" + gh, "FORGE_RELEASE_SBOM_FORGE=" + forge}, "v0.2.0")
	if err != nil {
		t.Fatalf("an SBOM or scan failure failed the release: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no SBOM for "+host+": syft: cataloging failed") {
		t.Errorf("the missing SBOM is not reported with its reason:\n%s", out)
	}
	if !strings.Contains(out, "could not determine (exit 2") {
		t.Errorf("the failed scan is not reported:\n%s", out)
	}
	ghCalls, _ := os.ReadFile(ghLog)
	if !strings.Contains(string(ghCalls), "/"+host) || !strings.Contains(string(ghCalls), "/checksums.txt") {
		t.Errorf("the archives were not published:\n%s", ghCalls)
	}
	if strings.Contains(string(ghCalls), ".cdx.json") {
		t.Errorf("an SBOM that failed was attached anyway:\n%s", ghCalls)
	}
}

// A dry run generates no SBOM and runs no scan: rc-train dry-runs every
// release before it cuts it.
func TestReleaseForgeScript_DryRunGeneratesNoSBOM(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the release script, which builds the fixture module")
	}
	repo := newForgeFixtureRepo(t)
	forge, forgeLog := fakeSBOMForge(t, "", "0")
	out, err := runForgeScriptEnv(t, repo, []string{"FORGE_RELEASE_SBOM_FORGE=" + forge}, "--dry-run", "v0.2.0")
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, out)
	}
	if calls, _ := os.ReadFile(forgeLog); len(calls) != 0 {
		t.Errorf("a dry run ran forge:\n%s", calls)
	}
	if !strings.Contains(out, "DRY RUN: would attach an SBOM per archive") {
		t.Errorf("the dry run does not describe the SBOMs:\n%s", out)
	}
}

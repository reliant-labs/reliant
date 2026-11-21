//go:build e2e

package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestE2EClaimableNewConvergesInOneRun is the landing page's first step,
// `forge project new <p> --service <s> --claimable`, followed by what
// `forge env deploy prod --claimable` renders first — with no `forge generate`
// in between. Bug d2a5b5ba: the first generate's composition graph could come
// out untyped (a module download failed on a cold module cache), so
// config_gen.k's server root reached no credential and both `project new`
// and the first deploy failed with "config_secrets names [DATABASE_URL],
// which root \"server\" does not reach (it reaches [])" until a second
// generate. The fix is in the graph's type load
// (TestGraphUniverseRetriesAModuleFetchThatFailedInTransport); this pins the
// outcome a user sees: one run, a tree whose prod renders.
func TestE2EClaimableNewConvergesInOneRun(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()
	runCmd(t, dir, forgeBin, "project", "new", "probecanary", "--mod", "example.com/probecanary", "--service", "notes", "--claimable")
	projectDir := filepath.Join(dir, "probecanary")

	cfg, err := os.ReadFile(filepath.Join(projectDir, "deploy", "kcl", "config_gen.k"))
	if err != nil {
		t.Fatal(err)
	}
	server := regexp.MustCompile(`(?s)"server" = Root \{.*?secrets = (\[[^\]]*\])`).FindStringSubmatch(string(cfg))
	if server == nil {
		t.Fatalf("config_gen.k has no server root:\n%s", cfg)
	}
	if !strings.Contains(server[1], `"DATABASE_URL"`) {
		t.Fatalf("the server root reaches secrets %s after one `project new`, not DATABASE_URL — the scaffold's api workload opts into it, so prod cannot render", server[1])
	}

	addCorpusForgePkgReplace(t, projectDir)
	out := runCmdOutput(t, projectDir, forgeBin, "env", "render", "prod")
	for _, want := range []string{"api (service)", "probecanary (database)"} {
		if !strings.Contains(out, want) {
			t.Errorf("prod render lacks %q:\n%s", want, out)
		}
	}
}

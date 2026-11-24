package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/templates"
)

func writeProjectFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pgo-refresh.yml runs `forge pgo pull --env <env>`, which writes a profile
// only for the Go binaries that env builds. reliant's one hosted env (demo)
// serves a static site, so it was given a workflow with nothing to pull, and
// deleted it by hand. The workflow is born only where a hosted env builds a
// Go binary, and pulls the last such env in promotion order.
func TestPGORefresh_OnlyWhereAHostedEnvBuildsAGoBinary(t *testing.T) {
	root := t.TempDir()
	writeEnvMain(t, root, "dev", "staging", "prod")
	hosted := []string{"staging", "prod"}
	for name, tc := range map[string]struct {
		pgo     []string
		wantEnv string // "" = no pgo-refresh.yml
	}{
		"no hosted env builds a Go binary (a static site)": {nil, ""},
		"prod builds one":                      {[]string{"prod"}, "prod"},
		"both do: the last in promotion order": {[]string{"staging", "prod"}, "prod"},
		"only staging does":                    {[]string{"staging"}, "staging"},
		"a non-hosted env is not pulled":       {[]string{"dev"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			files := CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: hosted, PGOEnvs: tc.pgo})
			data, ok := ciFile(files, PGORefreshPath).(templates.PGORefreshWorkflowData)
			if tc.wantEnv == "" {
				if ok {
					t.Fatalf("planned %s pulling %q; want none", PGORefreshPath, data.Env)
				}
				if !ciDests(files)[".github/workflows/release.yml"] {
					t.Error("the hosted release pipeline must not depend on PGO")
				}
				return
			}
			if !ok || data.Env != tc.wantEnv {
				t.Fatalf("pgo-refresh.yml = %+v (planned %v), want it pulling %s", data, ok, tc.wantEnv)
			}
		})
	}
}

// A project that already keeps its profiles current is not given a second
// weekly job: control-plane runs it as a Reliant workflow named pgo-refresh
// (which calls a script), others in a workflow of their own that runs `forge
// pgo pull`. Detection is narrow: those two shapes, nothing else.
func TestPGORefreshDeclaredElsewhere(t *testing.T) {
	for name, tc := range map[string]struct {
		files map[string]string
		want  string
	}{
		"nothing":                                   {nil, ""},
		"a Reliant workflow named pgo-refresh":      {map[string]string{".reliant/workflows/pgo-refresh.yaml": "steps:\n  - run: scripts/pgo-refresh.sh\n"}, ".reliant/workflows/pgo-refresh.yaml"},
		"its .yml spelling":                         {map[string]string{".reliant/workflows/pgo-refresh.yml": "x: 1\n"}, ".reliant/workflows/pgo-refresh.yml"},
		"an Actions workflow running the pull":      {map[string]string{".github/workflows/profiles.yml": "jobs:\n  p:\n    steps:\n      - run: forge pgo pull --env prod\n"}, ".github/workflows/profiles.yml"},
		"a Reliant workflow running it via reliant": {map[string]string{".reliant/workflows/weekly.yaml": "command: reliant forge pgo pull --env prod --json\n"}, ".reliant/workflows/weekly.yaml"},
		"a .yaml Actions workflow":                  {map[string]string{".github/workflows/p.yaml": "- run: |\n    forge pgo pull\n"}, ".github/workflows/p.yaml"},
		"only a comment mentions it":                {map[string]string{".github/workflows/ci.yml": "# see forge pgo pull\n  # forge pgo pull --env prod\n"}, ""},
		"forge's own pgo-refresh.yml":               {map[string]string{PGORefreshPath: "      - run: forge pgo pull --env prod --json\n"}, ""},
		"a script, not a workflow":                  {map[string]string{"scripts/pgo-refresh.sh": "forge pgo pull\n"}, ""},
		"a nested file is not a workflow":           {map[string]string{".github/workflows/sub/x.yml": "run: forge pgo pull\n"}, ""},
		"another pgo verb":                          {map[string]string{".github/workflows/x.yml": "run: forge pgo status\n"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for rel, body := range tc.files {
				writeProjectFile(t, root, rel, body)
			}
			if got := PGORefreshDeclaredElsewhere(root); got != tc.want {
				t.Errorf("PGORefreshDeclaredElsewhere = %q, want %q", got, tc.want)
			}
		})
	}

	root := t.TempDir()
	writeEnvMain(t, root, "dev", "prod")
	writeProjectFile(t, root, ".reliant/workflows/pgo-refresh.yaml", "x: 1\n")
	if dests := ciDests(CIWorkflowsFor(root, serviceCfg(), CIInputs{HostedEnvs: []string{"prod"}, PGOEnvs: []string{"prod"}})); dests[PGORefreshPath] {
		t.Error("planned pgo-refresh.yml beside the project's own Reliant pgo-refresh workflow")
	}
}

// The skip is said once per generate, naming the file that stands in and the
// forge.yaml key that silences it, and only while forge would otherwise have
// written the workflow.
func TestPGORefreshNotice(t *testing.T) {
	in := CIInputs{HostedEnvs: []string{"prod"}, PGOEnvs: []string{"prod"}}
	setup := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		checksums.ResetScaffoldLedgerCache()
		t.Cleanup(checksums.ResetScaffoldLedgerCache)
		writeProjectFile(t, root, ".reliant/workflows/pgo-refresh.yaml", "x: 1\n")
		return root
	}

	root := setup(t)
	note := PGORefreshNotice(root, serviceCfg(), in)
	if !strings.Contains(note, ".reliant/workflows/pgo-refresh.yaml") || !strings.Contains(note, "ci.workflows: {pgo-refresh: false}") || strings.Contains(note, "\n") {
		t.Errorf("notice = %q, want one line naming the Reliant workflow and the opt-out", note)
	}

	declined := serviceCfg()
	declined.CI.Workflows = map[string]bool{"pgo-refresh": false}
	if note := PGORefreshNotice(setup(t), declined, in); note != "" {
		t.Errorf("declined in forge.yaml, still noted: %q", note)
	}
	if note := PGORefreshNotice(setup(t), serviceCfg(), CIInputs{HostedEnvs: []string{"prod"}}); note != "" {
		t.Errorf("no Go binary to profile, still noted: %q", note)
	}
	deleted := setup(t)
	checksums.RecordScaffold(deleted, PGORefreshPath)
	if note := PGORefreshNotice(deleted, serviceCfg(), in); note != "" {
		t.Errorf("deleted after birth, still noted: %q", note)
	}
	if note := PGORefreshNotice(t.TempDir(), serviceCfg(), in); note != "" {
		t.Errorf("nothing stands in for it, yet noted: %q", note)
	}
}

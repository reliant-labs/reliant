package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

func runCIOIDCFix(t *testing.T, dir string, write bool) codemod.Report {
	t.Helper()
	c, ok := codemod.Lookup("ci-oidc-permissions")
	if !ok {
		t.Fatal("ci-oidc-permissions is not registered")
	}
	rep, err := c.Run(dir, codemod.Options{Write: write})
	if err != nil {
		t.Fatalf("ci-oidc-permissions: %v", err)
	}
	return rep
}

// ciOIDCBlock is the job-level block the codemod writes at indent 4, the
// scaffold's comment first.
func ciOIDCBlock(t *testing.T, entries ...string) string {
	t.Helper()
	comment, err := ciOIDCPermissionsComment()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, c := range comment {
		b.WriteString("    " + c + "\n")
	}
	b.WriteString("    permissions:\n")
	for _, e := range entries {
		b.WriteString("      " + e + "\n")
	}
	return b.String()
}

// Each job whose steps run `forge test report` gets `id-token: write`, in
// the one place that grants it, with every other byte of the file kept.
func TestCIOIDCPermissions_Table(t *testing.T) {
	const lint = "  lint:\n    runs-on: ubuntu-latest\n    steps:\n      - run: make lint\n"
	for name, tc := range map[string]struct {
		in, want string // want "" = unchanged
		rewrites int
		note     string // a substring of Next
		pending  bool
	}{
		"no permissions anywhere": {
			in:       "on: push\njobs:\n" + lint + "  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload\n",
			want:     "on: push\njobs:\n" + lint + "  test:\n" + ciOIDCBlock(t, "contents: read", "id-token: write") + "    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload\n",
			rewrites: 1,
			note:     "default GITHUB_TOKEN permissions",
		},
		"workflow-level only": {
			in:       "on: push\npermissions:\n  contents: read\n  pull-requests: write\njobs:\n" + lint + "  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: task test:report -- --upload || true\n",
			want:     "on: push\npermissions:\n  contents: read\n  pull-requests: write\njobs:\n" + lint + "  test:\n" + ciOIDCBlock(t, "contents: read", "pull-requests: write", "id-token: write") + "    runs-on: ubuntu-latest\n    steps:\n      - run: task test:report -- --upload || true\n",
			rewrites: 1,
		},
		"an existing job block": {
			in:       "on: push\npermissions:\n  contents: read\njobs:\n  test:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      checks: write\n    steps:\n      - run: forge test report\n",
			want:     "on: push\npermissions:\n  contents: read\njobs:\n  test:\n    runs-on: ubuntu-latest\n    permissions:\n      contents: read\n      checks: write\n      id-token: write\n    steps:\n      - run: forge test report\n",
			rewrites: 1,
		},
		"a flow-style job block": {
			in:       "on: push\njobs:\n  test:\n    permissions: { contents: read }\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			want:     "on: push\njobs:\n  test:\n    permissions: { contents: read, id-token: write }\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			rewrites: 1,
		},
		"already present on the job": {
			in: "on: push\njobs:\n  test:\n    permissions:\n      contents: read\n      id-token: write\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
		},
		"inherited from the workflow": {
			in: "on: push\npermissions:\n  contents: read\n  id-token: write\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
		},
		"write-all": {
			in: "on: push\npermissions: write-all\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
		},
		"declined on the job": {
			in:      "on: push\njobs:\n  test:\n    permissions:\n      contents: read\n      id-token: none\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			note:    "job test (line 3) uploads test results but sets `id-token: none`",
			pending: true,
		},
		"declined by the workflow, none": {
			in:      "on: push\npermissions:\n  contents: read\n  id-token: none\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			note:    "inherits the workflow's `id-token: none`",
			pending: true,
		},
		"declined by the workflow, read": {
			in:      "on: push\npermissions:\n  contents: read\n  id-token: read\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			note:    "inherits the workflow's `id-token: read`",
			pending: true,
		},
		"declined, but that job does not upload": {
			in: "on: push\npermissions:\n  id-token: none\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload=false\n",
		},
		"the report named outside steps[].run": {
			in: "# runs forge test report later\non: push\nenv:\n  NOTE: forge test report\njobs:\n  test:\n    name: forge test report\n    runs-on: ubuntu-latest\n    env:\n      run: forge test report --upload\n    steps:\n      - name: task test:report\n        with:\n          run: forge test report\n      - run: go test ./... # then forge test report\n",
		},
		"a compound command": {
			in:       "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: task test && forge test report --upload || echo skipped\n",
			want:     "on: push\njobs:\n  test:\n" + ciOIDCBlock(t, "contents: read", "id-token: write") + "    runs-on: ubuntu-latest\n    steps:\n      - run: task test && forge test report --upload || echo skipped\n",
			rewrites: 1,
			note:     "default GITHUB_TOKEN permissions",
		},
		"one invocation off, another uploads": {
			in:       "on: push\npermissions:\n  contents: read\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload=false; forge test report --upload\n",
			want:     "on: push\npermissions:\n  contents: read\njobs:\n  test:\n" + ciOIDCBlock(t, "contents: read", "id-token: write") + "    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload=false; forge test report --upload\n",
			rewrites: 1,
		},
		"every invocation off": {
			in: "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: |\n          forge test report --upload false\n          task test:report -- --upload=off\n",
		},
		"read-all is a hand edit": {
			in:      "on: push\npermissions: read-all\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report\n",
			note:    "`permissions: read-all`",
			pending: true,
		},
		"comments preserved": {
			in: "# top\non: push # trigger\npermissions:\n  contents: read # least privilege\njobs:\n  # the unit lane\n  test:\n    name: Test # shown in checks\n    # where it runs\n    runs-on: ubuntu-latest\n    steps:\n      # report\n      - run: 'forge test report --upload' # quoted\n",
			want: "# top\non: push # trigger\npermissions:\n  contents: read # least privilege\njobs:\n  # the unit lane\n  test:\n    name: Test # shown in checks\n" + ciOIDCBlock(t, "contents: read", "id-token: write") +
				"    # where it runs\n    runs-on: ubuntu-latest\n    steps:\n      # report\n      - run: 'forge test report --upload' # quoted\n",
			rewrites: 1,
		},
		"a job without forge test report": {
			in: "on: push\njobs:\n" + lint + "  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n      - run: echo forge test reports later\n",
		},
		"upload turned off": {
			in: "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: forge test report --upload=false\n",
		},
		"two jobs, CRLF": {
			in:       "on: push\r\npermissions:\r\n  contents: read\r\njobs:\r\n  a:\r\n    runs-on: x\r\n    steps:\r\n      - run: forge test report\r\n  b:\r\n    runs-on: x\r\n    steps:\r\n      - run: forge test report\r\n",
			want:     strings.ReplaceAll("on: push\npermissions:\n  contents: read\njobs:\n  a:\n"+ciOIDCBlock(t, "contents: read", "id-token: write")+"    runs-on: x\n    steps:\n      - run: forge test report\n  b:\n"+ciOIDCBlock(t, "contents: read", "id-token: write")+"    runs-on: x\n    steps:\n      - run: forge test report\n", "\n", "\r\n"),
			rewrites: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			wf := writeProjectFile(t, dir, ".github/workflows/ci.yml", tc.in)
			if got, want := projectNeedsCIOIDCPermissions(dir), tc.want != "" || tc.pending; got != want {
				t.Errorf("detection = %v, want %v", got, want)
			}
			dry := runCIOIDCFix(t, dir, false)
			if readString(t, wf) != tc.in {
				t.Fatal("a dry run wrote the workflow")
			}
			rep := runCIOIDCFix(t, dir, true)
			want := tc.want
			if want == "" {
				want = tc.in
			}
			if got := readString(t, wf); got != want {
				t.Errorf("workflow =\n%s\nwant\n%s\nreport %+v", got, want, rep)
			}
			if len(rep.Rewrites) != tc.rewrites || len(dry.Rewrites) != tc.rewrites {
				t.Errorf("rewrites = %q (dry %q), want %d", rep.Rewrites, dry.Rewrites, tc.rewrites)
			}
			if tc.note != "" && !strings.Contains(strings.Join(rep.Next, "\n"), tc.note) {
				t.Errorf("Next = %q, want one naming %q", rep.Next, tc.note)
			}
			if tc.note == "" && len(rep.Next) > 0 {
				t.Errorf("Next = %q, want none", rep.Next)
			}
			again := runCIOIDCFix(t, dir, true)
			if again.Changed() || readString(t, wf) != want {
				t.Errorf("second run changed the workflow: %q", again.Rewrites)
			}
			if projectNeedsCIOIDCPermissions(dir) != tc.pending {
				t.Errorf("after the fix, detection = %v, want %v", !tc.pending, tc.pending)
			}
		})
	}
}

// forge fix tests-capture on a ci.yml scaffolded before v1.12.0 (no report
// step, no job-level permissions) writes the current template byte for byte:
// the step uploads with OIDC, so its job gets `id-token: write` too.
func TestTestsCapture_CIGetsOIDCPermissions(t *testing.T) {
	src, err := templates.CITemplates("github").Render("ci.yml.tmpl", templates.CIWorkflowData{
		ProjectName: "demo", LintGolangci: true, TestRace: true, PermContents: "read",
		Module: "github.com/example/demo", HasDocker: true, VerifyGenerated: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cur := string(src)
	block := ciOIDCBlock(t, "contents: read", "id-token: write")
	if strings.Count(cur, block) != 2 {
		t.Fatalf("the template has %d job-level OIDC blocks, want 2 (test, integration-test)", strings.Count(cur, block))
	}
	old := strings.ReplaceAll(ciWithoutReport(t, cur), block, "")
	dir := t.TempDir()
	writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "service"))
	ci := writeProjectFile(t, dir, testsCaptureCIPath, old)
	rep := runTestsCaptureFix(t, dir, true)
	if got := readString(t, ci); got != cur {
		t.Errorf("ci.yml is not the template's:\n%s\nreport %+v", got, rep)
	}
	if projectNeedsCIOIDCPermissions(dir) {
		t.Error("the ci-oidc-uploads playbook is still pending after tests-capture")
	}
}

// The playbook's automation is the codemod, and its detection the codemod's
// own decision, offered to a project below the release.
func TestUpgradeMigrationCIOIDC_DetectionIsTheCodemods(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatal(err)
	}
	var m migrationMeta
	for _, c := range metas {
		if c.ID == ciOIDCPlaybookID {
			m = c
		}
	}
	if m.ID == "" {
		t.Fatalf("no shipped playbook %s", ciOIDCPlaybookID)
	}
	if m.Version != ciOIDCRelease || m.SkillPath != ciOIDCPlaybookSkill {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Automation) != 1 || m.Automation[0] != "fix:ci-oidc-permissions" {
		t.Errorf("automation = %v, want [fix:ci-oidc-permissions]", m.Automation)
	}
	const lacking = "on: push\njobs:\n  test:\n    runs-on: x\n    steps:\n      - run: task test:report -- --upload\n"
	for name, tc := range map[string]struct {
		files map[string]string
		want  bool
		stays bool // still offered after the codemod: a hand edit is left
	}{
		"no workflows":          {files: nil, want: false},
		"a job lacking it":      {files: map[string]string{".github/workflows/ci.yml": lacking}, want: true},
		"in a .yaml workflow":   {files: map[string]string{".github/workflows/tests.yaml": lacking}, want: true},
		"granted":               {files: map[string]string{".github/workflows/ci.yml": strings.Replace(lacking, "    runs-on", "    permissions:\n      id-token: write\n    runs-on", 1)}, want: false},
		"no report":             {files: map[string]string{".github/workflows/ci.yml": "on: push\njobs:\n  test:\n    runs-on: x\n    steps:\n      - run: go test\n"}, want: false},
		"outside the workflows": {files: map[string]string{"ci.yml": lacking}, want: false},
		// An explicit `id-token: none` is never overridden, and never complete.
		"declined by the workflow": {files: map[string]string{".github/workflows/ci.yml": strings.Replace(lacking, "jobs:", "permissions:\n  id-token: none\njobs:", 1)}, want: true, stays: true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for rel, body := range tc.files {
				writeProjectFile(t, dir, rel, body)
			}
			if got := migrationApplies(m, "v1.11.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
			if migrationApplies(m, ciOIDCRelease, dir) {
				t.Error("applies to a project already at the release")
			}
			if tc.want {
				runCIOIDCFix(t, dir, true)
				if got := migrationApplies(m, "v1.11.0", dir); got != tc.stays {
					t.Errorf("applies after the fix = %v, want %v:\n%s", got, tc.stays, readString(t, filepath.Join(dir, ".github/workflows", firstKey(tc.files))))
				}
			}
		})
	}
}

func firstKey(m map[string]string) string {
	for k := range m {
		return filepath.Base(k)
	}
	return ""
}

// A step's script uploads when any simple command in it runs the report
// without turning upload off; ambiguous scripts that name it count.
func TestCIOIDCRunUploads(t *testing.T) {
	for run, want := range map[string]bool{
		"forge test report":                                          true,
		"forge test report --upload":                                 true,
		"forge test report --upload=required":                        true,
		"forge test report --upload=false":                           false,
		"forge test report --upload false":                           false,
		"forge test report --upload=OFF":                             false,
		"forge test report --upload=false --upload":                  true, // the last flag wins
		"forge test report --upload=false; forge test report":        true,
		"forge test report; forge test report --upload=false":        true,
		"forge test report --upload=false && echo forge test report": false,
		"task test:report -- --upload || echo \"exited $?\"":         true,
		"task test:report -- --upload=false":                         false,
		"task lint test:report":                                      true,
		"task test -- test:report":                                   false,
		"FORGE_TESTS=on env forge test report":                       true,
		"/usr/local/bin/forge test report":                           true,
		"reliant forge test report --upload":                         true,
		"\"$FORGE\" test report":                                     true,
		"forge test \\\n  report --upload":                           true,
		"go test ./... | tee out.txt":                                false,
		"echo 'forge test report'":                                   false,
		"echo forge test report":                                     false,
		"# forge test report\ngo test ./...":                         false,
		"go test ./... # then forge test report":                     false,
		"forge test capture -- go test ./...":                        false,
		"(cd sub && forge test report)":                              true,
		"echo \"$(forge test report --upload=false)\"":               true, // ambiguous: granted
		"forge test report 'unterminated":                            true, // ambiguous: granted
	} {
		if got := ciOIDCRunUploads(run); got != want {
			t.Errorf("ciOIDCRunUploads(%q) = %v, want %v", run, got, want)
		}
	}
}

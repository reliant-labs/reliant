package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/templates"
)

// The fixtures under testdata/tests_capture are the Taskfiles older forges
// scaffolded, rendered from the templates in git history:
//
//	service, cli, library     787b48923~1 (the last shape before recording; v1.11.0)
//	service-single-quoted     c63bd4e7c~1 (single-quoted lines, `vars: {SCRIPT: …}`; v1.0.0)
//	service-no-forge-var      76cc03874 (heavy slots, but no FORGE var; v0.1.46)

func runTestsCaptureFix(t *testing.T, dir string, write bool) codemod.Report {
	t.Helper()
	c, ok := codemod.Lookup("tests-capture")
	if !ok {
		t.Fatal("tests-capture is not registered")
	}
	rep, err := c.Run(dir, codemod.Options{Write: write})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func testsCaptureFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "tests_capture", name+".Taskfile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodedTasks(t *testing.T, src []byte) map[string]any {
	t.Helper()
	var doc struct {
		Tasks map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(src, &doc); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	return doc.Tasks
}

func renderedTemplateTasks(t *testing.T, tmpl string) map[string]any {
	t.Helper()
	src, err := templates.ProjectTemplates().Render(tmpl, map[string]any{"Name": "demo", "ProjectName": "demo", "FrontendName": "web"})
	if err != nil {
		t.Fatal(err)
	}
	return decodedTasks(t, src)
}

var testsCaptureLaneTasks = []string{"test", "test:integration", "test:e2e", "test:frontend", "test:report", "frontends"}

// Each old scaffolded shape becomes, task for task, what the current
// template renders, and every other task is left exactly as it was.
func TestTestsCapture_RewritesOldLanesToTheTemplates(t *testing.T) {
	for _, tc := range []struct {
		fixture, template string
		rewrites          int
		compare           []string // tasks that must equal the template's
	}{
		{"service", "Taskfile.yml.tmpl", 5, testsCaptureLaneTasks},
		{"cli", "Taskfile.cli.yml.tmpl", 3, []string{"test", "test:integration", "test:report"}},
		{"library", "Taskfile.library.yml.tmpl", 3, []string{"test", "test:integration", "test:report"}},
		// Its `frontends` task predates other edits, so only the lanes are compared.
		{"service-single-quoted", "Taskfile.yml.tmpl", 5, []string{"test", "test:integration", "test:e2e", "test:frontend", "test:report"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			dir := t.TempDir()
			old := testsCaptureFixture(t, tc.fixture)
			p := writeTaskfile(t, dir, "Taskfile.yml", old)

			dry := runTestsCaptureFix(t, dir, false)
			if got := readString(t, p); got != old {
				t.Fatal("a dry run wrote the Taskfile")
			}
			rep := runTestsCaptureFix(t, dir, true)
			if !reflect.DeepEqual(dry, rep) {
				t.Errorf("dry run reported\n%#v\nthe write\n%#v", dry, rep)
			}
			if len(rep.Next) != 0 {
				t.Errorf("Next = %q, want none", rep.Next)
			}
			if len(rep.Rewrites) != tc.rewrites {
				t.Errorf("Rewrites = %q, want %d", rep.Rewrites, tc.rewrites)
			}
			out := []byte(readString(t, p))
			got, want, before := decodedTasks(t, out), renderedTemplateTasks(t, tc.template), decodedTasks(t, []byte(old))
			for _, name := range tc.compare {
				if !reflect.DeepEqual(got[name], want[name]) {
					t.Errorf("task %s = %#v\nwant the template's %#v", name, got[name], want[name])
				}
			}
			for name, task := range before {
				if containsString(testsCaptureLaneTasks, name) {
					continue
				}
				if !reflect.DeepEqual(got[name], task) {
					t.Errorf("task %s changed", name)
				}
			}
			// The lines are the template's own, byte for byte.
			tmpls, err := testsCaptureTemplates()
			if err != nil {
				t.Fatal(err)
			}
			fam := "go"
			if strings.HasPrefix(tc.fixture, "service") {
				fam = "service"
			}
			line, _, _ := tmpls[fam].captureLine("test")
			if !strings.Contains(string(out), line+"\n") {
				t.Errorf("the unit lane is not the template's line %q", line)
			}
			// Re-running it changes nothing.
			again := runTestsCaptureFix(t, dir, true)
			if again.Changed() || len(again.Next) != 0 || readString(t, p) != string(out) {
				t.Errorf("second run: %+v", again)
			}
			if projectNeedsTestsCapture(dir) {
				t.Error("detection still pending after the codemod")
			}
		})
	}
}

// A lane someone edited is named with the edit to make and left byte for
// byte; the lanes still in forge's shape are rewritten beside it, and it
// does not keep the playbook pending.
func TestTestsCapture_LeavesAnEditedLaneAndNamesIt(t *testing.T) {
	dir := t.TempDir()
	old := testsCaptureFixture(t, "service")
	const was = `-tags=integration -count=1 -timeout=10m`
	edited := strings.Replace(old, was, `-tags=integration -count=1 -p 1 -timeout=10m`, 1)
	if edited == old {
		t.Fatal("fixture has no integration lane to edit")
	}
	p := writeTaskfile(t, dir, "Taskfile.yml", edited)
	rep := runTestsCaptureFix(t, dir, true)
	if len(rep.Next) != 1 || !strings.Contains(rep.Next[0], "task `test:integration` is not the shape forge scaffolded") ||
		!strings.Contains(rep.Next[0], "--lane integration") {
		t.Fatalf("Next = %q", rep.Next)
	}
	out := readString(t, p)
	if !strings.Contains(out, `-tags=integration -count=1 -p 1 -timeout=10m`) {
		t.Error("the edited lane was changed")
	}
	if !strings.Contains(out, "--lane unit") || !strings.Contains(out, "\n  test:report:\n") {
		t.Error("the recognised lanes were not rewritten")
	}
	if projectNeedsTestsCapture(dir) {
		t.Error("an edited lane keeps the playbook pending")
	}
	if again := runTestsCaptureFix(t, dir, true); again.Changed() || len(again.Next) != 1 {
		t.Errorf("second run: %+v", again)
	}
}

// A Taskfile from before the FORGE var: its old lanes are recognised and
// keep the playbook pending, but nothing is written; each names the vars to
// add first.
func TestTestsCapture_OldLanesWithoutTheVarsTheyNeed(t *testing.T) {
	dir := t.TempDir()
	old := testsCaptureFixture(t, "service-no-forge-var")
	p := writeTaskfile(t, dir, "Taskfile.yml", old)
	rep := runTestsCaptureFix(t, dir, true)
	if rep.Changed() || readString(t, p) != old {
		t.Fatalf("wrote a Taskfile with no FORGE var: %q", rep.Rewrites)
	}
	var forgeVar int
	for _, n := range rep.Next {
		if strings.Contains(n, "no top-level FORGE var") {
			forgeVar++
		}
	}
	// test, test:integration, test:e2e, and test:report; its inline-loop
	// test:frontend is an older shape, named as edited.
	if forgeVar != 4 {
		t.Errorf("Next = %q, want 4 naming the FORGE var", rep.Next)
	}
	if !projectNeedsTestsCapture(dir) {
		t.Error("detection is not pending for old lanes")
	}
}

// Nothing is written while forge.yaml pins a released forge without
// `forge test`; at the release it is.
func TestTestsCapture_WaitsForThePin(t *testing.T) {
	for _, tc := range []struct {
		pin   string
		write bool
	}{{"v1.11.0", false}, {"v1.12.0-rc.1", true}, {"v1.12.0", true}, {"dev", true}} {
		t.Run(tc.pin, func(t *testing.T) {
			dir := newTestProjectWithVersion(t, tc.pin)
			old := testsCaptureFixture(t, "cli")
			p := writeTaskfile(t, dir, "Taskfile.yml", old)
			rep := runTestsCaptureFix(t, dir, true)
			if wrote := readString(t, p) != old; wrote != tc.write {
				t.Fatalf("wrote = %v, want %v (report %+v)", wrote, tc.write, rep)
			}
			if !tc.write && (len(rep.Next) == 0 || !strings.Contains(rep.Next[0], "pins forge "+tc.pin)) {
				t.Errorf("Next = %q, want the pin first", rep.Next)
			}
			if got := projectNeedsTestsCapture(dir); got != !tc.write {
				t.Errorf("detection pending = %v after a run that wrote = %v", got, tc.write)
			}
		})
	}
}

// ciWithoutReport is the current ci.yml with every report step removed:
// exactly what the template rendered before it gained them.
func ciWithoutReport(t *testing.T, cur string) string {
	t.Helper()
	step, err := testsCaptureCIStep()
	if err != nil {
		t.Fatal(err)
	}
	block := strings.Join(step.lines, "\n") + "\n"
	if !strings.Contains(cur, block) {
		t.Fatal("rendered ci.yml does not contain the report step")
	}
	return strings.ReplaceAll(cur, block, "")
}

// The CI step turns the old scaffolded ci.yml into the current one, byte
// for byte, with or without GOTESTRACE=.
func TestTestsCapture_CIStepMatchesTheTemplate(t *testing.T) {
	for _, race := range []bool{true, false} {
		src, err := templates.CITemplates("github").Render("ci.yml.tmpl", templates.CIWorkflowData{
			ProjectName: "demo", LintGolangci: true, TestRace: race, PermContents: "read",
			Module: "github.com/example/demo", HasDocker: true, VerifyGenerated: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		cur := string(src)
		old := ciWithoutReport(t, cur)
		dir := t.TempDir()
		writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "service"))
		ci := writeProjectFile(t, dir, testsCaptureCIPath, old)
		rep := runTestsCaptureFix(t, dir, true)
		if got := readString(t, ci); got != cur {
			t.Errorf("race=%v: ci.yml is not the template's:\n%s", race, got)
		}
		var steps int
		for _, r := range rep.Rewrites {
			if strings.HasPrefix(r, testsCaptureCIPath) {
				steps++
			}
		}
		if steps != 2 {
			t.Errorf("race=%v: CI rewrites = %d, want 2 (test, integration-test): %q", race, steps, rep.Rewrites)
		}
		if again := runTestsCaptureFix(t, dir, true); again.Changed() || readString(t, ci) != cur {
			t.Errorf("race=%v: second run changed ci.yml: %q", race, again.Rewrites)
		}
	}
}

// A CI test step that is not forge's (`go test ./...`) is the person's: no
// edit, one line saying so.
func TestTestsCapture_CIWithoutTaskTest(t *testing.T) {
	dir := t.TempDir()
	writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "cli"))
	const wf = "on: push\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: go test ./...\n"
	ci := writeProjectFile(t, dir, testsCaptureCIPath, wf)
	rep := runTestsCaptureFix(t, dir, true)
	if readString(t, ci) != wf {
		t.Error("ci.yml changed")
	}
	var named bool
	for _, n := range rep.Next {
		named = named || strings.Contains(n, "no step whose `run:` is `task test`")
	}
	if !named {
		t.Errorf("Next = %q", rep.Next)
	}
}

// A dash-form step (`- run: task test`) in a job of its own indentation
// gets the step at that indentation.
func TestTestsCapture_CIDashFormStep(t *testing.T) {
	dir := t.TempDir()
	writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "cli"))
	wf := "on: push\njobs:\n    test:\n        runs-on: ubuntu-latest\n        steps:\n            - run: task test\n            - run: echo done\n"
	ci := writeProjectFile(t, dir, testsCaptureCIPath, wf)
	rep := runTestsCaptureFix(t, dir, true)
	out := readString(t, ci)
	if !strings.Contains(out, "            - run: task test\n            # The lanes above") ||
		!strings.Contains(out, "              run: task test:report -- --upload") ||
		!strings.HasSuffix(out, "            - run: echo done\n") {
		t.Errorf("ci.yml =\n%s\nreport %+v", out, rep)
	}
}

func shippedTestsCapturePlaybook(t *testing.T) migrationMeta {
	t.Helper()
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatalf("loadMigrationMetas: %v", err)
	}
	for _, m := range metas {
		if m.ID == testsCapturePlaybookID {
			return m
		}
	}
	t.Fatalf("no shipped playbook %s", testsCapturePlaybookID)
	return migrationMeta{}
}

// The playbook's automation is the codemod and its detection the codemod's
// own reading of the Taskfile.
func TestUpgradeMigrationTestsCapture_DetectionIsTheCodemods(t *testing.T) {
	m := shippedTestsCapturePlaybook(t)
	if m.Version != testsCaptureRelease || m.SkillPath != testsCapturePlaybookSkill {
		t.Errorf("meta = %+v", m)
	}
	if len(m.Automation) != 1 || m.Automation[0] != "fix:tests-capture" {
		t.Errorf("automation = %v, want [fix:tests-capture]", m.Automation)
	}
	upgraded := func(t *testing.T) string {
		dir := t.TempDir()
		writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "library"))
		runTestsCaptureFix(t, dir, true)
		return readString(t, filepath.Join(dir, "Taskfile.yml"))
	}
	for name, tc := range map[string]struct {
		taskfile func(t *testing.T) string // nil writes none
		want     bool
	}{
		"no Taskfile":         {nil, false},
		"no test task":        {func(*testing.T) string { return "version: '3'\ntasks:\n  build:\n    cmds: [go build ./...]\n" }, false},
		"an old scaffold":     {func(t *testing.T) string { return testsCaptureFixture(t, "service") }, true},
		"upgraded":            {upgraded, false},
		"test:report removed": {func(t *testing.T) string { return strings.Replace(upgraded(t), "  test:report:", "  test:summary:", 1) }, true},
		"an edited lane, report": {func(*testing.T) string {
			return "version: '3'\ntasks:\n  test:\n    cmds: [go test ./...]\n  test:report:\n    cmds: [echo]\n"
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.taskfile != nil {
				writeTaskfile(t, dir, "Taskfile.yml", tc.taskfile(t))
			}
			if got := migrationApplies(m, "v1.11.0", dir); got != tc.want {
				t.Errorf("applies = %v, want %v", got, tc.want)
			}
			if migrationApplies(m, testsCaptureRelease, dir) {
				t.Error("applies to a project already at the release")
			}
		})
	}
}

// `forge project upgrade list` offers it to an older project still on the
// old lanes.
func TestUpgradeMigrationTestsCapture_UpgradeListSurfacesIt(t *testing.T) {
	dir := newTestProjectWithVersion(t, "v1.11.0")
	writeTaskfile(t, dir, "Taskfile.yml", testsCaptureFixture(t, "cli"))
	var pending []pendingMigration
	var err error
	withCwd(t, dir, func() { pending, err = computePendingMigrations() })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writePendingMigrationsHuman(&out, pending, len(pending)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{testsCapturePlaybookID, "skill load " + testsCapturePlaybookSkill, "upgrade apply " + testsCapturePlaybookID} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("upgrade list does not show %q:\n%s", want, out.String())
		}
	}
}

package templates

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Every scaffolded project ships two Go test tiers, and they must stay
// different on purpose:
//
//   - `task test:short` is the inner loop an agent runs after each edit:
//     `-short`, cached, no race detector, scoped by `--` args.
//   - `task test` is the full lane CI and "before you finish" run: race
//     detector on, `-count=1`, plus every frontend.
//
// Measured across agent sessions, most test wall-time went to recompiling and
// re-running tests whose result Go's test cache already held — `-count=1` was
// on 63% of runs and `-short` on ~19%. A `test:short` that quietly gained
// `-count=1` or `-race` would cost exactly that time again while still
// looking like the fast tier, which is why the flags are pinned here.

var taskfileTemplatesByKind = []string{"Taskfile.yml.tmpl", "Taskfile.cli.yml.tmpl", "Taskfile.library.yml.tmpl"}

// cliArgsScope is the Task expression every lane uses so `--` args REPLACE
// the default package pattern rather than append to it.
const cliArgsScope = "{{if .CLI_ARGS}}{{.CLI_ARGS}}{{else}}./...{{end}}"

type renderedTask struct {
	Desc string `yaml:"desc"`
	Cmds []any  `yaml:"cmds"`
}

func renderedTasks(t *testing.T, tmpl string) map[string]renderedTask {
	t.Helper()
	var doc struct {
		Tasks map[string]renderedTask `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(renderProject(t, tmpl, projectData()), &doc); err != nil {
		t.Fatalf("%s does not render to valid YAML: %v", tmpl, err)
	}
	return doc.Tasks
}

// goTestCmds returns the shell commands of a task that invoke `go test`, and
// reports whether the task also delegates to another task (`- task: x`).
func goTestCmds(task renderedTask) (cmds []string, delegates bool) {
	for _, c := range task.Cmds {
		switch v := c.(type) {
		case string:
			if strings.Contains(v, "go test") {
				cmds = append(cmds, v)
			}
		case map[string]any:
			if _, ok := v["task"]; ok {
				delegates = true
			}
			if s, ok := v["cmd"].(string); ok && strings.Contains(s, "go test") {
				cmds = append(cmds, s)
			}
		}
	}
	return cmds, delegates
}

func TestTaskfileTemplatesShipShortTier(t *testing.T) {
	t.Parallel()
	for _, tmpl := range taskfileTemplatesByKind {
		t.Run(tmpl, func(t *testing.T) {
			t.Parallel()
			tasks := renderedTasks(t, tmpl)

			short, ok := tasks["test:short"]
			if !ok {
				t.Fatalf("%s renders no `test:short` task — scaffolded projects have no inner-loop tier to run", tmpl)
			}
			if strings.TrimSpace(short.Desc) == "" {
				t.Errorf("test:short has no desc, so `task --list` does not show it")
			}
			cmds, delegates := goTestCmds(short)
			if len(cmds) != 1 {
				t.Fatalf("test:short should run exactly one `go test`, found %d: %q", len(cmds), cmds)
			}
			if delegates {
				t.Errorf("test:short delegates to another task; the inner loop is Go only (frontends keep their own lane)")
			}
			cmd := cmds[0]
			fields := strings.Fields(cmd)
			has := func(flag string) bool {
				for _, f := range fields {
					if f == flag || strings.HasPrefix(f, flag+"=") {
						return true
					}
				}
				return false
			}
			if !has("-short") {
				t.Errorf("test:short does not pass -short: %q", cmd)
			}
			if !has("-timeout") {
				t.Errorf("test:short has no -timeout, so a hung test wedges the inner loop: %q", cmd)
			}
			for _, banned := range []string{"-count", "-race"} {
				if has(banned) {
					t.Errorf("test:short passes %s, which defeats Go's test cache — that flag belongs to `task test` / CI: %q", banned, cmd)
				}
			}
			if strings.Contains(cmd, "GOTESTRACE") {
				t.Errorf("test:short reads GOTESTRACE, which turns the race detector on by default: %q", cmd)
			}
			if !strings.Contains(cmd, cliArgsScope) {
				t.Errorf("test:short is not scoped by `--` args like `test` is (want %q): %q", cliArgsScope, cmd)
			}

			// The full lane keeps the CI flags; the two tiers must not collapse.
			full, ok := tasks["test"]
			if !ok {
				t.Fatalf("%s renders no `test` task", tmpl)
			}
			fullCmds, _ := goTestCmds(full)
			if len(fullCmds) != 1 || !strings.Contains(fullCmds[0], "-count=1") || !strings.Contains(fullCmds[0], "GOTESTRACE") {
				t.Errorf("`task test` should keep -count=1 and the GOTESTRACE race default as the full lane, got %q", fullCmds)
			}
		})
	}
}

// memoryTaskSpan matches a `task <target>` command span in the memory file.
var memoryTaskSpan = regexp.MustCompile("`task ([a-z][a-z0-9:-]*)")

// TestMemoryTemplateTeachesShortTier pins the instruction to the thing it
// names: reliant.md is loaded into every agent session, so it must teach the
// inner-loop tier, and every `task <target>` it tells an agent to run must be
// a real task in the scaffolded service Taskfile.
func TestMemoryTemplateTeachesShortTier(t *testing.T) {
	t.Parallel()
	memory, err := ProjectTemplates().Render("reliant.md.tmpl", struct {
		Name string
		CLI  string
	}{Name: "demo", CLI: "forge"})
	if err != nil {
		t.Fatalf("render reliant.md.tmpl: %v", err)
	}
	body := string(memory)

	if !strings.Contains(body, "`task test:short") {
		t.Errorf("reliant.md.tmpl never tells agents to iterate with `task test:short`")
	}

	tasks := renderedTasks(t, "Taskfile.yml.tmpl")
	for _, m := range memoryTaskSpan.FindAllStringSubmatch(body, -1) {
		if _, ok := tasks[m[1]]; !ok {
			t.Errorf("reliant.md.tmpl tells agents to run `task %s`, which the scaffolded Taskfile.yml does not define", m[1])
		}
	}
}

// execsWithoutShortGate reports whether a scaffolded test template would
// compile into the cached `test:short` lane (no integration/e2e/ignore build
// tag) while exec'ing a child process with no testing.Short() gate.
//
// Go's test cache keys only on the files and env vars the TEST PROCESS reads.
// A child (forge, kcl, go build, npm, git) that reads repo files is invisible
// to it, so editing those files leaves a cached `ok` standing over a test
// that would now fail.
func execsWithoutShortGate(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//go:build") {
			for _, tag := range []string{"integration", "e2e", "ignore"} {
				if strings.Contains(line, tag) {
					return false
				}
			}
		}
	}
	return strings.Contains(src, `"os/exec"`) && !strings.Contains(src, "testing.Short()")
}

func TestExecsWithoutShortGateDetector(t *testing.T) {
	t.Parallel()
	const execImport = "import \"os/exec\"\n"
	for _, tc := range []struct {
		name string
		src  string
		want bool
	}{
		{"untagged exec", "package x\n" + execImport, true},
		{"e2e-tagged exec", "//go:build e2e\n\npackage x\n" + execImport, false},
		{"integration-tagged exec", "//go:build integration\n\npackage x\n" + execImport, false},
		{"short-gated exec", "package x\n" + execImport + "func T(t *testing.T) { if testing.Short() { t.Skip() } }\n", false},
		{"no exec", "package x\nimport \"os\"\n", false},
	} {
		if got := execsWithoutShortGate(tc.src); got != tc.want {
			t.Errorf("%s: execsWithoutShortGate = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestScaffoldedTestsAreCacheCorrect: every test template forge scaffolds
// that lands in the cached `test:short` lane reads its inputs in-process
// (the born CRUD tests read db/migrations with os.ReadFile, or go:embed),
// so a cached pass is a real pass. One that execs a child must be tagged out
// of the lane or gated on testing.Short().
func TestScaffoldedTestsAreCacheCorrect(t *testing.T) {
	t.Parallel()
	var checked int
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".tmpl") || !strings.Contains(d.Name(), "_test") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		checked++
		if execsWithoutShortGate(string(src)) {
			t.Errorf("%s execs a child process in the untagged lane with no testing.Short() gate — "+
				"the cached `task test:short` can report a stale pass when the files that child reads change. "+
				"Read the inputs in the test process, tag it integration/e2e, or skip under -short.", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if checked == 0 {
		t.Fatal("found no scaffolded test templates — the walk has drifted from the template layout")
	}
}

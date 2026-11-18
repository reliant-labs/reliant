package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var taskfileTemplates = []string{"Taskfile.yml.tmpl", "Taskfile.cli.yml.tmpl", "Taskfile.library.yml.tmpl"}

// TestTaskfilesRunThePinnedForge: every forge a scaffolded Taskfile runs is
// {{.FORGE}}, the project's pin — never `forge` off PATH, which is routinely
// another version (a bundled forge rejected reliant's forge.yaml with
// `unknown key "lint.tracing"`). The one exception is the SLOT probe: heavy
// slots are machine-wide, and the probe runs on every `task` invocation.
//
// And no task runs a bare `buf generate`: codegen is `forge generate`, which
// runs the pinned buf and plugins and everything else forge projects.
func TestTaskfilesRunThePinnedForge(t *testing.T) {
	for _, name := range taskfileTemplates {
		t.Run(name, func(t *testing.T) {
			rendered := renderProject(t, name, projectData())
			var doc struct {
				Vars map[string]any `yaml:"vars"`
			}
			if err := yaml.Unmarshal(rendered, &doc); err != nil {
				t.Fatal(err)
			}
			forge, _ := doc.Vars["FORGE"].(string)
			if !strings.Contains(forge, "go run github.com/reliant-labs/forge/cmd/forge@") || !strings.Contains(forge, "forge_version") {
				t.Errorf("FORGE must run forge.yaml's forge_version with go run; got %q", forge)
			}
			slot, _ := doc.Vars["SLOT"].(map[string]any)
			slotSh, _ := slot["sh"].(string)
			for _, snippet := range taskfileShellSnippets(t, rendered) {
				if snippet == slotSh {
					continue
				}
				for _, cmd := range externalCommands(t, snippet) {
					if cmd == "forge" {
						t.Errorf("runs `forge` off PATH; use {{.FORGE}}, the pinned forge:\n%s", snippet)
					}
				}
				if strings.Contains(snippet, "buf generate") {
					t.Errorf("runs a bare `buf generate`; codegen is {{.FORGE}} generate:\n%s", snippet)
				}
			}
		})
	}
}

// TestServiceTaskfileFrontendsAreDiscovered: the frontend lanes loop over
// `forge project frontends` (KCL-declared paths such as web/, plus
// frontends/<name>), not a frontends/* glob or a directory baked in at
// scaffold time.
func TestServiceTaskfileFrontendsAreDiscovered(t *testing.T) {
	rendered := string(renderProject(t, "Taskfile.yml.tmpl", projectData()))
	if strings.Contains(rendered, "in frontends/*/") || strings.Contains(rendered, "FRONTEND_DIR") {
		t.Errorf("the service Taskfile still globs frontends/* or bakes in FRONTEND_DIR")
	}
	if !strings.Contains(rendered, "{{.FORGE}} project frontends") {
		t.Errorf("the service Taskfile never asks forge for the frontend inventory")
	}
}

// TestServiceTaskfileRunsUnderTask runs the rendered service Taskfile with the
// real `task` binary: FORGE resolves to `go run …@<pin>` (or FORGE_BIN), and
// test:frontend runs npm in every directory forge reports — including one
// outside frontends/.
func TestServiceTaskfileRunsUnderTask(t *testing.T) {
	if testing.Short() {
		t.Skip("execs the task binary; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are POSIX shell scripts")
	}
	taskBin, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task is not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), renderProject(t, "Taskfile.yml.tmpl", projectData()), 0o644); err != nil {
		t.Fatal(err)
	}
	const pin = "v0.1.47-0.20261022115939-abcdefabcdef"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\nforge_version: "+pin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marks := filepath.Join(t.TempDir(), "npm.log")
	fake := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The pinned forge reports two frontends, one at a KCL-declared path.
	fake("pinned-forge", `[ "$1 $2" = "project frontends" ] && printf 'npm:frontends/admin\nnpm:web\n'`)
	fake("npm", `echo "$(basename "$PWD") $*" >> `+marks+"\n")
	for _, fe := range []string{"frontends/admin", "web"} {
		if err := os.MkdirAll(filepath.Join(dir, fe), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(taskBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), append([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, env...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("task %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	if out := run(nil, "--dry", "ci"); !strings.Contains(out, "go run github.com/reliant-labs/forge/cmd/forge@"+pin+" ci gate all") {
		t.Errorf("task ci does not run the pinned forge:\n%s", out)
	}

	// SLOT= : no host slot around the fake npm.
	run([]string{"FORGE_BIN=" + filepath.Join(bin, "pinned-forge")}, "test:frontend", "SLOT=")
	got, err := os.ReadFile(marks)
	if err != nil {
		t.Fatalf("npm never ran: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	sort.Strings(lines)
	want := []string{"admin test -- --passWithNoTests", "web test -- --passWithNoTests"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("npm ran in %q, want %q", lines, want)
	}
}

// TestTaskfileRunsTheBridgedForgeWhenLinked: a project bridged to a local forge
// checkout (go.work `use`, `forge project new --link-forge`) pins the newest
// RELEASE its forge descends from, but its Taskfile came from the checkout's
// templates and can call verbs that release lacks (`project frontends`
// against v0.1.46 made a fresh scaffold's `task test` fail). The workspace's
// forge is the one that wrote the project, so that is the one to run.
func TestTaskfileRunsTheBridgedForgeWhenLinked(t *testing.T) {
	if testing.Short() {
		t.Skip("execs the task binary; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fakes are POSIX shell scripts")
	}
	taskBin, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task is not installed")
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not installed")
	}
	const pin = "v0.1.46"
	for _, tc := range []struct {
		name, listOut, want, notWant string
	}{
		{"bridged", `{"Path": "github.com/reliant-labs/forge", "Main": true, "Dir": "/src/forge"}`,
			"go run github.com/reliant-labs/forge/cmd/forge ci gate all", "forge/cmd/forge@"},
		{"not bridged", `{"Path": "github.com/reliant-labs/forge", "Version": "` + pin + `"}`,
			"go run github.com/reliant-labs/forge/cmd/forge@" + pin + " ci gate all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, bin := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "Taskfile.yml"), renderProject(t, "Taskfile.yml.tmpl", projectData()), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: demo\nforge_version: "+pin+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Only `go list -m -json` is faked: the Taskfile asks it whether
			// the forge module is the workspace's main module.
			shim := "#!/bin/sh\nif [ \"$1\" = list ]; then cat <<'JSON'\n" + tc.listOut + "\nJSON\nexit 0; fi\nexec " + realGo + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(shim), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(taskBin, "--dry", "ci")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("task --dry ci: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, out)
			}
			if tc.notWant != "" && strings.Contains(string(out), tc.notWant) {
				t.Errorf("a bridged project must not run the pinned release (%q):\n%s", tc.notWant, out)
			}
		})
	}
}

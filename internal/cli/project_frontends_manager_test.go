package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/templates"
	"gopkg.in/yaml.v3"
)

// Full CLI tests execute rendered Taskfiles, including the full-tier changed
// tests in test-short-warm. Keep its PR cache-replay counterpart provisioned
// identically, and require the tool before any of those test entrypoints.
func TestTaskfileFrontendTestsHaveTaskInCI(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Uses string            `yaml:"uses"`
				If   string            `yaml:"if"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(body, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"test-cli", "test-short-warm", "test-short"} {
		t.Run(name, func(t *testing.T) {
			job, ok := workflow.Jobs[name]
			if !ok {
				t.Fatalf("CI job %s is missing", name)
			}
			installed, tested := false, false
			for _, step := range job.Steps {
				// Any release of the action: dependabot bumps its major, and
				// what this test pins is the Task version it installs.
				if strings.HasPrefix(step.Uses, "arduino/setup-task@") {
					if step.With["version"] != "3.52.0" || step.If != "" {
						t.Fatal("Task must be installed unconditionally at the tested pin 3.52.0")
					}
					installed = true
				}
				if strings.Contains(step.Run, "ci-test-shard.sh") || strings.Contains(step.Run, "ci-changed-tests.sh") || strings.Contains(step.Run, "ci-test-packages.sh cli") || strings.Contains(step.Run, "ci-test-full-shard.sh") {
					tested = true
					if !installed {
						t.Errorf("Task is not installed before %s", step.Run)
					}
				}
			}
			if !tested {
				t.Fatal("CI job has no recognized CLI test entrypoint")
			}
		})
	}
}

func TestTaskfileFrontendTestArguments(t *testing.T) {
	if testing.Short() {
		t.Skip("execs task over a rendered template; runs in full scoped test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX argv-capture fixture")
	}
	task, err := exec.LookPath("task")
	if err != nil {
		t.Fatal(err)
	}
	for _, pm := range []string{"npm", "pnpm", "yarn"} {
		for _, suite := range []string{"present", "empty"} {
			t.Run(pm+"/"+suite, func(t *testing.T) {
				root := t.TempDir()
				if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nmodule_path: example.com/demo\nforge_version: v0.1.46\n"), 0600); err != nil {
					t.Fatal(err)
				}
				bin := filepath.Join(root, "bin")
				if err := os.MkdirAll(filepath.Join(root, "frontends/web"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(bin, 0700); err != nil {
					t.Fatal(err)
				}
				marks := filepath.Join(root, "argv")
				// npm consumes its separator; pnpm/yarn forward it to Vitest.
				// The fixture refuses any argv that would hide the option from
				// Vitest, including empty-suite's --passWithNoTests requirement.
				manager := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + marks + "'\n[ \"$1\" = test ] || exit 11\nshift\n"
				if pm == "npm" {
					manager += "[ \"$1\" = -- ] || exit 12\nshift\n"
				}
				manager += "[ \"$1\" = --passWithNoTests ] && [ \"$#\" = 1 ] || exit 13\n"
				if err := os.WriteFile(filepath.Join(bin, pm), []byte(manager), 0700); err != nil {
					t.Fatal(err)
				}
				forge := filepath.Join(bin, "forge-list")
				if err := os.WriteFile(forge, []byte("#!/bin/sh\nprintf '%s\\n' '"+pm+":frontends/web'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				data, err := templates.ProjectTemplates().Render("Taskfile.yml.tmpl", map[string]any{"ProjectName": "demo", "Name": "demo", "FrontendName": "web", "CLI": "forge"})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "Taskfile.yml"), data, 0600); err != nil {
					t.Fatal(err)
				}
				run := exec.Command(task, "test:frontend", "FORGE="+forge, "SLOT=", "CLI_ARGS=")
				run.Dir = root
				run.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				if output, err := run.CombinedOutput(); err != nil {
					t.Fatalf("test entrypoint: %v\n%s", err, output)
				}
				got, err := os.ReadFile(marks)
				if err != nil {
					t.Fatal(err)
				}
				want := "test\n--passWithNoTests\n"
				if pm == "npm" {
					want = "test\n--\n--passWithNoTests\n"
				}
				if string(got) != want {
					t.Errorf("manager argv %q, want %q", got, want)
				}
			})
		}
	}
}

// The rendered Taskfile must consume exactly the CI resolver's decision,
// including migrations that temporarily leave multiple managers' locks.
func TestProjectFrontendsManagerTaskfileParity(t *testing.T) {
	if testing.Short() {
		t.Skip("execs task over rendered Taskfile; runs in full scoped test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake package managers")
	}
	task, err := exec.LookPath("task")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nmodule_path: example.com/demo\nforge_version: v0.1.46\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var want []string
	for _, pm := range []string{"npm", "pnpm", "yarn"} {
		for _, locks := range []bool{false, true} {
			name := pm + "-empty"
			if locks {
				name = pm + "-mixed"
			}
			dir := filepath.Join(root, "frontends", name)
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "next.config.ts"), []byte("export default {};\n"), 0600); err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]any{"name": name, "packageManager": pm + "@1.0.0"})
			if err := os.WriteFile(filepath.Join(dir, "package.json"), body, 0600); err != nil {
				t.Fatal(err)
			}
			if locks {
				for _, f := range []string{"package-lock.json", "pnpm-lock.yaml", "yarn.lock"} {
					if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if got := frontenddeps.PackageManager(dir, ""); got != pm {
				t.Fatalf("%s resolver=%s", name, got)
			}
			want = append(want, pm+":"+filepath.ToSlash(filepath.Join("frontends", name)))
		}
	}
	cmd := newProjectFrontendsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--with-package-manager"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	store, err := loadProjectStore()
	if err != nil {
		t.Fatal(err)
	}
	for _, fe := range ciFrontends(root, store.Config()) {
		if !strings.Contains(out.String(), fe.PM()+":"+fe.Path+"\n") {
			t.Fatalf("CI and Taskfile metadata disagree for %+v", fe)
		}
	}
	rows := strings.Fields(out.String())
	if len(rows) != len(want) {
		t.Fatalf("rows=%v want=%v", rows, want)
	}
	for _, row := range want {
		if !strings.Contains(out.String(), row+"\n") {
			t.Fatalf("missing %s in %s", row, out.String())
		}
	}
	// Freeze only the discovery result in a fake forge; decisions above were
	// made by the real command. A Taskfile-side lockfile algorithm would pick
	// pnpm for every mixed case and fail the recording assertions below.
	bin := t.TempDir()
	listing := filepath.Join(bin, "listing")
	if err := os.WriteFile(listing, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	fakeForge := filepath.Join(bin, "forge-list")
	if err := os.WriteFile(fakeForge, []byte("#!/bin/sh\ncase \"$*\" in 'project frontends --with-package-manager') cat '"+listing+"';; *) exit 91;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	marks := filepath.Join(root, "marks")
	for _, pm := range []string{"npm", "pnpm", "yarn"} {
		if err := os.WriteFile(filepath.Join(bin, pm), []byte("#!/bin/sh\nprintf '%s:%s\\n' '"+pm+"' \"${PWD##*/}\" >> '"+marks+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := templates.ProjectTemplates().Render("Taskfile.yml.tmpl", map[string]any{"ProjectName": "demo", "Name": "demo", "FrontendName": "web", "CLI": "forge"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Taskfile.yml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(task, "deps:frontend", "FORGE="+fakeForge)
	run.Dir = root
	run.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("task: %v\n%s", err, output)
	}
	got, err := os.ReadFile(marks)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range want {
		pm, path, _ := strings.Cut(row, ":")
		if !strings.Contains(string(got), pm+":"+filepath.Base(path)+"\n") {
			t.Errorf("missing %s in executed managers %s", row, got)
		}
	}
}

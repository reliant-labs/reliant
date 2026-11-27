package ciguard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

// Read the real workflow in the test process: edits invalidate Go's test cache,
// and the exercised scripts cannot drift from the scripts Actions executes.
func mainBisectScript(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "main-bisect.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	for _, s := range wf.Jobs["judge"].Steps {
		if s.Name == name && s.Run != "" {
			return s.Run
		}
	}
	t.Fatalf("missing script %q", name)
	return ""
}

func runMainBisectScript(t *testing.T, script, dir string, env map[string]string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script)
	cmd.Dir = dir
	// Do not inherit dispatch-like variables or shell startup hooks from the host.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "LC_ALL=C"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	if e, ok := err.(*exec.ExitError); ok {
		return string(out), e.ExitCode()
	}
	t.Fatalf("start bash: %v", err)
	return "", -1
}

func TestMainBisectValidation(t *testing.T) {
	script := mainBisectScript(t, "Validate the inputs")
	cases := []struct {
		name, key, value string
		valid            bool
	}{
		{"defaults", "RUN", "", true},
		{"root", "PKGS", ".", true},
		{"packages", "PKGS", "internal/pathglob pkg/foo-bar/", true},
		{"max packages", "PKGS", strings.TrimSpace(strings.Repeat("pkg/a ", 20)), true},
		{"max tests", "RUN", strings.TrimSuffix(strings.Repeat("TestA|", 50), "|"), true},
		{"test kinds", "RUN", "TestA|ExampleB|FuzzC|BenchmarkD", true},
		{"tags", "TAGS", "e2e,integration", true},
		{"windows", "OS", "windows-latest", true},
		{"no race", "RACE", "false", true},
		{"short sha", "SHA", "abc", false},
		{"uppercase sha", "SHA", strings.Repeat("A", 40), false},
		{"sha injection", "SHA", "$(touch injected)", false},
		{"empty token", "TOKEN", "", false},
		{"long token", "TOKEN", strings.Repeat("a", 41), false},
		{"token injection", "TOKEN", "x; touch injected", false},
		{"empty packages", "PKGS", "", false},
		{"too many packages", "PKGS", strings.Repeat("pkg/a ", 21), false},
		{"absolute package", "PKGS", "/tmp/a", false},
		{"package option", "PKGS", "-exec=evil", false},
		{"parent package", "PKGS", "a/../b", false},
		{"parent at end", "PKGS", "a/..", false},
		{"parent at start", "PKGS", "../a", false},
		{"glob package", "PKGS", "pkg/*", false},
		{"package injection", "PKGS", "a;touch injected", false},
		{"too many tests", "RUN", strings.TrimSuffix(strings.Repeat("TestA|", 51), "|"), false},
		{"leading empty test", "RUN", "|TestA", false},
		{"middle empty test", "RUN", "TestA||TestB", false},
		{"trailing empty test", "RUN", "TestA|", false},
		{"regex test", "RUN", "Test.*", false},
		{"subtest", "RUN", "TestA/sub", false},
		{"test injection", "RUN", "TestA$(touch injected)", false},
		{"invalid tags", "TAGS", "e2e -exec=evil", false},
		{"invalid os", "OS", "macos-latest", false},
		{"invalid race", "RACE", "yes", false},
	}
	dir := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"SHA": strings.Repeat("a", 40), "TOKEN": "bisect-123", "PKGS": "internal/pathglob", "RUN": "TestMatch", "TAGS": "", "OS": "ubuntu-latest", "RACE": "true"}
			env[tc.key] = tc.value
			out, code := runMainBisectScript(t, script, dir, env)
			if tc.valid {
				if code != 0 || !strings.Contains(out, "MAIN_BISECT=valid ") {
					t.Fatalf("valid input: exit=%d output=%s", code, out)
				}
			} else if code != 1 || !strings.Contains(out, "MAIN_BISECT=invalid ") || strings.Contains(out, "MAIN_BISECT=valid ") {
				t.Fatalf("invalid input: exit=%d output=%s", code, out)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatalf("input executed as shell: %v", err)
	}
}

func TestMainBisectJudgement(t *testing.T) {
	script := mainBisectScript(t, "Go tests")
	cases := []struct {
		name, runner, race, tags, run string
		code                          int
		args                          []string
		cgo, tools                    string
	}{
		{"linux race success", "Linux", "true", "e2e,integration", "TestA|ExampleB", 0, []string{"-race", "-tags", "e2e,integration", "-run", "^(TestA|ExampleB)$"}, "", ""},
		{"linux failure", "Linux", "false", "", "", 1, nil, "", ""},
		{"build failure", "Linux", "false", "", "TestA", 2, []string{"-run", "^(TestA)$"}, "", ""},
		{"windows ignores race", "Windows", "true", "integration", "TestA", 0, []string{"-tags", "integration", "-run", "^(TestA)$"}, "0", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			candidate := filepath.Join(dir, "candidate")
			for _, p := range []string{bin, candidate} {
				if err := os.Mkdir(p, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			// Capture one argument per line, plus the environment and cwd identity,
			// then return a controlled Go exit status. Compare directories in Bash:
			// Git Bash's $PWD and Go's Windows paths have different spellings.
			// No compiler, network or candidate code runs.
			stub := "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$CAPTURE\"\ncwd_matches=false\nif [[ . -ef \"$EXPECTED_CWD\" ]]; then cwd_matches=true; fi\nprintf '%s\\n' \"$cwd_matches\" \"${CGO_ENABLED-}\" \"${FORGE_E2E_REQUIRE_TOOLS-}\" > \"$DETAILS\"\necho 'candidate output'\nexit \"$GO_EXIT\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			capture, details := filepath.Join(dir, "args"), filepath.Join(dir, "details")
			env := map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "CAPTURE": capture, "DETAILS": details, "EXPECTED_CWD": filepath.ToSlash(candidate), "GO_EXIT": strconv.Itoa(tc.code), "RUNNER_OS": tc.runner, "RACE": tc.race, "TAGS": tc.tags, "RUN": tc.run, "PKGS": ". internal/pathglob/ pkg/a"}
			out, code := runMainBisectScript(t, script, candidate, env)
			marker := fmt.Sprintf("MAIN_BISECT_EXIT=%d\n", tc.code)
			if code != tc.code || strings.Count(out, "MAIN_BISECT_EXIT=") != 1 || !strings.HasSuffix(out, marker) || !strings.Contains(out, "candidate output\n") {
				t.Fatalf("exit=%d output=%s; want exit=%d and marker %q", code, out, tc.code, marker)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string{"test", "-count=1", "-timeout=25m"}, tc.args...)
			want = append(want, "./", "./internal/pathglob/", "./pkg/a/")
			got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("go args=%q; want %q", got, want)
			}
			raw, err = os.ReadFile(details)
			if err != nil {
				t.Fatal(err)
			}
			if want := "true\n" + tc.cgo + "\n" + tc.tools + "\n"; string(raw) != want {
				t.Fatalf("cwd/environment=%q; want %q", raw, want)
			}
		})
	}
}

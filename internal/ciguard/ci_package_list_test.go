package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func packageListStep(t *testing.T, job, step string) string {
	t.Helper()
	body, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var ci struct {
		Jobs map[string]struct {
			Steps []struct{ Name, Run string }
		}
	}
	if err := yaml.Unmarshal(body, &ci); err != nil {
		t.Fatal(err)
	}
	for _, s := range ci.Jobs[job].Steps {
		if s.Name == step {
			if s.Run == "" {
				t.Fatalf("%s / %s has no shell commands", job, step)
			}
			return s.Run
		}
	}
	t.Fatalf("%s has no %s step", job, step)
	return ""
}

func TestCIPackageListPreservesArgumentsAndFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("executes the workflow steps in Bash against package-list fixtures; runs in task test")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("needs Bash: %v", err)
	}
	for _, job := range []struct{ key, step, flags, selector string }{
		{"test-short-warm", "Go tests (guards)", "test\n-p\n1\n-count=1\n-timeout\n30m\n", "guards"},
		{"windows", "Go tests (-short)", "test\n-short\n-timeout\n30m\n", "unit cli"},
		{"windows-changed", "Go tests (-short, changed packages)", "test\n-short\n-timeout\n17m\n", ""},
	} {
		for _, mode := range []string{"packages", "empty", "failed"} {
			t.Run(job.key+"/"+mode, func(t *testing.T) {
				run := packageListStep(t, job.key, job.step)
				dir := t.TempDir()
				if err := os.Mkdir(filepath.Join(dir, "scripts"), 0o700); err != nil {
					t.Fatal(err)
				}
				write := func(name, content string, perm os.FileMode) {
					t.Helper()
					if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), perm); err != nil {
						t.Fatal(err)
					}
				}
				// One import path per line, in the selector's original order.
				const packages = "example.test/z\nexample.test/a[ab]\n"
				write("go", "#!/usr/bin/env bash\nprintf '%s\\n' \"$@\"\n", 0o700)
				write("scripts/ci-test-packages.sh", `#!/usr/bin/env bash
[[ $* == "$SELECTOR" ]] || exit 8
case "$MODE" in
  empty) exit 0 ;;
  failed) printf '%s\n' 'example.test/partial'; exit 9 ;;
  *) printf '%s' "$PACKAGES" ;;
esac
`, 0o700)
				list := "test list.txt"
				if job.selector == "" && mode != "failed" {
					content := packages
					if mode == "empty" {
						content = ""
					}
					write(list, content, 0o600)
				}
				if err := os.Mkdir(filepath.Join(dir, "example.test"), 0o700); err != nil {
					t.Fatal(err)
				}
				write("example.test/aa", "", 0o600) // glob would silently change the package argument
				cmd := exec.Command(bash, "-eo", "pipefail", "-c", run)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
					"PACKAGE_LIST="+list, "SELECTOR="+job.selector, "PACKAGES="+packages, "MODE="+mode)
				out, err := cmd.CombinedOutput()
				if mode == "packages" {
					if err != nil || string(out) != job.flags+packages {
						t.Fatalf("want exact ordered go arguments %q; got %v\n%s", job.flags+packages, err, out)
					}
				} else if err == nil || strings.HasPrefix(string(out), "test\n") {
					t.Fatalf("bad package list must fail before invoking go: %v\n%s", err, out)
				}
			})
		}
	}
}

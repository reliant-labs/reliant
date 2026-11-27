package templates

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestServiceTaskfileFrontendLaneReachesVitest runs the rendered `task
// test:frontend` with the real task, npm and pnpm, a fake forge and a fake
// vitest that records its argv. It proves what the frontend lane's recording
// rests on: the reporter and coverage flags CAPTURE appends after `$PM test`
// travel through the package manager into the `vitest run` script, so the
// JSON report capture copies and the coverage it scans are written; and the
// command capture runs (after its own `--`) is the package manager's, not
// vitest's. npm needs the `--` the lane puts before --passWithNoTests; pnpm
// forwards without one. A frontend with no vitest runs unrecorded.
func TestServiceTaskfileFrontendLaneReachesVitest(t *testing.T) {
	if testing.Short() {
		t.Skip("execs task, npm and pnpm; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the fake forge and vitest are POSIX shell scripts")
	}
	for _, tool := range []string{"task", "npm", "pnpm", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	run := filepath.Join(dir, "run")
	for _, d := range []string{bin, run} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "Taskfile.yml"), string(renderProject(t, "Taskfile.yml.tmpl", projectData())), 0o644)
	write(filepath.Join(dir, "forge.yaml"), "name: demo\n", 0o644)
	// The fake forge: no slots, three frontends, one run, and a capture that
	// logs the command it was handed and then runs it, as capture does.
	write(filepath.Join(bin, "forge"), `#!/bin/sh
case "$1 $2" in
"project frontends") echo "npm:web-npm pnpm:web-pnpm npm:plain" ;;
"test begin") echo "`+run+`" ;;
"test capture")
  shift 2
  while [ "$1" != "--" ]; do shift; done
  shift
  printf '%s\n' "$*" >> "`+filepath.Join(dir, "captured")+`"
  exec "$@" ;;
*) exit 2 ;;
esac
`, 0o755)
	vitest := `#!/bin/sh
printf '%s\n' "$*" > "$(dirname "$0")/../../argv"
`
	pkg := `{"name":"x","private":true,"scripts":{"test":"vitest run"}}`
	for _, fe := range []string{"web-npm", "web-pnpm"} {
		write(filepath.Join(dir, fe, "package.json"), pkg, 0o644)
		write(filepath.Join(dir, fe, "node_modules/.bin/vitest"), vitest, 0o755)
		write(filepath.Join(dir, fe, "node_modules/@vitest/coverage-v8/package.json"), `{"name":"@vitest/coverage-v8"}`, 0o644)
	}
	// No vitest installed: runs, unrecorded.
	write(filepath.Join(dir, "plain", "package.json"), `{"name":"plain","private":true,"scripts":{"test":"echo plain-ran"}}`, 0o644)

	cmd := exec.Command("task", "test:frontend")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FORGE_BIN="+filepath.Join(bin, "forge"), "npm_config_update_notifier=false")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("task test:frontend: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "plain-ran") {
		t.Errorf("the frontend without vitest did not run:\n%s", out)
	}

	captured, err := os.ReadFile(filepath.Join(dir, "captured"))
	if err != nil {
		t.Fatalf("no frontend lane went through forge test capture: %v\n%s", err, out)
	}
	if got := strings.Count(string(captured), "\n"); got != 2 {
		t.Errorf("forge test capture ran %d commands, want one per vitest frontend (2):\n%s", got, captured)
	}
	for _, fe := range []struct{ dir, pm string }{{"web-npm", "npm"}, {"web-pnpm", "pnpm"}} {
		lane := "frontend-" + fe.dir
		if !strings.Contains(string(captured), fe.pm+" test ") {
			t.Errorf("%s: capture was not handed `%s test …`:\n%s", fe.dir, fe.pm, captured)
		}
		argv, err := os.ReadFile(filepath.Join(dir, fe.dir, "argv"))
		if err != nil {
			t.Errorf("%s: vitest never ran: %v\n%s", fe.dir, err, out)
			continue
		}
		got := strings.Fields(string(argv))
		for _, want := range []string{
			"run",
			"--passWithNoTests",
			"--reporter=default",
			"--reporter=json",
			"--outputFile.json=" + filepath.Join(run, "."+lane+"-vitest.json"),
			"--coverage.enabled",
			"--coverage.provider=v8",
			"--coverage.clean=false",
			"--coverage.reporter=json",
			"--coverage.reportsDirectory=" + filepath.Join(run, lane),
		} {
			if !containsArg(got, want) {
				t.Errorf("%s (%s): vitest did not receive %q; it got %q", fe.dir, fe.pm, want, got)
			}
		}
		if containsArg(got, "--") {
			t.Errorf("%s (%s): vitest received a literal --, so the flags after it would be positional: %q", fe.dir, fe.pm, got)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

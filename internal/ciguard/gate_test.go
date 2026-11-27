package ciguard

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Copy the scripts with ReadFile, so Go's test cache sees their real inputs.
// All subprocesses below use fake tools and temp-dir inputs, never real builds.
func gateFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux gate uses bash and Unix tools")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash is not installed")
	}
	root := t.TempDir()
	for _, dir := range []string{"scripts", "bin", "logs"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"gate.sh", "ci-e2e-scaffold.sh"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		fixtureFile(t, root, "scripts/"+name, string(data))
	}
	fixtureFile(t, root, "bin/git", "#!/usr/bin/env bash\ncase $* in *--show-toplevel*) echo \"$FIXTURE_ROOT\";; *) echo fixture;; esac\n")
	fixtureFile(t, root, "scripts/ci-test-packages.sh", "#!/usr/bin/env bash\necho ./fixture/$1\n")
	return root
}

func fixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func runGateFixture(t *testing.T, root, script string, extra ...string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts", script))
	cmd.Dir = root
	// The timeout kills bash only. A lane it started holds the output pipe
	// open, and without a WaitDelay CombinedOutput waits on it for as long as
	// the package's own timeout.
	cmd.WaitDelay = time.Second
	// Do not let the developer's gate/scaffold settings influence the fixture.
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if key != "PATH" && key != "GOFLAGS" && key != "CI" && !strings.HasPrefix(key, "GATE_") && !strings.HasPrefix(key, "SCAFFOLD_") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"), "FIXTURE_ROOT="+root, "GOFLAGS=", "GATE_LOG_DIR="+filepath.Join(root, "logs"), "SCAFFOLD_DIR="+filepath.Join(root, "work"))
	cmd.Env = append(cmd.Env, extra...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("fixture timed out: %s", out)
	}
	if err == nil {
		return 0, string(out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return exit.ExitCode(), string(out)
}

// gateGoStub stands in for go. gate.sh asks `go env GOFLAGS` before it starts
// a lane; that answers empty and is never a lane (under BARRIER it would be a
// sixth arrival the barrier never releases).
const gateGoStub = `#!/usr/bin/env bash
[[ $1 == env ]] && exit 0
lane=${*: -1}
lane=${lane##*/}
[[ $* == *'-tags e2e'* ]] && lane=e2e
if [[ ${SERIAL:-0} == 1 ]]; then
  mkdir "$FIXTURE_ROOT/active" || exit 1
  trap 'rmdir "$FIXTURE_ROOT/active"' EXIT
fi
if [[ ${BARRIER:-0} == 1 ]]; then
  touch "$FIXTURE_ROOT/$lane.started"
  while :; do
    files=("$FIXTURE_ROOT/"*.started)
    ((${#files[@]} == 5)) && break
    sleep 0.01
  done
fi
case ${FAILURE:-} in
  mixed) echo '--- FAIL: TestAssertion'; echo 'expected true, got false'; echo 'no docker daemon answers'; exit 1;;
  host-text) echo 'no docker daemon answers'; exit 1;;
  ordinary) echo 'assertion failed'; exit 1;;
esac
`

func prepareGate(t *testing.T, root string) {
	t.Helper()
	fixtureFile(t, root, "bin/go", gateGoStub)
	fixtureFile(t, root, "scripts/ci-e2e-scaffold.sh", `#!/usr/bin/env bash
if [[ ${SERIAL:-0} == 1 ]]; then
  mkdir "$FIXTURE_ROOT/active" || exit 1
  trap 'rmdir "$FIXTURE_ROOT/active"' EXIT
fi
if [[ ${BARRIER:-0} == 1 ]]; then touch "$FIXTURE_ROOT/scaffold.started"; fi
case ${SCAFFOLD_RESULT:-pass} in
  host) echo 'required tool(s) not on PATH: buf (E2E Scaffold needs go, node, pnpm and buf)'; exit 2;;
  mixed) echo 'assertion failed'; echo 'required tool(s) not on PATH: buf (E2E Scaffold needs go, node, pnpm and buf)'; exit 2;;
  fail) echo 'build failed'; exit 1;;
esac
`)
}

func TestGateReceiptsAreInvocationScoped(t *testing.T) {
	root := gateFixture(t)
	prepareGate(t, root)
	if code, out := runGateFixture(t, root, "gate.sh"); code != 0 {
		t.Fatalf("full run: exit %d: %s", code, out)
	}
	code, out := runGateFixture(t, root, "gate.sh", "GATE_LANES=unit")
	if code != 2 || strings.Count(out, "NOT RUN") != 4 {
		t.Fatalf("narrowed run reused old receipts: exit %d: %s", code, out)
	}
}

func TestGateAggregatesResults(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		code int
	}{
		{"pass", nil, 0},
		{"omitted", []string{"GATE_LANES=unit"}, 2},
		{"assertion", []string{"FAILURE=ordinary"}, 1},
		{"mixed-host-and-assertion", []string{"FAILURE=mixed"}, 1},
		{"ambiguous-host-text", []string{"FAILURE=host-text"}, 1},
		{"explicit-preflight", []string{"SCAFFOLD_RESULT=host"}, 2},
		{"failure-wins-over-unproven", []string{"FAILURE=ordinary", "SCAFFOLD_RESULT=host"}, 1},
		{"scaffold-mixed-exit-two", []string{"SCAFFOLD_RESULT=mixed"}, 1},
		{"scaffold-failure", []string{"SCAFFOLD_RESULT=fail"}, 1},
		{"parallel", []string{"BARRIER=1", "GATE_JOBS=5"}, 0},
		{"serial-limit", []string{"SERIAL=1", "GATE_JOBS=1"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := gateFixture(t)
			prepareGate(t, root)
			code, out := runGateFixture(t, root, "gate.sh", tc.env...)
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, out)
			}
		})
	}
}

func TestScaffoldStopsOnFailedStep(t *testing.T) {
	for _, step := range []string{"install", "tools", "overlay"} {
		t.Run(step, func(t *testing.T) {
			root := gateFixture(t)
			for _, tool := range []string{"node", "pnpm", "buf"} {
				fixtureFile(t, root, "bin/"+tool, "#!/usr/bin/env bash\nexit 0\n")
			}
			fixtureFile(t, root, "bin/forge", `#!/usr/bin/env bash
if [[ $* == '--version' ]]; then exit 0; fi
echo "forge $*" >>"$FIXTURE_ROOT/calls"
[[ ${FAIL_STEP} != tools ]]
`)
			fixtureFile(t, root, "bin/go", `#!/usr/bin/env bash
echo "go $*" >>"$FIXTURE_ROOT/calls"
case $1 in
  install) [[ ${FAIL_STEP} != install ]];;
  build) exit 1;;
  *) echo 'unexpected go command'; exit 99;;
esac
`)
			code, out := runGateFixture(t, root, "ci-e2e-scaffold.sh", "FAIL_STEP="+step)
			if code != 1 {
				t.Fatalf("failed %s step: exit %d: %s", step, code, out)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			want := "go install ./cmd/forge ./cmd/contractlint\n"
			if step != "install" {
				want += "forge tools install\n"
			}
			if step == "overlay" {
				want += "go build -o " + filepath.Join(root, "work/npm-overlay") + " ./internal/npmoverlay/cmd/npm-overlay\n"
			}
			if string(calls) != want {
				t.Fatalf("steps continued after failure: got %q, want %q", calls, want)
			}
		})
	}
}

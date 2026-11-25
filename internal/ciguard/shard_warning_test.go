package ciguard

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestShardSurfacesPassingTestWarnings pins scripts/ci-test-shard.sh's
// warning pass: a PASSING test that logs a titled workflow command
// (`::warning title=…::…`, e.g. internal/templates' skill-headroom guard) has
// that line echoed as an annotation, while the rest of a passing test's
// output stays dropped, and the shard still exits with the test command's
// status.
func TestShardSurfacesPassingTestWarnings(t *testing.T) {
	testkit.Slow(t, "execs bash")
	const warning = "::warning title=Skill headroom::deploy is 851 of 1000 bytes (85%, over the 85% headroom line): split a separable section into a sub-skill before it reaches the cap"
	const noise = "ordinary passing output that must stay suppressed"
	for _, tc := range []struct {
		name     string
		goStatus int
	}{
		{"passing run", 0},
		{"failing run", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, status := runShardFixture(t, tc.goStatus, []map[string]any{
				// t.Log indents and prefixes the line with file:line; logged twice
				// (once per subtest, say) it must be annotated once.
				{"Action": "output", "Package": "fixture/package", "Test": "TestHeadroom", "Output": "    skills_size_test.go:133: " + warning + "\n"},
				{"Action": "output", "Package": "fixture/package", "Test": "TestHeadroom", "Output": "    skills_size_test.go:133: " + warning + "\n"},
				{"Action": "output", "Package": "fixture/package", "Test": "TestHeadroom", "Output": "    skills_size_test.go:140: " + noise + "\n"},
				{"Action": "pass", "Package": "fixture/package", "Test": "TestHeadroom", "Elapsed": 0.01},
				{"Action": "pass", "Package": "fixture/package", "Elapsed": 0.01},
			})
			if status != tc.goStatus {
				t.Fatalf("shard exited %d, want the test command's %d:\n%s", status, tc.goStatus, out)
			}
			var hits int
			for _, line := range strings.Split(out, "\n") {
				// Windows' bash ends a line with \r\n.
				if strings.TrimSuffix(line, "\r") == warning {
					hits++
				}
			}
			if hits != 1 {
				t.Fatalf("want the exact workflow-command line once in stdout, got %d:\n%s", hits, out)
			}
			if strings.Contains(out, noise) {
				t.Fatalf("a passing test's ordinary output must stay suppressed:\n%s", out)
			}
		})
	}
}

// runShardFixture runs scripts/ci-test-shard.sh against a stub `go` that
// prints events as its -json stream and exits goStatus. It returns the
// shard's stdout and exit status.
func runShardFixture(t *testing.T, goStatus int, events []map[string]any) (string, int) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-test-shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-go-test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var stream strings.Builder
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(b)
		stream.WriteByte('\n')
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"shard.sh":                    string(body),
		"scripts/ci-go-test.sh":       string(helper),
		"scripts/ci-test-packages.sh": "#!/usr/bin/env bash\necho fixture/package\n",
		"events.in":                   stream.String(),
		"go":                          "#!/usr/bin/env bash\ncat \"$(dirname \"$0\")/events.in\"\nexit " + strconv.Itoa(goStatus) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "shard.sh", "0", "1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"), installStubForge(t, root))
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exitErr):
		return string(out), exitErr.ExitCode()
	default:
		t.Fatalf("shard: %v", err)
		return "", -1
	}
}

// TestShardRecordsThroughCapture pins how the shard records its run: the
// test command goes through `forge test capture --lane unit --render raw`
// (so $CI_TEST_JSON is still the raw stream), into the run `forge test
// begin` names, with the lane's coverage written as an atomic -coverprofile
// in the lane directory, and through scripts/ci-go-test.sh, which keeps
// capture's per-run FORGE_TEST_* variables out of Go's test cache key.
func TestShardRecordsThroughCapture(t *testing.T) {
	testkit.Slow(t, "execs bash")
	body := readText(t, filepath.Join("..", "..", "scripts", "ci-test-shard.sh"))
	helper := readText(t, filepath.Join("..", "..", "scripts", "ci-go-test.sh"))
	root := t.TempDir()
	run := filepath.Join(root, "run")
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"shard.sh":                    body,
		"scripts/ci-go-test.sh":       helper,
		"scripts/ci-test-packages.sh": "#!/usr/bin/env bash\necho fixture/package\n",
		// The stub go records its arguments and whether a FORGE_TEST_*
		// variable reached it, then passes.
		"go": `#!/usr/bin/env bash
echo "$*" >"$(dirname "$0")/go-args"
env | grep '^FORGE_TEST_' >"$(dirname "$0")/go-env"
printf '%s\n' '{"Action":"pass","Package":"fixture/package","Test":"TestCheap","Elapsed":0.01}' '{"Action":"pass","Package":"fixture/package","Elapsed":0.01}'
`,
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "forge-log")
	cmd := exec.Command("bash", "shard.sh", "0", "1")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"),
		installStubForge(t, root), "FORGE_LOG="+log, "STUB_RUN="+run,
		// What capture exports to the command it records.
		"FORGE_TEST_RUN="+run, "FORGE_TEST_LANE=unit", "FORGE_TEST_LANE_DIR="+filepath.Join(run, "unit"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shard: %v\n%s", err, out)
	}
	calls := readText(t, log)
	if !strings.Contains(calls, "test begin\n") || !strings.Contains(calls, "test capture --run "+run+" --lane unit --render raw -- scripts/ci-go-test.sh ") {
		t.Errorf("the shard must begin a run and record through capture, got:\n%s", calls)
	}
	args := readText(t, filepath.Join(root, "go-args"))
	for _, want := range []string{"test -short -json -covermode=atomic -coverprofile=" + filepath.Join(run, "unit", "cover.out") + " ", "fixture/package"} {
		if !strings.Contains(args, want) {
			t.Errorf("go test lacks %q: %s", want, args)
		}
	}
	if env := readText(t, filepath.Join(root, "go-env")); env != "" {
		t.Errorf("capture's FORGE_TEST_* variables reached cmd/go, where they key the test cache on a new run every time:\n%s", env)
	}
	if fi, err := os.Stat(filepath.Join(run, "unit")); err != nil || !fi.IsDir() {
		t.Errorf("the lane directory -coverprofile writes into must exist before go test runs: %v", err)
	}
	if events := readText(t, filepath.Join(root, "events.json")); !strings.Contains(events, `"Test":"TestCheap"`) {
		t.Errorf("$CI_TEST_JSON must hold the raw stream: %s", events)
	}
}

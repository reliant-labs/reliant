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
)

// TestShardSurfacesPassingTestWarnings pins scripts/ci-test-shard.sh's
// warning pass: a PASSING test that logs a titled workflow command
// (`::warning title=…::…`, e.g. internal/templates' skill-headroom guard) has
// that line echoed as an annotation, while the rest of a passing test's
// output stays dropped, and the shard still exits with the test command's
// status.
func TestShardSurfacesPassingTestWarnings(t *testing.T) {
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
				if line == warning {
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
	cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "CI_TEST_JSON="+filepath.Join(root, "events.json"), "GITHUB_STEP_SUMMARY="+filepath.Join(root, "summary"))
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

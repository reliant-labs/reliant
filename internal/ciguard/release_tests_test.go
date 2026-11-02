package ciguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseRequiresExactFullGreen(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "check-release-tests.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, run, jobs string
		wantPass        bool
	}{
		{"green", "123|completed|success|exact", "success", true},
		{"missing", "missing", "success", false},
		// Still running (the CI gate job queued for a runner) but the three
		// full Test jobs are done and green: releasable.
		{"running-tests-green", "123|in_progress||exact", "success", true},
		{"running-tests-not-done", "123|in_progress||exact", "failure", false},
		// A red Windows or Lint job fails the run; the verdict is the three
		// full Test jobs, so that run is releasable when they are green.
		{"failed-elsewhere", "123|completed|failure|exact", "success", true},
		{"failed-tests", "123|completed|failure|exact", "failure", false},
		{"cancelled", "123|completed|cancelled|exact", "success", false},
		{"other-sha", "123|completed|success|older", "success", false},
		{"waived-tests", "123|completed|success|exact", "failure", false},
		{"api-error", "api-error", "success", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for name, content := range map[string]string{
				"check.sh": string(body),
				"gh": `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$CALLS"
if [[ "$FIXTURE_RUN" == api-error ]]; then exit 1; fi
if [[ "$2" == list ]]; then echo "$FIXTURE_RUN"; else echo "$FIXTURE_JOBS"; fi
`,
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", filepath.Join(root, "check.sh"), "owner/repo", "exact")
			cmd.Env = append(os.Environ(), "FORGE_RELEASE_GH="+filepath.Join(root, "gh"), "FIXTURE_RUN="+tc.run, "FIXTURE_JOBS="+tc.jobs, "CALLS="+filepath.Join(root, "calls"))
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantPass {
				t.Fatalf("pass=%v want %v: %v\n%s", err == nil, tc.wantPass, err, out)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"--commit exact", "--workflow ci.yml", `.event == "push" or .event == "workflow_dispatch"`} {
				if !strings.Contains(string(calls), want) {
					t.Errorf("query lost %q: %s", want, calls)
				}
			}
		})
	}
}

func TestReleaseChecksFullTestsBeforeEditing(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release-forge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	check := strings.Index(string(body), "bash \"$RELEASE_SCRIPT_DIR/check-release-tests.sh\"")
	edit := strings.Index(string(body), "# ── 7. Edit the release files")
	if check < 0 || edit < 0 || check > edit {
		t.Fatal("release must prove full tests green before editing/tagging/pushing")
	}
}

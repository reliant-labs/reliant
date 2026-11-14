package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestMergeGateSkipLint runs merge-gate.sh's own flag parsing, diff base, tier
// marker and phase launch, with `phase` stubbed to record what it would run,
// over a throwaway branch with two commits and a merge of main between them:
// the shape of a PR branch that control-plane's pr-fix gates.
//
// --skip-lint must drop vet and the scoped forge lint and nothing else: the
// compat checks keep the merge-base with origin/main and CI_QUEUE_ENTRY=0.
// --queue sets CI_QUEUE_ENTRY=1, under which check-compat.sh demands a
// single-parent HEAD and judges HEAD^..HEAD, which is wrong on such a branch
// (control-plane#2249's review); its behaviour must not change. --queue drops
// vet but keeps the scoped forge lint, --optional-deps-guard among its lanes:
// it once implied --skip-lint, and #2696 and #2662 turned main red on findings
// that lint names.
func TestMergeGateSkipLint(t *testing.T) {
	testkit.Slow(t, "runs merge-gate.sh's phase launch under bash over throwaway git repositories",
		testkit.Inputs("scripts/merge-gate.sh"))
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	extract := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(src, start)
		if !ok {
			t.Fatalf("merge-gate.sh has no %q", start)
		}
		body, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("merge-gate.sh has no %q after %q", end, start)
		}
		return start + body
	}
	// The heavy-slot re-exec must hand the flag on, or the gate drops it.
	if !strings.Contains(src, `$([ "$skip_lint" = 1 ] && echo --skip-lint)`) {
		t.Fatal("merge-gate.sh's heavy-slot re-exec does not forward --skip-lint")
	}
	harness := strings.Join([]string{
		`set -uo pipefail`,
		`phase() { printf '%s|%s\n' "$1" "${*:2}" >>"$L/phases"; }`,
		`build_check() { :; }`,
		`fail=0 typecheck=false gofull=false half=2 quarter=2 test_lanes=go webdemo=${WEBDEMO:-false}`,
		`forge=(forge) notes=() pids=() run_plan=$PLAN`,
		extract(`lint_lanes=(`, "\n# The whole vitest suite"),
		extract(`base="" amend=0`, "\nexport GOFLAGS"),
		extract(`[ -n "$base" ] || base=$(git merge-base`, "\nL=${MG_LOGDIR"),
		`L=$LOGDIR`,
		extract(`if [ "$skip_lint" = 1 ]; then echo skip-lint >"$L/tier"`, "\npin="),
		// As the gate computes it: the working tree against the base.
		`files=$( { git diff --name-only "$base"; git ls-files --others --exclude-standard; } | sort -u)`,
		extract("if [ \"$fail\" = 0 ]; then\n  phase build build_check", "\n# Every launched phase is waited for"),
		`wait`,
		`echo "$base" >"$L/base"`,
	}, "\n")

	for _, tc := range []struct {
		name       string
		proto      bool
		flags      []string
		tier       string
		wantPhases []string
		noPhases   []string
		queueEntry string // CI_QUEUE_ENTRY the compat phases get
		webDemo    bool   // the plan selected the forge demo build
	}{
		{"merge tier", true, nil, "", []string{"build", "test", "lint", "vet", "proto", "migsafety", "migorder"}, []string{"demo"}, "0", false},
		{"skip-lint", true, []string{"--skip-lint"}, "skip-lint", []string{"build", "test", "proto", "migsafety", "migorder"}, []string{"vet", "lint"}, "0", false},
		{"skip-lint keeps buf lint without a proto change", false, []string{"--skip-lint"}, "skip-lint", []string{"build", "test", "proto", "migsafety"}, []string{"vet", "lint"}, "0", false},
		{"queue", true, []string{"--queue"}, "queue", []string{"build", "test", "lint", "proto", "migsafety", "migorder"}, []string{"vet"}, "1", false},
		{"queue skips buf lint without a proto change", false, []string{"--queue"}, "queue", []string{"build", "test", "lint", "migsafety"}, []string{"vet", "proto"}, "1", false},
		{"queue builds a selected demo bundle", false, []string{"--queue"}, "queue", []string{"build", "test", "lint", "demo"}, []string{"vet"}, "1", true},
		{"queue with --skip-lint lints nothing", false, []string{"--queue", "--skip-lint"}, "skip-lint", []string{"build", "test"}, []string{"vet", "lint"}, "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mergeBase := branchWithMergeOfMain(t, tc.proto)
			logs := t.TempDir()
			plan := filepath.Join(logs, "plan.json")
			if err := os.WriteFile(plan, []byte(`{"go":{"build":["./internal/x"],"packages":["./internal/x"]}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", append([]string{"-c", harness, "merge-gate"}, tc.flags...)...)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "LOGDIR="+logs, "PLAN="+plan, fmt.Sprintf("WEBDEMO=%v", tc.webDemo))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("harness: %v\n%s", err, out)
			}
			read := func(name string) string {
				b, _ := os.ReadFile(filepath.Join(logs, name))
				return strings.TrimSpace(string(b))
			}
			if got := read("base"); got != mergeBase {
				t.Fatalf("diff base %s, want the merge-base with origin/main %s", got, mergeBase)
			}
			if got := read("tier"); got != tc.tier {
				t.Fatalf("tier %q, want %q", got, tc.tier)
			}
			phases := map[string]string{}
			for _, line := range strings.Split(read("phases"), "\n") {
				if n, args, ok := strings.Cut(line, "|"); ok {
					phases[n] = args
				}
			}
			for _, n := range tc.wantPhases {
				if _, ok := phases[n]; !ok {
					t.Errorf("phase %s did not run; ran %v", n, phases)
				}
			}
			for _, n := range tc.noPhases {
				if _, ok := phases[n]; ok {
					t.Errorf("phase %s ran: %s", n, phases[n])
				}
			}
			if args, ok := phases["lint"]; ok {
				for _, lane := range []string{"--tracing", "--optional-deps-guard"} {
					if !strings.Contains(args, " "+lane) {
						t.Errorf("lint runs %q, without %s", args, lane)
					}
				}
			}
			for _, n := range []string{"proto", "migsafety"} {
				args, ok := phases[n]
				if !ok {
					continue
				}
				want := "env CI_BASE=" + mergeBase + " CI_QUEUE_ENTRY=" + tc.queueEntry + " "
				if !strings.HasPrefix(args, want) {
					t.Errorf("phase %s runs %q, want it to start %q", n, args, want)
				}
			}
		})
	}
}

// branchWithMergeOfMain builds a repository whose HEAD is a PR branch: a
// commit, a merge of a main that moved on, and another commit (adding a
// migration). It returns the checkout and the merge-base with origin/main.
func branchWithMergeOfMain(t *testing.T, proto bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, body string) {
		t.Helper()
		putFixture(t, dir, name, body, 0o755)
	}
	git("init", "-q", "-b", "main")
	write("scripts/lint-scope.sh", "#!/bin/sh\ncat >/dev/null\necho internal/x\n")
	write("README.md", "base\n")
	git("add", "-A")
	git("commit", "-q", "-m", "base")
	git("checkout", "-q", "-b", "feature")
	write("internal/x/x.go", "package x\n")
	if proto {
		write("proto/x/v1/x.proto", "syntax = \"proto3\";\n")
	}
	git("add", "-A")
	git("commit", "-q", "-m", "one")
	git("checkout", "-q", "main")
	write("README.md", "main moved\n")
	git("commit", "-q", "-am", "main moved")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	git("checkout", "-q", "feature")
	git("merge", "-q", "--no-edit", "origin/main")
	write("db/migrations/20260101000000_a.up.sql", "SELECT 1;\n")
	git("add", "-A")
	git("commit", "-q", "-m", "two")
	return dir, git("merge-base", "HEAD", "origin/main")
}

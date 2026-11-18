package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
//
// golangci (scripts/golangci-changed.sh, the merge queue's golangci (changed)
// job) runs with the gate's diff base in the merge tier and under --skip-lint,
// whose PR would otherwise be ejected by that job, but never under --queue or
// --skip-golangci (ci.yml's ci:run gate), where such a job runs beside the gate
// and the gate marks golangci-elsewhere for gate-verify; nor on a diff without
// a .go file.
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
	// The heavy-slot re-exec must hand the flags on, or the gate drops them.
	for _, flag := range []string{"skip-lint", "skip-golangci"} {
		if !strings.Contains(src, `$([ "$`+strings.ReplaceAll(flag, "-", "_")+`" = 1 ] && echo --`+flag+`)`) {
			t.Fatalf("merge-gate.sh's heavy-slot re-exec does not forward --%s", flag)
		}
	}
	harness := strings.Join([]string{
		`set -uo pipefail`,
		`phase() { printf '%s|%s\n' "$1" "${*:2}" >>"$L/phases"; }`,
		`build_check() { :; }`,
		`fail=0 typecheck=false gofull=false half=2 quarter=2 test_lanes=go webdemo=${WEBDEMO:-false}`,
		`forge=(forge) notes=() pids=() run_plan=$PLAN MG_IN_SLOT=${TEST_IN_SLOT:-}`,
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
		noGo       bool   // the branch changes no .go file
		inSlot     bool   // the gate took the heavy slot itself (MG_IN_SLOT)
	}{
		{"merge tier", true, nil, "", []string{"build", "test", "golangci", "lint", "vet", "proto", "migsafety", "migorder"}, []string{"demo"}, "0", false, false, false},
		{"merge tier in its own slot drops golangci's lock", false, nil, "", []string{"golangci"}, nil, "0", false, false, true},
		{"merge tier without a Go change skips golangci", true, nil, "", []string{"build", "test", "lint", "proto", "migsafety", "migorder"}, []string{"golangci"}, "0", false, true, false},
		{"skip-lint", true, []string{"--skip-lint"}, "skip-lint", []string{"build", "test", "golangci", "proto", "migsafety", "migorder"}, []string{"vet", "lint"}, "0", false, false, false},
		{"skip-lint without a Go change skips golangci", false, []string{"--skip-lint"}, "skip-lint", []string{"build", "test", "proto"}, []string{"vet", "lint", "golangci"}, "0", false, true, false},
		{"skip-lint keeps buf lint without a proto change", false, []string{"--skip-lint"}, "skip-lint", []string{"build", "test", "golangci", "proto", "migsafety"}, []string{"vet", "lint"}, "0", false, false, false},
		{"queue", true, []string{"--queue"}, "queue", []string{"build", "test", "lint", "proto", "migsafety", "migorder"}, []string{"vet", "golangci"}, "1", false, false, false},
		{"queue skips buf lint without a proto change", false, []string{"--queue"}, "queue", []string{"build", "test", "lint", "migsafety"}, []string{"vet", "proto", "golangci"}, "1", false, false, false},
		{"queue builds a selected demo bundle", false, []string{"--queue"}, "queue", []string{"build", "test", "lint", "demo"}, []string{"vet", "golangci"}, "1", true, false, false},
		{"queue with --skip-lint lints nothing", false, []string{"--queue", "--skip-lint"}, "skip-lint", []string{"build", "test"}, []string{"vet", "lint", "golangci"}, "1", false, false, false},
		{"skip-golangci", true, []string{"--skip-golangci"}, "", []string{"build", "test", "lint", "vet", "proto"}, []string{"golangci"}, "0", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, mergeBase := branchWithMergeOfMain(t, tc.proto, !tc.noGo)
			logs := t.TempDir()
			plan := filepath.Join(logs, "plan.json")
			if err := os.WriteFile(plan, []byte(`{"go":{"build":["./internal/x"],"packages":["./internal/x"]}}`), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", append([]string{"-c", harness, "merge-gate"}, tc.flags...)...)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "LOGDIR="+logs, "PLAN="+plan, fmt.Sprintf("WEBDEMO=%v", tc.webDemo), "TEST_IN_SLOT=")
			if tc.inSlot {
				cmd.Env = append(cmd.Env, "TEST_IN_SLOT=1")
			}
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
			// gate-verify obliges golangci unless a job beside the gate runs it.
			elsewhere := slices.Contains(tc.flags, "--queue") || slices.Contains(tc.flags, "--skip-golangci")
			if _, err := os.Stat(filepath.Join(logs, "golangci-elsewhere")); (err == nil) != elsewhere {
				t.Errorf("golangci-elsewhere marker present=%v, want %v", err == nil, elsewhere)
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
			if args, ok := phases["golangci"]; ok {
				// The queue job's own command (task ci:golangci -- --base),
				// with the gate's base; the slot flag only when the gate
				// took a heavy slot itself.
				want := "env GOMAXPROCS=2 scripts/golangci-changed.sh --base " + mergeBase
				if tc.inSlot {
					want += " --in-slot"
				}
				if args != want {
					t.Errorf("golangci runs %q, want %q", args, want)
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
// migration). Its first commit adds internal/x/x.go when goFile is set, and
// internal/x/README.md otherwise. It returns the checkout and the merge-base
// with origin/main.
func branchWithMergeOfMain(t *testing.T, proto, goFile bool) (string, string) {
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
	if goFile {
		write("internal/x/x.go", "package x\n")
	} else {
		write("internal/x/README.md", "x\n")
	}
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

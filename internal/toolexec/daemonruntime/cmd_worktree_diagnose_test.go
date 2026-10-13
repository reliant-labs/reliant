package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreeerr"
)

// A refused worktree.create says WHY in a typed field, so the server can tell
// the caller's mistake from its own fault without reading git's output.
//
// ── The bug ──
//
// Prod, 2026-10-13 (Sentry GO-2A / GO-2B): an agent asked for a worktree based
// on `feat/telemetry-delivery-c2`, which did not exist. git said
//
//	fatal: invalid reference: feat/telemetry-delivery-c2
//
// and that text was all the server got. It string-matched it into an untyped
// error and logged it at ERROR — twice, both reaching Sentry — for a typo
// nobody on our side could act on. The same match also missed git >= 2.42's
// "is already used by worktree", so a branch checked out elsewhere fell to
// the generic server-error path.

// diagnoseRepo is a repo on `main` with one commit and the branches named.
func diagnoseRepo(t *testing.T, branches ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	for _, b := range branches {
		run("branch", b)
	}
	return dir
}

type diagnosedCreate struct {
	Success     bool               `json:"success"`
	Error       string             `json:"error"`
	ErrorCode   worktreeerr.Reason `json:"error_code"`
	ErrorDetail string             `json:"error_detail"`
}

func createFor(t *testing.T, req map[string]any) diagnosedCreate {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw, err := handleWorktreeCreate(context.Background(), payload)
	if err != nil {
		t.Fatalf("handler returned a transport error: %v", err)
	}
	var resp diagnosedCreate
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return resp
}

func TestWorktreeCreate_DiagnosesTheCallersMistake(t *testing.T) {
	repo := diagnoseRepo(t, "release/compute-tier-redesign", "rel")

	cases := []struct {
		name       string
		branch     string
		base       string
		wantCode   worktreeerr.Reason
		wantDetail string
	}{
		{
			// The prod event: a base branch that does not exist.
			name: "base branch does not exist", branch: "feat/new", base: "feat/telemetry-delivery-c2",
			wantCode: worktreeerr.BaseNotFound, wantDetail: "feat/telemetry-delivery-c2",
		},
		{
			name: "branch name git refuses", branch: "bad..name", base: "main",
			wantCode: worktreeerr.InvalidBranchName, wantDetail: "bad..name",
		},
		{
			name: "branch would be a parent of an existing branch", branch: "release", base: "main",
			wantCode: worktreeerr.BranchNameConflict, wantDetail: "release/compute-tier-redesign",
		},
		{
			name: "branch would be a child of an existing branch", branch: "rel/y", base: "main",
			wantCode: worktreeerr.BranchNameConflict, wantDetail: "rel",
		},
		{
			// `main` is checked out in the repo's own checkout.
			name: "branch is checked out in another worktree", branch: "main", base: "main",
			wantCode: worktreeerr.BranchCheckedOut, wantDetail: repo,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := createFor(t, map[string]any{
				"project_path": repo,
				"workspace_id": "ws-diagnose",
				"name":         "diagnose",
				"branch":       tc.branch,
				"base_branch":  tc.base,
			})
			if resp.Success {
				t.Fatalf("expected the create to fail")
			}
			if resp.ErrorCode != tc.wantCode {
				t.Fatalf("error_code = %q, want %q (git said: %s)", resp.ErrorCode, tc.wantCode, resp.Error)
			}
			if !samePath(resp.ErrorDetail, tc.wantDetail) {
				t.Errorf("error_detail = %q, want %q", resp.ErrorDetail, tc.wantDetail)
			}
			if resp.Error == "" {
				t.Error("git's own output must still be carried for the logs")
			}
		})
	}
}

// A failure that is not the caller's to fix carries no code: the server must
// keep treating it as its own fault and report it.
func TestWorktreeCreate_LeavesRealFailuresUndiagnosed(t *testing.T) {
	t.Run("destination already populated", func(t *testing.T) {
		// The workspace path is ours (name + random suffix), never the
		// caller's choice, so a collision is our bug.
		repo := diagnoseRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		dest := filepath.Join(home, ".reliant", "worktrees", "ws-taken")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{
			"project_path": repo, "workspace_id": "ws-taken", "name": "taken",
			"branch": "feature/ok", "base_branch": "main",
		})
		raw, err := handleWorktreeCreate(context.Background(), payload)
		if err != nil {
			t.Fatal(err)
		}
		var resp diagnosedCreate
		_ = json.Unmarshal(raw, &resp)
		if resp.Success {
			t.Fatal("expected the create to fail")
		}
		if resp.ErrorCode != "" {
			t.Fatalf("error_code = %q for a failure that is not the caller's; want none", resp.ErrorCode)
		}
	})

	t.Run("auto-detected base", func(t *testing.T) {
		// Only a base the CALLER named is theirs to fix. One the daemon
		// picked itself that fails is the daemon's bug, so a missing base is
		// diagnosed only when it was supplied. Here nothing was supplied and
		// the failure is the path, so there must be no BaseNotFound.
		repo := diagnoseRepo(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		dest := filepath.Join(home, ".reliant", "worktrees", "ws-auto")
		if err := os.MkdirAll(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dest, "x"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]any{
			"project_path": repo, "workspace_id": "ws-auto", "name": "auto", "branch": "feature/auto",
		})
		raw, err := handleWorktreeCreate(context.Background(), payload)
		if err != nil {
			t.Fatal(err)
		}
		var resp diagnosedCreate
		_ = json.Unmarshal(raw, &resp)
		if resp.Success || resp.ErrorCode != "" {
			t.Fatalf("success=%v error_code=%q; want a failure with no code", resp.Success, resp.ErrorCode)
		}
	})
}

// samePath compares paths through symlinks (macOS reports /private/var for
// /var), and plain strings otherwise.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

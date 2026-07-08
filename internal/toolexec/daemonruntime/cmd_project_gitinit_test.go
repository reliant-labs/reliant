package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDirIsEffectivelyEmpty(t *testing.T) {
	cases := []struct {
		name    string
		entries []string // files to create; dirs end with "/"
		want    bool
	}{
		{"truly empty", nil, true},
		{"only reliant.md", []string{"reliant.md"}, true},
		{"only .reliant dir", []string{".reliant/"}, true},
		{"scaffold + existing .git", []string{"reliant.md", ".git/"}, true},
		{"has a real file", []string{"main.go"}, false},
		{"scaffold plus real file", []string{"reliant.md", "README.md"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, e := range tc.entries {
				if len(e) > 0 && e[len(e)-1] == '/' {
					if err := os.MkdirAll(filepath.Join(dir, e), 0o755); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(filepath.Join(dir, e), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := dirIsEffectivelyEmpty(dir)
			if err != nil {
				t.Fatalf("dirIsEffectivelyEmpty: %v", err)
			}
			if got != tc.want {
				t.Errorf("dirIsEffectivelyEmpty(%v) = %v, want %v", tc.entries, got, tc.want)
			}
		})
	}
}

// TestHandleInitGitRepo_OnlyIfEmpty verifies the auto-init gate: an empty
// project initializes, but a non-empty folder is skipped (Success=false, no
// error, no .git created) so the user is prompted instead.
func TestHandleInitGitRepo_OnlyIfEmpty(t *testing.T) {
	call := func(t *testing.T, path string) initGitRepoResponse {
		t.Helper()
		payload, _ := json.Marshal(initGitRepoRequest{
			Path:          path,
			InitialBranch: "main",
			OnlyIfEmpty:   true,
		})
		out, err := handleInitGitRepo(context.Background(), payload)
		if err != nil {
			t.Fatalf("handleInitGitRepo: %v", err)
		}
		var resp initGitRepoResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			t.Fatalf("unmarshal resp: %v", err)
		}
		return resp
	}

	t.Run("empty project inits", func(t *testing.T) {
		dir := t.TempDir()
		resp := call(t, dir)
		if !resp.Success {
			t.Fatalf("expected Success on empty dir, got %+v", resp)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			t.Errorf(".git should exist after init: %v", err)
		}
	})

	t.Run("non-empty project is skipped", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
		resp := call(t, dir)
		if resp.Success {
			t.Errorf("expected skip (Success=false) on non-empty dir, got %+v", resp)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
			t.Errorf(".git must NOT be created for a non-empty dir")
		}
	})
}

package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// worktree.copy_paths copies exact paths between two workspace ROOTS. In a
// multi-repo workspace that is what lets one list name a repo's gitignored
// file (`reliant/.env`) and a root-level one (`.env`) alike.
func TestWorktreeCopyPaths_CopiesExactPathsRootToRoot(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	for rel, content := range map[string]string{
		".env":                          "ROOT",
		"reliant/.env":                  "RELIANT",
		"reliant/web/node_modules/a.js": "a",
		"forge/.env":                    "FORGE — not requested",
	} {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The new workspace already has its repo checkouts.
	for _, repo := range []string{"reliant", "forge"} {
		if err := os.MkdirAll(filepath.Join(dst, repo), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	payload, _ := json.Marshal(worktreeCopyPathsRequest{
		SourceRoot: src,
		DestRoot:   dst,
		Paths:      []string{".env", "reliant/.env", "reliant/web/node_modules", "control-plane/.env"},
	})
	raw, err := handleWorktreeCopyPaths(context.Background(), payload)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var resp worktreeCopyPathsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error != "" || len(resp.Failed) > 0 {
		t.Fatalf("unexpected failure: error=%q failed=%v", resp.Error, resp.Failed)
	}

	for rel, want := range map[string]string{
		".env":                          "ROOT",
		"reliant/.env":                  "RELIANT",
		"reliant/web/node_modules/a.js": "a",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || string(got) != want {
			t.Errorf("%s: got %q, %v; want %q", rel, got, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "forge/.env")); !os.IsNotExist(err) {
		t.Errorf("forge/.env was not requested and must not be copied")
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != "control-plane/.env" {
		t.Errorf("missing = %v, want [control-plane/.env]", resp.Missing)
	}
}

// The handler is the boundary that touches the disk, so it validates even
// though the server already did.
func TestWorktreeCopyPaths_RejectsEscapingPath(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	payload, _ := json.Marshal(worktreeCopyPathsRequest{
		SourceRoot: src, DestRoot: dst, Paths: []string{"../../etc/passwd"},
	})
	raw, err := handleWorktreeCopyPaths(context.Background(), payload)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var resp worktreeCopyPathsResponse
	_ = json.Unmarshal(raw, &resp)
	if resp.Error == "" {
		t.Fatal("an escaping path must be rejected")
	}
	if len(resp.Copied) > 0 {
		t.Fatalf("nothing may be copied when the request is invalid, got %v", resp.Copied)
	}
}

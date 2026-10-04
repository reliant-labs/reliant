// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// forge.project_name is the narrow read the API server does right after a
// clone lands, so a freshly cloned forge project knows its own forge name
// without anyone opening the Forge tab. These pin the answers it gives.

func callForgeProjectName(t *testing.T, path string) forgeProjectNameResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"path": path})
	if err != nil {
		t.Fatal(err)
	}
	out, err := handleForgeProjectName(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleForgeProjectName: %v", err)
	}
	var resp forgeProjectNameResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestForgeProjectName_ReadsTheNameFromForgeYAML(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("name: barksocial\nmodule: github.com/acme/barksocial\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := callForgeProjectName(t, root)
	if !resp.HasForge {
		t.Error("a directory with a forge.yaml is a forge project")
	}
	if resp.ForgeProjectName != "barksocial" {
		t.Errorf("got name %q, want barksocial", resp.ForgeProjectName)
	}
}

func TestForgeProjectName_PlainRepoIsNotAForgeProject(t *testing.T) {
	// The server keys off has_forge to decide whether to mark the row at
	// all; a plain repo must not come back looking like a forge project.
	resp := callForgeProjectName(t, t.TempDir())
	if resp.HasForge {
		t.Error("a directory with no forge.yaml must not report has_forge")
	}
	if resp.ForgeProjectName != "" {
		t.Errorf("got name %q, want empty", resp.ForgeProjectName)
	}
}

func TestForgeProjectName_UnparseableManifestIsStillAForgeProject(t *testing.T) {
	// A forge.yaml forge itself would reject is a forge project with a
	// defect, not a plain repo. Reporting has_forge=false here would hide
	// the defect; the empty name is what stops the row being marked.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"),
		[]byte("\tname: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	resp := callForgeProjectName(t, root)
	if !resp.HasForge {
		t.Error("forge.yaml exists; has_forge must reflect the file, not whether it parses")
	}
	if resp.ForgeProjectName != "" {
		t.Errorf("an unparseable manifest must name nothing, got %q", resp.ForgeProjectName)
	}
}

func TestForgeProjectName_MissingPathIsRejected(t *testing.T) {
	// A blank path would stat the process's working directory and could
	// report the daemon's own checkout as the project's forge name.
	if _, err := handleForgeProjectName(context.Background(), []byte(`{}`)); err == nil {
		t.Error("an empty path must be refused, not resolved against the cwd")
	}
}

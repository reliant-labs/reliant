package doctor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDisowned writes a .forge/disowned.json into dir. files maps
// project-relative paths to their disown records.
func writeDisowned(t *testing.T, dir string, files map[string]map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"forge_version": "test", "files": files})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "disowned.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDisownedFiles(t *testing.T) {
	tests := []struct {
		name       string
		files      map[string]map[string]any // nil → no disowned.json at all
		wantStatus Status
		wantInMsg  string
		wantInEvid []string
	}{
		{
			// The steady state: a project with no disowns has NO
			// .forge/disowned.json at all (Save deletes it when empty).
			name:       "no disowned.json — pass",
			files:      nil,
			wantStatus: StatusPass,
			wantInMsg:  "no disowned generated files",
		},
		{
			name: "disowned files — informational PASS with paths and re-adopt hint",
			files: map[string]map[string]any{
				"pkg/app/wire_gen.go":  {"reason": "custom pool wiring", "disowned_at": "2026-06-10T00:00:00Z"},
				"pkg/app/bootstrap.go": {"reason": "legacy port", "disowned_at": "2026-06-10T00:00:00Z"},
			},
			wantStatus: StatusPass,
			wantInMsg:  "2 disowned generated file(s)",
			wantInEvid: []string{"pkg/app/bootstrap.go", "pkg/app/wire_gen.go"},
		},
		{
			// Legacy `forked: true` manifest entries are converted to
			// disowned.json records by the legacy-manifest migration on
			// the next `forge generate` — doctor reads the one converted
			// source of truth (the pre-migration manifest is invisible
			// here by design; the migration is automatic and loud).
			name: "migrated legacy fork shows as disowned",
			files: map[string]map[string]any{
				"pkg/app/wire_gen.go": {"reason": "migrated from legacy .forge/checksums.json (legacy fork-era entry; the fork state was removed)", "disowned_at": "2026-06-01T00:00:00Z"},
			},
			wantStatus: StatusPass,
			wantInMsg:  "1 disowned generated file(s)",
			wantInEvid: []string{"pkg/app/wire_gen.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.files != nil {
				writeDisowned(t, dir, tt.files)
			}
			env := &Environment{ProjectDir: dir}
			res := CheckDisownedFiles(context.Background(), env)
			if res.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s (message: %s)", res.Status, tt.wantStatus, res.Message)
			}
			if !strings.Contains(res.Message, tt.wantInMsg) {
				t.Errorf("message %q missing %q", res.Message, tt.wantInMsg)
			}
			for _, want := range tt.wantInEvid {
				if !strings.Contains(res.Evidence, want) {
					t.Errorf("evidence %q missing %q", res.Evidence, want)
				}
			}
		})
	}
}

// An unreadable .forge/disowned.json is UNDETERMINED, not a warning. It is
// this check's only input, so a parse failure leaves it with no answer —
// and the reader must not be able to mistake it for "no disowned files".
func TestCheckDisownedFilesUnknownOnUnreadableManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".forge", "disowned.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := CheckDisownedFiles(context.Background(), &Environment{ProjectDir: dir})
	if res.Status != StatusUnknown {
		t.Fatalf("status = %s, want %s (message: %s)", res.Status, StatusUnknown, res.Message)
	}
	if res.Evidence == "" {
		t.Error("undetermined result carries no evidence naming the parse failure")
	}
}

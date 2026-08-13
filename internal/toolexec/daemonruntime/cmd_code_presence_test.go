// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeFiles creates each named file (with parent dirs) under root.
func writeFiles(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// The predicate exists to answer "is this a greenfield directory". These are
// the cases that decide whether stack guidance is injected, so each one is the
// difference between nudging a user who wanted a recommendation and lecturing
// a user who already has an app.
func TestScanCodePresence(t *testing.T) {
	tests := []struct {
		name        string
		files       []string
		wantHasCode bool
	}{
		{
			name:        "empty directory is greenfield",
			files:       nil,
			wantHasCode: false,
		},
		{
			name:        "prose and license only is greenfield",
			files:       []string{"README.md", "NOTES.md", "LICENSE"},
			wantHasCode: false,
		},
		{
			name:        "editor and git config only is greenfield",
			files:       []string{".gitignore", ".editorconfig", ".vscode/settings.json"},
			wantHasCode: false,
		},
		{
			name:        "the reliant scaffold alone is greenfield",
			files:       []string{"reliant.md", ".reliant/config.yaml"},
			wantHasCode: false,
		},
		{
			name:        "a single python file is not greenfield",
			files:       []string{"main.py"},
			wantHasCode: true,
		},
		{
			name:        "a package manifest is not greenfield",
			files:       []string{"package.json"},
			wantHasCode: true,
		},
		{
			name:        "nested source is not greenfield",
			files:       []string{"README.md", "src/app/handler.ts"},
			wantHasCode: true,
		},
		{
			name:        "build tooling is not greenfield",
			files:       []string{"Makefile"},
			wantHasCode: true,
		},
		{
			name:        "images alongside prose stay greenfield",
			files:       []string{"README.md", "docs/mockup.png"},
			wantHasCode: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files...)

			got, err := scanCodePresence(dir)
			if err != nil {
				t.Fatalf("scanCodePresence: %v", err)
			}
			if got.HasCode != tt.wantHasCode {
				t.Errorf("HasCode = %v, want %v (code files found: %v)",
					got.HasCode, tt.wantHasCode, got.CodeFiles)
			}
		})
	}
}

// node_modules is both the most expensive directory to walk and a guaranteed
// false positive — a dependency tree is not the user's code. The same holds
// for .git, which contains object files that no extension rule would classify
// as prose.
func TestScanCodePresenceSkipsDependencyAndVCSDirs(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir,
		"README.md",
		"node_modules/left-pad/index.js",
		".git/objects/ab/cdef",
		".venv/lib/python3.12/site-packages/foo.py",
	)

	got, err := scanCodePresence(dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if got.HasCode {
		t.Errorf("a directory whose only 'code' is vendored deps must stay greenfield; found %v", got.CodeFiles)
	}
}

// A .gitignore listing node_modules/ contains no code but is emphatically a
// stack declaration. The scan reports these so the caller can tell the model to
// read them before recommending a stack — otherwise "no code" gets mistaken for
// "no opinion".
func TestScanCodePresenceReportsStackDeclaringConfig(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, ".gitignore", ".vscode/settings.json", "README.md")

	got, err := scanCodePresence(dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if got.HasCode {
		t.Fatalf("config-only directory must be greenfield; found %v", got.CodeFiles)
	}

	found := map[string]bool{}
	for _, f := range got.ConfigFiles {
		found[f] = true
	}
	for _, want := range []string{".gitignore", ".vscode/settings.json"} {
		if !found[want] {
			t.Errorf("expected %q reported as stack-declaring config; got %v", want, got.ConfigFiles)
		}
	}
}

// The API tier reaches this handler by command name through the default
// registry. A handler that is implemented but never registered passes every
// unit test above and fails silently in production, where the only symptom is
// guidance that never appears.
func TestCodePresenceIsRegisteredAndDispatches(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "README.md")

	payload, err := json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	out, err := DefaultRegistry().Handle(context.Background(), "project.code_presence", payload)
	if err != nil {
		t.Fatalf("dispatch project.code_presence: %v", err)
	}

	var resp codePresenceResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("handler reported error: %s", resp.Error)
	}
	if resp.HasCode {
		t.Errorf("a README-only directory must report no code; got %+v", resp)
	}
}

// A missing path is a caller bug, and it must come back as a structured error
// rather than a confident "no code here" — which would inject stack guidance
// on the strength of a directory nobody looked at.
func TestCodePresenceRejectsEmptyPath(t *testing.T) {
	payload, err := json.Marshal(map[string]string{"path": ""})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	out, err := DefaultRegistry().Handle(context.Background(), "project.code_presence", payload)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	var resp codePresenceResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Error == "" {
		t.Error("an empty path must report an error, not a silent no-code answer")
	}
	if resp.HasCode {
		t.Error("an errored scan must not claim to have found code")
	}
}

// Sample lists are bounded: the caller puts them in a prompt, and a full
// listing would be both useless and expensive.
func TestScanCodePresenceBoundsSamples(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < codePresenceSampleLimit*3; i++ {
		writeFiles(t, dir, filepath.Join("src", "file"+string(rune('a'+i%26))+string(rune('a'+i/26))+".go"))
	}

	got, err := scanCodePresence(dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if !got.HasCode {
		t.Fatal("expected HasCode for a directory full of .go files")
	}
	if len(got.CodeFiles) > codePresenceSampleLimit {
		t.Errorf("CodeFiles = %d entries, want at most %d", len(got.CodeFiles), codePresenceSampleLimit)
	}
}

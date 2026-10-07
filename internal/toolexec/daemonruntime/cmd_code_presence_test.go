// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

			got, _, err := scanCodePresence(context.Background(), dir)
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

	got, _, err := scanCodePresence(context.Background(), dir)
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

	got, _, err := scanCodePresence(context.Background(), dir)
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

// The config sample is bounded: the caller puts it in a prompt, and a full
// listing would be both useless and expensive.
func TestScanCodePresenceBoundsConfigSample(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < codePresenceSampleLimit*3; i++ {
		writeFiles(t, dir, fmt.Sprintf(".vscode/settings-%03d.json", i))
	}

	got, _, err := scanCodePresence(context.Background(), dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if got.HasCode {
		t.Fatalf("an editor-config-only directory must stay greenfield; found %v", got.CodeFiles)
	}
	if len(got.ConfigFiles) != codePresenceSampleLimit {
		t.Errorf("ConfigFiles = %d entries, want exactly the %d-entry sample", len(got.ConfigFiles), codePresenceSampleLimit)
	}
}

// The probe answers "is there ANY code", so the first code file must end the
// scan. It runs on the StartChat path for every first message, and walking on
// to sample more files cost up to 2000 files and hundreds of directory reads
// on a real repo (measured 8-390ms on a busy laptop) for names nobody reads.
func TestScanCodePresenceStopsAtFirstCodeFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "README.md", "go.mod")
	// A large tree the answer does not depend on.
	for d := 0; d < 40; d++ {
		for f := 0; f < 10; f++ {
			writeFiles(t, dir, fmt.Sprintf("pkg/sub%02d/file%02d.go", d, f))
		}
	}

	got, stats, err := scanCodePresence(context.Background(), dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if !got.HasCode {
		t.Fatal("a directory with go.mod at its root has code")
	}
	if want := []string{"go.mod"}; !reflect.DeepEqual(got.CodeFiles, want) {
		t.Errorf("CodeFiles = %v, want %v (the file that decided the answer)", got.CodeFiles, want)
	}
	if stats.dirsRead != 1 {
		t.Errorf("read %d directories; code at the root must be decided by the root alone", stats.dirsRead)
	}
	if stats.filesVisited > 2 {
		t.Errorf("visited %d files; the scan must stop at the first code file", stats.filesVisited)
	}
}

// Breadth-first: a code file at the root decides the answer before any
// subdirectory is read, even one that sorts first and holds code of its own.
// A depth-first walk descends through every dotted directory (.claude/,
// .github/...) ahead of the project's own manifest.
func TestScanCodePresencePrefersShallowestCode(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, ".claude/agents/reviewer.json", ".github/workflows/ci.yml", "main.go")

	got, stats, err := scanCodePresence(context.Background(), dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if want := []string{"main.go"}; !reflect.DeepEqual(got.CodeFiles, want) {
		t.Errorf("CodeFiles = %v, want %v", got.CodeFiles, want)
	}
	if stats.dirsRead != 1 {
		t.Errorf("read %d directories, want 1", stats.dirsRead)
	}
}

// Code that only exists several levels below prose still counts.
func TestScanCodePresenceFindsNestedCode(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "README.md", "docs/guide.md", "src/app/deep/handler.py")

	got, _, err := scanCodePresence(context.Background(), dir)
	if err != nil {
		t.Fatalf("scanCodePresence: %v", err)
	}
	if want := []string{"src/app/deep/handler.py"}; !got.HasCode || !reflect.DeepEqual(got.CodeFiles, want) {
		t.Errorf("got HasCode=%v CodeFiles=%v, want HasCode=true CodeFiles=%v", got.HasCode, got.CodeFiles, want)
	}
}

// A directory nobody could read must not come back as "no code here" — that
// would inject greenfield guidance for a path that does not exist.
func TestScanCodePresenceMissingRootIsAnError(t *testing.T) {
	_, _, err := scanCodePresence(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("scanning a missing directory must fail, not report greenfield")
	}
}

// The probe runs under the caller's deadline; a cancelled caller stops the
// scan rather than finishing a walk nobody will read.
func TestScanCodePresenceHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "README.md")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := scanCodePresence(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

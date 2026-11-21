package kclrender

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWithPreviewDArgs(t *testing.T) {
	dir := t.TempDir()
	if got := withPreviewDArgs(dir, []string{"env=x"}); !reflect.DeepEqual(got, []string{"env=x"}) {
		t.Fatalf("no marker: %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, PreviewMarkerFile), []byte(`{"of":"preview","expires_at":"2026-11-15T12:00:00Z","source":"pr-7"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"env=x", `forge_preview_of="preview"`, `forge_preview_expires_at="2026-11-15T12:00:00Z"`, `forge_preview_source="pr-7"`}
	if got := withPreviewDArgs(dir, []string{"env=x"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("marker: %v", got)
	}
	main := filepath.Join(dir, "main.k")
	if err := os.WriteFile(main, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := withPreviewDArgs(main, []string{"env=x"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("a file in the instance dir: %v", got)
	}
	// A caller's own binding wins.
	if got := withPreviewDArgs(dir, []string{`forge_preview_of="other"`}); got[0] != `forge_preview_of="other"` || len(got) != 3 {
		t.Fatalf("caller binding: %v", got)
	}
}

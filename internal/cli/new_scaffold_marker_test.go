package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScaffoldInProgress_UndoesOnlyAnUnfinishedScaffold(t *testing.T) {
	fileExists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	t.Run("fresh directory is removed", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "app")
		s, err := beginScaffold(target, false)
		if err != nil {
			t.Fatal(err)
		}
		if !fileExists(s.markerPath) {
			t.Fatal("beginScaffold wrote no marker")
		}
		s.undoUnlessFinished()
		if fileExists(target) {
			t.Error("a failed fresh scaffold must be removed")
		}
	})

	t.Run("in-place keeps the user's directory", func(t *testing.T) {
		target := t.TempDir()
		own := filepath.Join(target, "main.go")
		if err := os.WriteFile(own, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		s, err := beginScaffold(target, true)
		if err != nil {
			t.Fatal(err)
		}
		s.undoUnlessFinished()
		if !fileExists(own) || fileExists(s.markerPath) {
			t.Error("in-place undo must remove the marker alone")
		}
	})

	t.Run("finished is left alone", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "app")
		s, err := beginScaffold(target, false)
		if err != nil {
			t.Fatal(err)
		}
		s.finish()
		s.undoUnlessFinished()
		if !fileExists(target) {
			t.Error("a finished scaffold was removed")
		}
	})

	t.Run("no marker, no undo", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "app")
		s, err := beginScaffold(target, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(s.markerPath); err != nil {
			t.Fatal(err)
		}
		s.undoUnlessFinished()
		if !fileExists(target) {
			t.Error("the undo ran without its marker (the TOCTOU guard)")
		}
	})
}

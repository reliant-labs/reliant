package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// scaffoldInProgress is `forge project new`'s undo for a scaffold that fails
// part-way: the in-progress marker it drops in the target, and what removing
// a failed scaffold may touch.
//
// To guard against TOCTOU — another process creating files at the target in
// the meantime — the marker is written immediately after the directory is
// created, and the undo runs only while that marker is still present. In
// --in-place mode the target is a directory the user owns, so the undo never
// removes it: only the marker goes.
type scaffoldInProgress struct {
	targetPath string
	markerPath string
	inPlace    bool
	finished   bool
}

// beginScaffold creates the target directory and drops the in-progress
// marker, before the generator runs. Creating the directory up front is safe:
// the generator populates it with MkdirAll. Nothing is left to undo when it
// fails — the marker an undo keys on was never written.
func beginScaffold(targetPath string, inPlace bool) (*scaffoldInProgress, error) {
	s := &scaffoldInProgress{
		targetPath: targetPath,
		markerPath: filepath.Join(targetPath, ".forge", ".scaffold-in-progress"),
		inPlace:    inPlace,
	}
	if err := os.MkdirAll(filepath.Dir(s.markerPath), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create project directory: %w", err)
	}
	if err := atomicfile.WriteFile(s.markerPath, []byte("forge scaffold in progress\n"), 0o644); err != nil {
		return nil, fmt.Errorf("failed to write scaffold marker: %w", err)
	}
	return s, nil
}

// finish marks the scaffold complete, so undoUnlessFinished leaves it alone.
// The marker itself is finalize's to remove (newFinalizeInput.markerPath).
func (s *scaffoldInProgress) finish() { s.finished = true }

// undoUnlessFinished removes a scaffold that did not finish: the whole
// target for a fresh directory, the marker alone in --in-place mode. Meant to
// be deferred right after beginScaffold.
func (s *scaffoldInProgress) undoUnlessFinished() {
	if s.finished {
		return
	}
	if _, err := os.Stat(s.markerPath); err != nil {
		return
	}
	if s.inPlace {
		if rmErr := os.Remove(s.markerPath); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Fprintf(os.Stderr, "warning: failed to remove scaffold marker: %v\n", rmErr)
		}
		return
	}
	if rmErr := os.RemoveAll(s.targetPath); rmErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to clean up %s: %v\n", s.targetPath, rmErr)
	}
}

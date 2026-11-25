// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A command that fails in, or above, a directory whose removed output is
// still missing gets a note naming it and how to put it back; elsewhere, or
// once it is reinstalled, nothing.
func TestRecords_Note(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	nm := filepath.Join(root, "web", "node_modules")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "web"), 0o755))
	recPath := filepath.Join(base, "state", RecordFile)
	r := NewRecords(recPath)
	r.add(root, time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), 72*time.Hour, []string{nm})

	note := r.Note(filepath.Join(root, "web"))
	require.Contains(t, note, "Reliant removed node_modules on 2026-11-22, after 72h0m0s unused")
	require.Contains(t, note, "npm ci")
	require.Contains(t, r.Note(root), "web/node_modules", "named relative to where the command ran")
	require.Contains(t, r.Note(base), "ws/web/node_modules", "a command above the directory")
	require.Empty(t, r.Note(filepath.Join(base, "elsewhere")))

	// Persisted: a restarted daemon still notes it.
	require.Contains(t, NewRecords(recPath).Note(root), "node_modules")

	require.NoError(t, os.MkdirAll(nm, 0o755))
	require.Empty(t, r.Note(root), "reinstalled: nothing to say")
}

func TestHumanBytes(t *testing.T) {
	t.Parallel()
	require.Equal(t, "512 B", HumanBytes(512))
	require.Equal(t, "1.5 KiB", HumanBytes(1536))
	require.Equal(t, "2.0 GiB", HumanBytes(2<<30))
}

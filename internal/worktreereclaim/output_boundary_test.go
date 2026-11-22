// Copyright (c) 2026 Reliant Labs
package worktreereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOutputInventorySurvivesAuthorization(t *testing.T) {
	skipWithoutSafeInspection(t)
	dir := filepath.Join(t.TempDir(), "dist")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	file := filepath.Join(dir, "output.js")
	require.NoError(t, os.WriteFile(file, []byte("generated"), 0o600))
	err := RemoveInventoriedOutput(context.Background(), dir, 0, time.Now().Add(time.Hour), func(context.Context) error {
		return os.WriteFile(filepath.Join(dir, ".env"), []byte("unique secret"), 0o600)
	})
	require.Error(t, err)
	require.FileExists(t, file)
	require.FileExists(t, filepath.Join(dir, ".env"))
}

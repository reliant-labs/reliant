package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/skills"
)

func TestServiceResolve_DelegatesToRuntimeCompatibilityBoundary(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	write := func(path, content string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}

	write(filepath.Join(project, ".reliant", "skills", "sql-debug", "SKILL.md"), `---
name: sql-debug
description: Analyze SQL performance and schema bottlenecks
---
Use SQL diagnostics.`)

	resolved := New().Resolve(context.Background(), skills.ResolveTurnInput{
		ProjectPath:       project,
		LatestUserMessage: "Need help debugging a SQL query",
		ActivationMode:    "auto",
	})

	require.NotNil(t, resolved.ActiveSkill)
	require.Equal(t, "sql-debug", resolved.ActiveSkill.Name)
}

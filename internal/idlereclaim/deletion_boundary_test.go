// Copyright (c) 2026 Reliant Labs
package idlereclaim

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScratchRevalidatesAfterAdmission(t *testing.T) {
	for _, change := range []string{"keep", "source", "activity", "protected"} {
		t.Run(change, func(t *testing.T) {
			h := newHarness(t)
			dir := h.chat("c1")
			write(t, dir, "app/package.json", "{}")
			write(t, dir, "app/dist/output.js", "generated")
			h.age(dir, 8*day)
			acquire := h.r.Acquire
			h.r.Acquire = func(ctx context.Context, paths []string) (Lease, error) {
				switch change {
				case "keep":
					write(t, dir, KeepFile, "")
				case "source":
					write(t, dir, "new.go", "source")
				case "activity":
					h.scratchUse[dir] = h.now
				case "protected":
					write(t, dir, "app/dist/.env", "secret")
				}
				return acquire(ctx, paths)
			}
			h.pass()
			require.True(t, exists(filepath.Join(dir, "app/dist/output.js")))
		})
	}
}

func TestScratchPreservesForceAddedOutput(t *testing.T) {
	for _, output := range []string{"dist", "node_modules"} {
		t.Run(output, func(t *testing.T) {
			h := newHarness(t)
			dir := h.chat("c1")
			gitRun(t, dir, "init")
			write(t, dir, "package.json", "{}")
			write(t, dir, ".gitignore", output+"/\n")
			write(t, dir, output+"/manual.js", "original")
			gitRun(t, dir, "add", "-f", output+"/manual.js")
			write(t, dir, output+"/manual.js", "edited source")
			h.age(dir, 3*day)
			h.pass()
			b, err := os.ReadFile(filepath.Join(dir, output, "manual.js"))
			require.NoError(t, err)
			require.Equal(t, "edited source", string(b))
		})
	}
}

func TestScratchFindsRepositoryInsideNodeModules(t *testing.T) {
	h := newHarness(t)
	dir := h.chat("c1")
	nested := filepath.Join(dir, "node_modules", "local")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	gitRun(t, nested, "init")
	write(t, nested, "work.txt", "unique work")
	h.age(dir, 8*day)
	h.pass()
	require.True(t, exists(filepath.Join(nested, "work.txt")))
}

// Copyright (c) 2025 Reliant Labs
package copypath

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClean(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{".env", ".env"},
		{" .env ", ".env"},
		{"reliant/.env", "reliant/.env"},
		{"web/node_modules/", "web/node_modules"},
		{"./web/./node_modules", "web/node_modules"},
		{"a/b/../c", "a/c"},
	} {
		got, err := Clean(tc.in)
		require.NoError(t, err, "Clean(%q)", tc.in)
		assert.Equal(t, tc.want, got, "Clean(%q)", tc.in)
	}

	for _, bad := range []string{"", "   ", ".", "./", "..", "../x", "a/../../x", "/etc/passwd"} {
		_, err := Clean(bad)
		assert.Error(t, err, "Clean(%q) must reject", bad)
	}
}

func TestCleanAllDedupsAndStopsAtFirstInvalid(t *testing.T) {
	got, err := CleanAll([]string{".env", "./.env", "web/node_modules/", "web/node_modules"})
	require.NoError(t, err)
	assert.Equal(t, []string{".env", "web/node_modules"}, got)

	_, err = CleanAll([]string{".env", "../escape"})
	assert.Error(t, err)
}

// tree builds files under root from a path -> content map.
func tree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

// The behaviour this package exists for: a bare name is an exact path, so
// ".env" copies the ROOT .env and nothing else. It used to be a recursive
// search, which walked node_modules on every workspace create.
func TestCopyBareNameIsTheRootFileNotASearch(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	tree(t, src, map[string]string{
		".env":          "ROOT",
		"frontend/.env": "NESTED",
	})

	res := Copy(src, dst, []string{".env"})

	assert.Equal(t, []string{".env"}, res.Copied)
	assert.Equal(t, "ROOT", read(t, filepath.Join(dst, ".env")))
	assert.NoFileExists(t, filepath.Join(dst, "frontend/.env"))
}

func TestCopyNestedFileAndWholeDirectory(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	tree(t, src, map[string]string{
		"reliant/.env":                       "R",
		"web/node_modules/pkg/index.js":      "js",
		"web/node_modules/pkg/deep/file.txt": "deep",
		"web/src/app.ts":                     "not asked for",
	})

	res := Copy(src, dst, []string{"reliant/.env", "web/node_modules"})

	assert.ElementsMatch(t, []string{"reliant/.env", "web/node_modules"}, res.Copied)
	assert.Empty(t, res.Failed)
	assert.Equal(t, "R", read(t, filepath.Join(dst, "reliant/.env")))
	assert.Equal(t, "js", read(t, filepath.Join(dst, "web/node_modules/pkg/index.js")))
	assert.Equal(t, "deep", read(t, filepath.Join(dst, "web/node_modules/pkg/deep/file.txt")))
	assert.NoFileExists(t, filepath.Join(dst, "web/src/app.ts"))
}

func TestCopyMissingEntryIsSkippedNotFailed(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	tree(t, src, map[string]string{".env": "x"})

	res := Copy(src, dst, []string{".env.local", ".env"})

	assert.Equal(t, []string{".env"}, res.Copied)
	assert.Equal(t, []string{".env.local"}, res.Missing)
	assert.Empty(t, res.Failed)
}

// Symlinks are copied as symlinks. The old copy dropped linked directories
// and flattened linked files, which breaks node_modules/.bin.
func TestCopyKeepsSymlinksAsSymlinks(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	tree(t, src, map[string]string{"node_modules/pkg/cli.js": "#!/usr/bin/env node"})
	require.NoError(t, os.MkdirAll(filepath.Join(src, "node_modules/.bin"), 0o755))
	require.NoError(t, os.Symlink("../pkg/cli.js", filepath.Join(src, "node_modules/.bin/pkg")))
	// A link to a directory, which the old copy skipped entirely.
	require.NoError(t, os.Symlink("pkg", filepath.Join(src, "node_modules/alias")))

	for _, name := range []string{"clone", "portable"} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(dst, name)
			require.NoError(t, os.MkdirAll(out, 0o755))
			if name == "clone" {
				Copy(src, out, []string{"node_modules"})
			} else {
				info, err := os.Lstat(filepath.Join(src, "node_modules"))
				require.NoError(t, err)
				require.NoError(t, copyTree(filepath.Join(src, "node_modules"), filepath.Join(out, "node_modules"), info))
			}

			for link, target := range map[string]string{".bin/pkg": "../pkg/cli.js", "alias": "pkg"} {
				got, err := os.Readlink(filepath.Join(out, "node_modules", link))
				require.NoError(t, err, "%s must still be a symlink", link)
				assert.Equal(t, target, got)
			}
			assert.Equal(t, "#!/usr/bin/env node", read(t, filepath.Join(out, "node_modules/.bin/pkg")),
				"the copied link must resolve inside the copy")
		})
	}
}

// A symlink pointing OUTSIDE the source is copied as a link, never followed —
// copying it must not drag the outside target in.
func TestCopyDoesNotFollowSymlinkOutOfSource(t *testing.T) {
	outside := t.TempDir()
	tree(t, outside, map[string]string{"secret": "do not copy"})
	src, dst := t.TempDir(), t.TempDir()
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret"), filepath.Join(src, "link")))

	res := Copy(src, dst, []string{"link"})
	require.Equal(t, []string{"link"}, res.Copied)

	info, err := os.Lstat(filepath.Join(dst, "link"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "must be copied as a link, not as the target's bytes")
}

func TestCopyReplacesWhatTheCheckoutCreated(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	tree(t, src, map[string]string{"config/local.yaml": "from source"})
	tree(t, dst, map[string]string{"config/local.yaml": "from checkout", "config/other.yaml": "keep"})

	res := Copy(src, dst, []string{"config/local.yaml"})

	require.Equal(t, []string{"config/local.yaml"}, res.Copied)
	assert.Equal(t, "from source", read(t, filepath.Join(dst, "config/local.yaml")))
	assert.Equal(t, "keep", read(t, filepath.Join(dst, "config/other.yaml")), "siblings are untouched")
}

func TestCopyPreservesExecutableBit(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh"), 0o755))

	info, err := os.Lstat(filepath.Join(src, "run.sh"))
	require.NoError(t, err)
	require.NoError(t, copyTree(filepath.Join(src, "run.sh"), filepath.Join(dst, "run.sh"), info))

	got, err := os.Stat(filepath.Join(dst, "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), got.Mode().Perm())
}

package workflowref

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

func TestReadSourceLayoutRejectsEscapes(t *testing.T) {
	for _, kind := range []string{"file", "directory", "workflows-directory", "config-directory"} {
		t.Run(kind, func(t *testing.T) {
			repo, external := t.TempDir(), t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(external, "secret.yaml"), []byte("name: secret\n"), 0600))
			dir := filepath.Join(repo, filepath.FromSlash(Dir))
			require.NoError(t, os.MkdirAll(dir, 0700))
			switch kind {
			case "file":
				require.NoError(t, os.Symlink(filepath.Join(external, "secret.yaml"), filepath.Join(dir, "leak.yaml")))
			case "directory":
				require.NoError(t, os.Symlink(external, filepath.Join(dir, "leak")))
			case "workflows-directory":
				require.NoError(t, os.Remove(dir))
				require.NoError(t, os.Symlink(external, dir))
			case "config-directory":
				require.NoError(t, os.RemoveAll(filepath.Join(repo, ".reliant")))
				require.NoError(t, os.Symlink(external, filepath.Join(repo, ".reliant")))
			}
			layout, err := ReadSourceLayout(context.Background(), repo)
			// An absent workflows directory behind an escaped .reliant symlink
			// must fail confinement, not be mistaken for a normal absent source.
			require.Error(t, err)
			require.Nil(t, layout)
		})
	}
}

func TestReadLayoutBoundsAndCancellation(t *testing.T) {
	t.Run("oversized workflow", func(t *testing.T) {
		l, err := ReadLayout(fstest.MapFS{"large.yaml": {Data: []byte(strings.Repeat("x", MaxWorkflowFileBytes+1))}})
		require.ErrorContains(t, err, "exceeds")
		require.Nil(t, l)
	})
	t.Run("aggregate", func(t *testing.T) {
		files := fstest.MapFS{}
		for i := 0; i < MaxWorkflowLayoutBytes/MaxWorkflowFileBytes+1; i++ {
			files[fmt.Sprintf("%d.yaml", i)] = &fstest.MapFile{Data: make([]byte, MaxWorkflowFileBytes)}
		}
		l, err := ReadLayout(files)
		require.ErrorContains(t, err, "layout exceeds")
		require.Nil(t, l)
	})
	t.Run("entry count", func(t *testing.T) {
		files := fstest.MapFS{}
		for i := 0; i < MaxWorkflowLayoutEntries; i++ {
			files[fmt.Sprintf("%d.txt", i)] = &fstest.MapFile{}
		}
		l, err := ReadLayout(files)
		require.ErrorContains(t, err, "entries")
		require.Nil(t, l)
	})
	t.Run("depth", func(t *testing.T) {
		l, err := ReadLayout(fstest.MapFS{"a/b/c/d/e.yaml": {Data: []byte("name: deep")}})
		require.ErrorContains(t, err, "depth")
		require.Nil(t, l)
	})
	t.Run("unreadable", func(t *testing.T) {
		l, err := ReadLayout(deniedLayoutFS{})
		require.ErrorIs(t, err, fs.ErrPermission)
		require.Nil(t, l)
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		l, err := ReadLayoutContext(ctx, fstest.MapFS{})
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, l)
	})
	t.Run("special YAML", func(t *testing.T) {
		l, err := ReadLayout(fstest.MapFS{"pipe.yaml": {Mode: fs.ModeNamedPipe}})
		require.ErrorContains(t, err, "regular file")
		require.Nil(t, l)
	})
}

// A successful read is still incomplete when closing the file/directory
// fails. Preserve both errors if the content read also failed.
func TestReadLayoutPropagatesCloseErrors(t *testing.T) {
	closeErr := errors.New("close failed")
	readErr := errors.New("read failed")
	for _, tc := range []struct {
		name, target string
		readErr      error
	}{
		{name: "directory", target: "."},
		{name: "file", target: "flow.yaml"},
		{name: "read and close", target: "flow.yaml", readErr: readErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := closeErrorFS{FS: fstest.MapFS{"flow.yaml": {Data: []byte(wf("flow"))}}, target: tc.target, closeErr: closeErr, readErr: tc.readErr}
			layout, err := ReadLayout(fsys)
			require.ErrorIs(t, err, closeErr)
			if tc.readErr != nil {
				require.ErrorIs(t, err, readErr)
			}
			require.Nil(t, layout)
		})
	}
}

type closeErrorFS struct {
	fs.FS
	target            string
	closeErr, readErr error
}

func (s closeErrorFS) Open(name string) (fs.File, error) {
	f, err := s.FS.Open(name)
	if err != nil || name != s.target {
		return f, err
	}
	wrapped := closeErrorFile{File: f, closeErr: s.closeErr, readErr: s.readErr}
	if dir, ok := f.(fs.ReadDirFile); ok {
		return closeErrorDir{closeErrorFile: wrapped, dir: dir}, nil
	}
	return wrapped, nil
}

type closeErrorFile struct {
	fs.File
	closeErr, readErr error
}

func (f closeErrorFile) Close() error { return errors.Join(f.File.Close(), f.closeErr) }
func (f closeErrorFile) Read(p []byte) (int, error) {
	if f.readErr != nil {
		return 0, f.readErr
	}
	return f.File.Read(p)
}

type closeErrorDir struct {
	closeErrorFile
	dir fs.ReadDirFile
}

func (d closeErrorDir) ReadDir(n int) ([]fs.DirEntry, error) { return d.dir.ReadDir(n) }

type deniedLayoutFS struct{}

func (deniedLayoutFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestMergedLayoutRejectsOrphansInTheirSource(t *testing.T) {
	valid := map[string][]byte{"flow.yaml": []byte(wf("flow"))}
	orphan := map[string][]byte{"flow/scenarios/orphan.yaml": []byte("name: orphan")}
	duplicate := map[string][]byte{"a.yaml": []byte(wf("flow")), "b.yaml": []byte(wf("flow")), "flow/scenarios/orphan.yaml": []byte("name: orphan")}
	for _, tc := range []struct {
		name         string
		root, nested map[string][]byte
	}{
		{"root orphan", orphan, valid}, {"nested orphan", valid, orphan},
		{"root duplicate", duplicate, valid}, {"nested duplicate", valid, duplicate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout := MergeLayouts(NewLayout(tc.root), map[string]*Layout{"repo": NewLayout(tc.nested)})
			require.Empty(t, layout.ScenariosFor("flow"))
			require.Empty(t, layout.ScenariosFor("repo/flow"))
			require.Len(t, layout.ScenarioProblems(), 1)
			require.Contains(t, layout.ScenarioProblems()[0].Error(), "source directory")
		})
	}
}

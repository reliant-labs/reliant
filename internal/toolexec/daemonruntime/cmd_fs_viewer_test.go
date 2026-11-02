// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonpolicy"
)

// The file viewer's reads (they carry viewer_root) refuse a credential store
// on every command the viewer sends, through a symlink too; a project file
// still opens, and a read from any other caller is unchanged.
func TestFSViewerReadsRefuseCredentialStores(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	key := filepath.Join(home, ".ssh", "id_rsa")
	project := filepath.Join(home, "proj")
	readme := filepath.Join(project, "README.md")
	for _, f := range []string{key, readme} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("contents\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(project, "key")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}

	handlers := map[string]CommandHandler{
		"fs.read_file":        handleFSReadFile,
		"fs.read_binary_file": handleFSReadBinaryFile,
		"fs.stat":             handleFSStat,
		"fs.preview_info":     handleFSPreviewInfo,
	}
	call := func(h CommandHandler, path, root string) error {
		payload, err := json.Marshal(map[string]any{"path": path, "viewer_root": root, "max_bytes": 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		_, err = h(context.Background(), payload)
		return err
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			for _, p := range []string{key, link} {
				if err := call(h, p, project); !errors.Is(err, daemonpolicy.ErrViewerCredential) {
					t.Errorf("viewer %s(%s) = %v, want ErrViewerCredential", name, p, err)
				}
			}
			if err := call(h, readme, project); err != nil {
				t.Errorf("viewer %s of a project file: %v", name, err)
			}
			// No viewer_root: an agent's own read, which this guard leaves alone.
			if err := call(h, key, ""); err != nil {
				t.Errorf("non-viewer %s(%s) = %v, want it unchanged", name, key, err)
			}
		})
	}
}

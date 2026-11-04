package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A pinned buf must replace the program exec.Command resolved off forge's own
// PATH — prepending its directory to the CHILD's PATH changes nothing about
// which binary the parent execs — and clear the not-found a missing buf left
// in cmd.Err.
func TestWithPinnedTools_ExecsThePinnedBuf(t *testing.T) {
	root := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "buf")
	pinMu.Lock()
	pinCache[root+"\x00"+os.Getenv("PATH")] = &codegenTools{Dirs: []string{filepath.Dir(pinned)}, paths: map[string]string{"buf": pinned}}
	pinMu.Unlock()
	t.Cleanup(func() {
		pinMu.Lock()
		delete(pinCache, root+"\x00"+os.Getenv("PATH"))
		pinMu.Unlock()
	})

	cmd := exec.Command("buf", "generate")
	cmd.Err = exec.ErrNotFound // what exec.Command records when PATH has no buf
	if err := withPinnedTools(cmd, root); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != pinned || cmd.Err != nil {
		t.Errorf("cmd.Path = %q, cmd.Err = %v; want the pinned %s and no error", cmd.Path, cmd.Err, pinned)
	}

	other := exec.Command("go", "version")
	goPath := other.Path
	if err := withPinnedTools(other, root); err != nil {
		t.Fatal(err)
	}
	if other.Path != goPath {
		t.Errorf("a non-buf command was repointed to %q", other.Path)
	}
}

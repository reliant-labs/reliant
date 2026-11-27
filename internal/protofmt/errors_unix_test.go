//go:build !windows

package protofmt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test relies on")
	}
}

// A proto buf could format but forge cannot write back is reported with its
// path, and nothing is claimed as settled.
func TestSettleReportsAProtoItCannotWriteBack(t *testing.T) {
	skipIfRoot(t)
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	end := Begin(root, nil)
	defer end()
	write(t, root, "proto/a/v1/a.proto", spaced)
	dir := filepath.Join(root, "proto", "a", "v1")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	changed, err := Settle(context.Background(), root)
	if err == nil || len(changed) != 0 {
		t.Fatalf("Settle = %v, %v; want the write failure and nothing settled", changed, err)
	}
	if got := read(t, root, "proto/a/v1/a.proto"); got != spaced {
		t.Errorf("the file changed although it could not be written: %q", got)
	}
}

// With no usable scratch directory buf cannot be handed the source, and the
// error reaches the caller instead of the proto being silently left alone.
func TestSettleReportsAScratchDirectoryThatCannotBeCreated(t *testing.T) {
	useFakeBuf(t)
	missing := filepath.Join(t.TempDir(), "no-such-tmp")
	t.Setenv("TMPDIR", missing)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	end := Begin(root, nil)
	defer end()
	write(t, root, "proto/a/v1/a.proto", spaced)
	_, err := Settle(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "proto/a/v1/a.proto") {
		t.Errorf("err = %v, want the file named", err)
	}
	if _, ferr := Format(context.Background(), "buf", []byte(spaced)); ferr == nil {
		t.Error("Format must fail when it has nowhere to write the source")
	}
}

// A proto that cannot be read is simply not tracked; the rest still are.
func TestReadProtosSkipsAnUnreadableFile(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	write(t, root, "proto/a/v1/a.proto", canonical)
	write(t, root, "proto/b/v1/b.proto", canonical)
	if err := os.Chmod(filepath.Join(root, "proto/b/v1/b.proto"), 0); err != nil {
		t.Fatal(err)
	}
	got := readProtos(root)
	var names []string
	for k := range got {
		names = append(names, k)
	}
	if !slices.Equal(names, []string{"proto/a/v1/a.proto"}) {
		t.Errorf("readProtos = %v, want only the readable proto", names)
	}
}

// A root that cannot be made absolute (the working directory is gone) is used
// as given rather than failing the session.
func TestAbsRootFallsBackToTheGivenPath(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Skipf("cannot remove the working directory here: %v", err)
	}
	if got := absRoot("rel/dir"); got != "rel/dir" {
		t.Errorf("absRoot = %q, want the relative path unchanged", got)
	}
}

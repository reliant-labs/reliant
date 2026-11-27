package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/reliant-labs/forge/pkg/querycapture"
)

func TestFrameResolverExpandsWindowsShortPathAboveDeletedDirectories(t *testing.T) {
	t.Parallel()
	repo := filepath.Join(t.TempDir(), "repository with a long name")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	longName, err := windows.UTF16PtrFromString(repo)
	if err != nil {
		t.Fatal(err)
	}
	n, err := windows.GetShortPathName(longName, nil, 0)
	if err != nil || n == 0 {
		t.Fatalf("size short path name: %d, %v", n, err)
	}
	buf := make([]uint16, n)
	if _, err := windows.GetShortPathName(longName, &buf[0], n); err != nil {
		t.Fatal(err)
	}
	shortName := windows.UTF16ToString(buf)
	root := realPath(repo)
	if strings.EqualFold(shortName, root) {
		t.Skip("filesystem did not assign an alternate short name")
	}
	r := frameResolver{repoRoot: root, projectDir: root}
	frame := querycapture.Frame{AbsFile: filepath.Join(shortName, "removed", "nested", "query.go")}
	if got, ok := r.repoPath(frame); !ok || got != "removed/nested/query.go" {
		t.Fatalf("resolve %q under %q: %q, %v", frame.AbsFile, root, got, ok)
	}
}

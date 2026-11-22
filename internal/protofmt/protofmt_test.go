package protofmt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/pkg/testkit"
)

const unformatted = "syntax = \"proto3\";\npackage a.v1;\nmessage   A {string id = 1;}\n"

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Without a session nothing is settled: only a command that declared it
// writes protos has edits to settle, so a hand edit is never reformatted by
// an unrelated forge command.
func TestSettleWithoutSessionIsANoop(t *testing.T) {
	root := t.TempDir()
	write(t, root, "buf.yaml", "version: v2\nmodules:\n  - path: proto\n")
	write(t, root, "proto/a/v1/a.proto", unformatted)
	if changed, err := Settle(context.Background(), root); err != nil || changed != nil {
		t.Fatalf("Settle with no session: %v %v", changed, err)
	}
	if got := read(t, root, "proto/a/v1/a.proto"); got != unformatted {
		t.Errorf("rewritten without a session:\n%s", got)
	}
}

// A proto the command creates is formatted; forge's vendored copies are not
// touched, whatever buf would make of them.
func TestSessionFormatsCreatedProtosButNotVendoredCopies(t *testing.T) {
	testkit.Slow(t, "execs buf")
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf not on PATH")
	}
	root := t.TempDir()
	write(t, root, "buf.yaml", "version: v2\nmodules:\n  - path: proto\n")
	end := Begin(root, nil)
	write(t, root, "proto/a/v1/a.proto", unformatted)
	write(t, root, assets.ForgeProtoVendorRelPath, unformatted)
	end()

	want := "syntax = \"proto3\";\npackage a.v1;\nmessage A {\n  string id = 1;\n}\n"
	if got := read(t, root, "proto/a/v1/a.proto"); got != want {
		t.Errorf("created proto not settled to buf's output:\n got %q\nwant %q", got, want)
	}
	if got := read(t, root, assets.ForgeProtoVendorRelPath); got != unformatted {
		t.Errorf("the vendored copy was rewritten:\n%s", got)
	}
}

package protofmt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

const (
	spaced    = "message   A {\n}\n"
	canonical = "message A {\n}\n"
)

func TestFormatReturnsTheFormattersOutput(t *testing.T) {
	useFakeBuf(t)
	exe, _ := os.Executable()
	got, err := Format(context.Background(), exe, []byte(spaced))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != canonical {
		t.Errorf("Format = %q, want %q", got, canonical)
	}
}

func TestFormatReportsTheFormattersStderr(t *testing.T) {
	useFakeBuf(t)
	exe, _ := os.Executable()
	_, err := Format(context.Background(), exe, []byte("BROKEN"))
	if err == nil || !strings.Contains(err.Error(), "buf format") || !strings.Contains(err.Error(), "syntax error: BROKEN") {
		t.Fatalf("err = %v, want a buf format error carrying buf's stderr", err)
	}
}

func TestFormatWithMissingBinaryFails(t *testing.T) {
	_, err := Format(context.Background(), filepath.Join(t.TempDir(), "no-such-buf"), []byte(spaced))
	if err == nil {
		t.Fatal("expected an error when the buf binary does not exist")
	}
}

func TestFormatHonoursCancelledContext(t *testing.T) {
	useFakeBuf(t)
	exe, _ := os.Executable()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Format(ctx, exe, []byte(spaced)); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

// The unavailable-buf warning is given once per session, and the error still
// comes back each time so a caller can tell the settle did not happen.
func TestSettleWarnsOnceWhenBufCannotRun(t *testing.T) {
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	boom := errors.New("no buf for you")
	old := resolveBin
	resolveBin = func(context.Context, string) (string, error) { return "", boom }
	t.Cleanup(func() { resolveBin = old })

	var warn bytes.Buffer
	end := Begin(root, &warn)
	defer end()
	write(t, root, "proto/a/v1/a.proto", spaced)

	for i := range 2 {
		changed, err := Settle(context.Background(), root)
		if !errors.Is(err, boom) || changed != nil {
			t.Fatalf("settle %d = %v, %v; want nil, %v", i, changed, err, boom)
		}
	}
	if n := strings.Count(warn.String(), "could not run buf format"); n != 1 {
		t.Errorf("warning printed %d times, want 1:\n%s", n, warn.String())
	}
	if !strings.Contains(warn.String(), "no buf for you") || !strings.Contains(warn.String(), "buf format -w") {
		t.Errorf("warning should name the cause and the manual fix:\n%s", warn.String())
	}
	if got := read(t, root, "proto/a/v1/a.proto"); got != spaced {
		t.Errorf("file touched although buf could not run: %q", got)
	}
}

// A project with no buf.yaml has no format to converge on.
func TestSettleWithoutBufModuleLeavesFilesAlone(t *testing.T) {
	useFakeBuf(t)
	root := t.TempDir()
	end := Begin(root, nil)
	defer end()
	write(t, root, "proto/a/v1/a.proto", spaced)
	changed, err := Settle(context.Background(), root)
	if err != nil || changed != nil {
		t.Fatalf("Settle = %v, %v; want nil, nil", changed, err)
	}
	if got := read(t, root, "proto/a/v1/a.proto"); got != spaced {
		t.Errorf("file rewritten without a buf module: %q", got)
	}
}

func TestSettleWithNothingEditedIsANoop(t *testing.T) {
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	write(t, root, "proto/a/v1/a.proto", spaced)
	end := Begin(root, nil)
	defer end()
	changed, err := Settle(context.Background(), root)
	if err != nil || changed != nil {
		t.Fatalf("Settle = %v, %v; want nil, nil", changed, err)
	}
	if got := read(t, root, "proto/a/v1/a.proto"); got != spaced {
		t.Errorf("an untouched proto was reformatted: %q", got)
	}
}

// A created proto is always formatted, an edit to a buf-clean proto is
// formatted, an edit to a proto that was already unformatted is the author's,
// and a proto that does not parse is reported without being touched. The
// others in the same settle still land.
func TestSettleDecidesPerFile(t *testing.T) {
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	write(t, root, "proto/clean/v1/clean.proto", canonical)
	write(t, root, "proto/drift/v1/drift.proto", spaced)
	write(t, root, "proto/gone/v1/gone.proto", canonical)
	write(t, root, "proto/oldbroken/v1/oldbroken.proto", "BROKEN\n")

	var warn bytes.Buffer
	end := Begin(root, &warn)
	defer end()

	write(t, root, "proto/created/v1/created.proto", spaced)
	write(t, root, "proto/clean/v1/clean.proto", "message   A {\n  string  id = 1;\n}\n")
	write(t, root, "proto/drift/v1/drift.proto", spaced+"// edit\n")
	write(t, root, "proto/oldbroken/v1/oldbroken.proto", "BROKEN edit\n")
	write(t, root, "proto/bad/v1/bad.proto", "BROKEN   new\n")
	if err := os.Remove(filepath.Join(root, "proto/gone/v1/gone.proto")); err != nil {
		t.Fatal(err)
	}

	changed, err := Settle(context.Background(), root)
	slices.Sort(changed)
	if want := []string{"proto/clean/v1/clean.proto", "proto/created/v1/created.proto"}; !slices.Equal(changed, want) {
		t.Errorf("changed = %v, want %v", changed, want)
	}
	if err == nil || !strings.Contains(err.Error(), "proto/bad/v1/bad.proto") || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("err = %v, want the unparseable file named with buf's message", err)
	}
	for rel, want := range map[string]string{
		"proto/created/v1/created.proto":     canonical,
		"proto/clean/v1/clean.proto":         "message A {\nstring id = 1;\n}\n",
		"proto/drift/v1/drift.proto":         spaced + "// edit\n",
		"proto/oldbroken/v1/oldbroken.proto": "BROKEN edit\n",
		"proto/bad/v1/bad.proto":             "BROKEN   new\n",
	} {
		if got := read(t, root, rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if !strings.Contains(warn.String(), "settled 2 proto file(s)") {
		t.Errorf("settle should report what it formatted:\n%s", warn.String())
	}

	// Settled files are the new baseline: a second settle has nothing to do.
	changed, err = Settle(context.Background(), root)
	if changed != nil {
		t.Errorf("second settle rewrote %v", changed)
	}
	if err != nil && strings.Contains(err.Error(), "created.proto") {
		t.Errorf("second settle revisited a settled file: %v", err)
	}
}

func TestSettlePreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no unix permission bits")
	}
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	end := Begin(root, nil)
	defer end()
	write(t, root, "proto/a/v1/a.proto", spaced)
	if err := os.Chmod(filepath.Join(root, "proto/a/v1/a.proto"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Settle(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "proto/a/v1/a.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

// A nested Begin belongs to the outer command: its end must not settle or
// close the outer session.
func TestNestedBeginIsANoop(t *testing.T) {
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	outerEnd := Begin(root, nil)
	innerEnd := Begin(root, nil)
	write(t, root, "proto/a/v1/a.proto", spaced)
	innerEnd()
	if got := read(t, root, "proto/a/v1/a.proto"); got != spaced {
		t.Fatalf("the nested end settled the outer session: %q", got)
	}
	outerEnd()
	if got := read(t, root, "proto/a/v1/a.proto"); got != canonical {
		t.Errorf("outer end did not settle: %q", got)
	}
	// The session is closed: a later settle does nothing.
	write(t, root, "proto/b/v1/b.proto", spaced)
	if changed, err := Settle(context.Background(), root); changed != nil || err != nil {
		t.Errorf("Settle after end = %v, %v; want nil, nil", changed, err)
	}
}

// Sessions are keyed by absolute path, so a relative root and its absolute
// form are the same project.
func TestSessionKeyedByAbsolutePath(t *testing.T) {
	useFakeBuf(t)
	root := t.TempDir()
	write(t, root, "buf.yaml", bufYAML)
	t.Chdir(filepath.Dir(root))
	rel := filepath.Base(root)
	end := Begin(rel, nil)
	write(t, root, "proto/a/v1/a.proto", spaced)
	changed, err := Settle(context.Background(), root)
	if err != nil || !slices.Equal(changed, []string{"proto/a/v1/a.proto"}) {
		t.Errorf("Settle(abs) = %v, %v; want the file begun via the relative path", changed, err)
	}
	end()
}

// readProtos tracks proto files only, skipping vendored trees and the
// directories nothing of forge's lives in.
func TestReadProtosSkipsNonProtoAndExcludedDirs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "proto/a/v1/a.proto", canonical)
	write(t, root, "proto/a/v1/README.md", "not a proto")
	write(t, root, "proto/node_modules/x/x.proto", canonical)
	write(t, root, "proto/testdata/t.proto", canonical)
	write(t, root, "proto/.git/g.proto", canonical)
	write(t, root, "other/o.proto", canonical)

	got := readProtos(root)
	if len(got) != 1 || string(got["proto/a/v1/a.proto"]) != canonical {
		t.Errorf("readProtos = %v, want only proto/a/v1/a.proto", got)
	}
	if got := readProtos(filepath.Join(root, "missing")); len(got) != 0 {
		t.Errorf("readProtos of a missing tree = %v, want none", got)
	}
}

func TestFileExistsIsFalseForDirectories(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f", "x")
	if !fileExists(filepath.Join(dir, "f")) {
		t.Error("a file should exist")
	}
	if fileExists(dir) || fileExists(filepath.Join(dir, "none")) {
		t.Error("a directory or a missing path is not a file")
	}
}

// A project that does not pin buf in go.mod formats with the buf on PATH.
func TestResolveBinPrefersBufOnPathWhenProjectDoesNotPinIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("writes an executable shell stub")
	}
	bin := t.TempDir()
	stub := filepath.Join(bin, "buf")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	got, err := resolveBin(context.Background(), t.TempDir())
	if err != nil || got != stub {
		t.Errorf("resolveBin = %q, %v; want %q", got, err, stub)
	}
}

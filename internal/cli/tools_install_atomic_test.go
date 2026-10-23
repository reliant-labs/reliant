package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// `forge tools install --force` replaces a binary in GOBIN that another
// process may be starting at that moment (a parallel generate's buf or
// protoc-gen-go). A reader must see the old binary or the new one, never a
// half-written file: `go install` straight into GOBIN rewrote it in place.
func TestGoInstallTool_ReplacesTheGOBINBinaryAtomically(t *testing.T) {
	gobin := t.TempDir()
	t.Setenv("GOBIN", gobin)
	t.Setenv("PATH", gobin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bin := filepath.Join(gobin, "protoc-gen-fake"+exeSuffix())
	old := strings.Repeat("o", 64)
	if err := os.WriteFile(bin, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("n", 64)
	prev := goInstallPinned
	t.Cleanup(func() { goInstallPinned = prev })
	// A slow writer: the binary appears a byte at a time, as a copy does.
	goInstallPinned = func(_ context.Context, _, dir string) error {
		f, err := os.OpenFile(filepath.Join(dir, "protoc-gen-fake"+exeSuffix()), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		for i := range full {
			if _, err := f.WriteString(full[i : i+1]); err != nil {
				return err
			}
			time.Sleep(time.Millisecond)
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- goInstallTool(context.Background(), "protoc-gen-fake", "example.com/fake/cmd/protoc-gen-fake@v1.0.0", true)
	}()
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
		}
		if b, err := os.ReadFile(bin); err == nil && string(b) != old && string(b) != full {
			t.Fatalf("a reader saw a partial binary (%d bytes, neither the old nor the new one) during the install", len(b))
		}
	}
	if b, err := os.ReadFile(bin); err != nil || string(b) != full {
		t.Fatalf("GOBIN binary = %q, %v; want the new one", b, err)
	}
	if left, _ := filepath.Glob(filepath.Join(gobin, ".forge-install-*")); len(left) > 0 {
		t.Errorf("the install left its staging dir behind: %v", left)
	}
}

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The codegen tools are installed at the version go.mod resolves, not
// @latest. protoc-gen-go stamps its own version into every file it writes
// (`// protoc-gen-go v1.36.12`), so a verify-generated run whose plugin came
// from @latest reports every stub as drifted the day a new plugin release
// ships — a failure caused by the calendar, not by the code.
func TestResolveToolVersion_FromGoMod(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	goMod := "module example.com/p\n\ngo 1.24\n\nrequire google.golang.org/protobuf v1.36.12\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOPROXY", "off")
	ctx := context.Background()

	protocGenGo := requiredProtoTools[0]
	if got := resolveToolVersion(ctx, dir, protocGenGo, ""); got != "v1.36.12" {
		t.Errorf("protoc-gen-go resolved to %q, want go.mod's v1.36.12", got)
	}
	// A module the graph does not contain falls back to latest rather than
	// failing the install.
	connect := requiredProtoTools[1]
	if got := resolveToolVersion(ctx, dir, connect, ""); got != "latest" {
		t.Errorf("a tool absent from go.mod resolved to %q, want latest", got)
	}
	// An explicit --version wins.
	if got := resolveToolVersion(ctx, dir, protocGenGo, "v1.30.0"); got != "v1.30.0" {
		t.Errorf("--version was ignored: got %q", got)
	}
}

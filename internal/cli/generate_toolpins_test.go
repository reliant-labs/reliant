package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const modLineOut = `/x/protoc-gen-go: go1.24.0
	path	google.golang.org/protobuf/cmd/protoc-gen-go
	mod	google.golang.org/protobuf	v1.36.11	h1:abc=
	build	-ldflags=-s
`

const depLineOut = `/x/protoc-gen-go: go1.24.0
	path	google.golang.org/protobuf/cmd/protoc-gen-go
	mod	github.com/reliant-labs/reliant	(devel)
	dep	google.golang.org/protobuf	v1.36.12	h1:def=
	dep	golang.org/x/tools	v0.30.0	h1:ghi=
`

func TestParseBuildInfoVersion(t *testing.T) {
	cases := []struct {
		name, out, module, want string
		ok                      bool
	}{
		{"mod line", modLineOut, "google.golang.org/protobuf", "v1.36.11", true},
		{"dep line", depLineOut, "google.golang.org/protobuf", "v1.36.12", true},
		{"other dep", depLineOut, "golang.org/x/tools", "v0.30.0", true},
		{"absent", modLineOut, "connectrpc.com/connect", "", false},
		{"prefix is not a match", depLineOut, "google.golang.org/proto", "", false},
		{"empty", "", "google.golang.org/protobuf", "", false},
	}
	for _, c := range cases {
		got, ok := parseBuildInfoVersion(c.out, c.module)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestPathPrepended(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := pathPrepended([]string{"A=1", "PATH=/usr/bin"}, []string{"/c1", "/c2"})
	if got[1] != "PATH=/c1"+sep+"/c2"+sep+"/usr/bin" {
		t.Errorf("PATH not prepended: %v", got)
	}
	if got := pathPrepended([]string{"A=1"}, []string{"/c1"}); got[1] != "PATH=/c1" {
		t.Errorf("missing PATH not added: %v", got)
	}
	in := []string{"PATH=/usr/bin"}
	if got := pathPrepended(in, nil); got[0] != "PATH=/usr/bin" {
		t.Errorf("no dirs must be a no-op: %v", got)
	}
}

func TestPinErrorNamesFix(t *testing.T) {
	err := pinError(requiredProtoTools[0], "v1.36.12", "PATH has v1.36.11", context.Canceled)
	for _, want := range []string{"protoc-gen-go", "v1.36.11", "v1.36.12", "forge tools install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// A real binary built from this module: toolBinVersion must read the module
// version from build info, and a version mismatch must be detected.
func TestToolMatchesRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs go build; runs in task test")
	}
	bin := filepath.Join(t.TempDir(), "forgebin")
	build := exec.Command("go", "build", "-o", bin, "github.com/reliant-labs/forge/cmd/forge")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build probe binary: %v\n%s", err, out)
	}
	tool := protoTool{Binary: "forgebin", VersionModule: "github.com/reliant-labs/forge"}
	have, found := toolBinVersion(context.Background(), bin, tool.VersionModule)
	if !found {
		t.Skip("probe binary has no build info for its main module")
	}
	if _, ok := toolMatches(context.Background(), bin, tool, have); !ok {
		t.Errorf("binary at %s should match itself", have)
	}
	if _, ok := toolMatches(context.Background(), bin, tool, "v0.0.0-nope"); ok {
		t.Error("mismatched version matched")
	}
}

// Parallel forge processes pin the same tool into one cache dir. A reader
// must never see a binary another install is still writing: that is how
// `go version -m` read a half-written protoc-gen-connect-go, the pin said
// "installed binary reports unknown", and a fresh project's bootstrap
// generate failed in CI (TestE2EFixtureCorpusKalshiShaped).
func TestInstallPinnedTool_ReadersNeverSeeAPartialBinary(t *testing.T) {
	full := strings.Repeat("x", 64)
	prev := goInstallPinned
	t.Cleanup(func() { goInstallPinned = prev })
	// A slow writer: the binary appears a byte at a time, like a copy.
	goInstallPinned = func(_ context.Context, _, gobin string) error {
		f, err := os.Create(filepath.Join(gobin, "protoc-gen-fake"+exeSuffix()))
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
	tool := protoTool{Binary: "protoc-gen-fake", Module: "example.com/fake/cmd/protoc-gen-fake", VersionModule: "example.com/fake"}
	dir := t.TempDir()
	bin := filepath.Join(dir, tool.Binary+exeSuffix())

	done := make(chan error, 1)
	go func() { done <- installPinnedTool(context.Background(), tool, "v1.0.0", dir, "not found on PATH") }()
	for running := true; running; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			running = false
		default:
		}
		if b, err := os.ReadFile(bin); err == nil && string(b) != full {
			t.Fatalf("a reader saw a partial binary (%d of %d bytes) while it was being installed", len(b), len(full))
		}
	}
	if b, err := os.ReadFile(bin); err != nil || string(b) != full {
		t.Fatalf("installed binary = %q, %v; want the complete file", b, err)
	}
}

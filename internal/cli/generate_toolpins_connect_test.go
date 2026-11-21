package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// `go version -m` of each major's protoc-gen-connect-go build.
const (
	connectV1PluginInfo = "/x/protoc-gen-connect-go: go1.26.0\n" +
		"\tpath\tconnectrpc.com/connect/cmd/protoc-gen-connect-go\n" +
		"\tmod\tconnectrpc.com/connect\tv1.21.1\th1:x=\n"
	connectV2PluginInfo = "/x/protoc-gen-connect-go: go1.26.0\n" +
		"\tpath\tconnectrpc.com/connect/v2/cmd/protoc-gen-connect-go\n" +
		"\tmod\tconnectrpc.com/connect/v2\tv2.0.2\th1:x=\n"
)

// untidyV1GoMod requires connect-go v1 and names protobuf twice at different
// versions — the shape an appended `require` leaves — so `go list -m` fails
// with "updates to go.mod needed" and resolveToolVersion says "latest".
const untidyV1GoMod = `module example.com/untidy

go 1.24

require (
	connectrpc.com/connect v1.21.1
	google.golang.org/protobuf v1.36.11
)

require google.golang.org/protobuf v0.0.0
`

// stubGoVersionM answers `go version -m` from infos, keyed by binary path; a
// path it does not name is not a Go binary.
func stubGoVersionM(t *testing.T, infos map[string]string) {
	t.Helper()
	prev := goVersionM
	t.Cleanup(func() { goVersionM = prev })
	goVersionM = func(_ context.Context, binPath string) (string, error) {
		if info, ok := infos[binPath]; ok {
			return info, nil
		}
		return "", errors.New("not a go binary")
	}
}

func connectPinGoMod(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// When the module graph cannot name a version, protoc-gen-connect-go must
// still never be the other major's build: v2's emits Register<Svc>Handler
// into a v1 project whose handlers call New<Svc>Handler (forge main's e2e
// suite, after `forge tools install` in forge's own checkout put v2's plugin
// on PATH). Every other tool keeps whatever PATH holds.
func TestUnresolvedToolPin_ConnectPluginNeverTheOtherMajor(t *testing.T) {
	v1 := connectTool(t, requiredProtoToolsFor(connectmajor.V1))
	v2 := connectTool(t, requiredProtoToolsFor(connectmajor.V2))
	stubGoVersionM(t, map[string]string{"/bin/v1": connectV1PluginInfo, "/bin/v2": connectV2PluginInfo})

	v1Dir := connectPinGoMod(t, untidyV1GoMod)
	v2Dir := connectPinGoMod(t, "module example.com/v2\n\ngo 1.24\n\nrequire connectrpc.com/connect/v2 v2.0.2\n")
	noneDir := connectPinGoMod(t, "module example.com/none\n\ngo 1.24\n")

	cases := []struct {
		name    string
		dir     string
		tool    protoTool
		pathBin string
		onPath  bool
		want    string
	}{
		{"v1 project, v2 build on PATH: pin go.mod's v1", v1Dir, v1, "/bin/v2", true, "v1.21.1"},
		{"v1 project, v1 build on PATH: PATH's", v1Dir, v1, "/bin/v1", true, ""},
		{"v1 project, nothing on PATH: pin go.mod's v1", v1Dir, v1, "", false, "v1.21.1"},
		{"v1 project, unreadable build on PATH: pin go.mod's v1", v1Dir, v1, "/bin/script", true, "v1.21.1"},
		{"v2 project, v1 build on PATH: pin go.mod's v2", v2Dir, v2, "/bin/v1", true, "v2.0.2"},
		{"v2 project, v2 build on PATH: PATH's", v2Dir, v2, "/bin/v2", true, ""},
		{"duplicate v1 requirements, higher last", connectPinGoMod(t, "module example.com/duplicate\nrequire connectrpc.com/connect v1.9.0\nrequire connectrpc.com/connect v1.21.1\n"), v1, "/bin/v2", true, "v1.21.1"},
		{"duplicate v1 requirements, higher first", connectPinGoMod(t, "module example.com/duplicate\nrequire connectrpc.com/connect v1.21.1\nrequire connectrpc.com/connect v1.9.0\n"), v1, "/bin/v2", true, "v1.21.1"},
		{"duplicate v2 requirements, stable after prerelease", connectPinGoMod(t, "module example.com/duplicate\nrequire connectrpc.com/connect/v2 v2.0.2-rc.1\nrequire connectrpc.com/connect/v2 v2.0.2\n"), v2, "/bin/v1", true, "v2.0.2"},
		{"go.mod names no connect: nothing to pin to", noneDir, v1, "/bin/v2", true, ""},
		{"no go.mod: nothing to pin to", t.TempDir(), v1, "/bin/v2", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unresolvedToolPin(context.Background(), tc.dir, tc.tool, tc.pathBin, tc.onPath); got != tc.want {
				t.Errorf("unresolvedToolPin = %q, want %q", got, tc.want)
			}
		})
	}

	// Any other tool: the module is not in the graph, so PATH's build is fine.
	for _, tool := range requiredProtoToolsFor(connectmajor.V1) {
		if tool.Binary == v1.Binary {
			continue
		}
		if got := unresolvedToolPin(context.Background(), v1Dir, tool, "/bin/v2", true); got != "" {
			t.Errorf("%s: unresolvedToolPin = %q, want \"\" (PATH's build)", tool.Binary, got)
		}
	}
}

// The whole path the e2e suite took: a v1 project whose go.mod needs a tidy,
// connect-go v2's plugin first on PATH. Generate must run v1's plugin from
// forge's tool cache, at the version go.mod requires.
func TestPinCodegenTools_UntidyV1GoModWithV2PluginOnPath(t *testing.T) {
	testkit.Slow(t, "execs go list -m against the fixture's go.mod")
	t.Setenv("FORGE_TOOLS_CACHE", t.TempDir())

	pathDir := t.TempDir()
	for _, name := range []string{"protoc-gen-connect-go", "buf"} {
		if err := os.WriteFile(filepath.Join(pathDir, name+exeSuffix()), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	pathPlugin := filepath.Join(pathDir, "protoc-gen-connect-go"+exeSuffix())

	projectDir := connectPinGoMod(t, untidyV1GoMod)
	v1 := connectTool(t, requiredProtoToolsFor(connectmajor.V1))
	if got := resolveToolVersion(context.Background(), projectDir, v1, ""); got != "latest" {
		t.Fatalf("fixture go.mod resolved %s at %q; it must need a tidy so `go list -m` fails", v1.VersionModule, got)
	}

	// PATH's plugin is v2's; whatever forge installs into its cache is v1's.
	stubGoVersionM(t, map[string]string{pathPlugin: connectV2PluginInfo})
	inner := goVersionM
	goVersionM = func(ctx context.Context, binPath string) (string, error) {
		if binPath != pathPlugin && filepath.Base(binPath) == "protoc-gen-connect-go"+exeSuffix() {
			if _, err := os.Stat(binPath); err != nil {
				return "", err
			}
			return connectV1PluginInfo, nil
		}
		return inner(ctx, binPath)
	}
	var installed []string
	prev := goInstallPinned
	t.Cleanup(func() { goInstallPinned = prev })
	goInstallPinned = func(_ context.Context, spec, gobin string) error {
		installed = append(installed, spec)
		return os.WriteFile(filepath.Join(gobin, "protoc-gen-connect-go"+exeSuffix()), []byte("v1"), 0o755)
	}

	res, err := pinCodegenTools(context.Background(), projectDir, codegenToolsMajor(projectDir))
	if err != nil {
		t.Fatal(err)
	}
	want := v1.Module + "@v1.21.1"
	if len(installed) != 1 || installed[0] != want {
		t.Fatalf("installed %v, want [%s]", installed, want)
	}
	got, _ := res.Path("protoc-gen-connect-go")
	if got == "" || got == pathPlugin || !strings.Contains(got, "protoc-gen-connect-go@v1.21.1") {
		t.Fatalf("protoc-gen-connect-go resolved to %q, want the v1.21.1 cache entry (not PATH's v2 build %s)", got, pathPlugin)
	}
}

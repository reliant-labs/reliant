package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
	"github.com/reliant-labs/forge/internal/toolcache"
)

const (
	v1ProjectGoMod = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect v1.19.0\n"
	v2ProjectGoMod = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"
)

// The codegen tool pins follow the major a generate run read once, not the
// go.mod as it reads later: a tidy or a user hook can rewrite go.mod
// mid-run, and buf must run the plugin of the major every emitter renders.
func TestCodegenToolsMajorFollowsTheRunSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeGoModIn(t, dir, v2ProjectGoMod)
	ctx, err := newPipelineContextWithFlags(dir, pipelineFlags{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bindCodegenToolsMajor(dir, 0) })
	// go.mod flips under the run.
	writeGoModIn(t, dir, v1ProjectGoMod)
	if got := ctx.connectMajor(); got != connectmajor.V2 {
		t.Fatalf("run major = %v after go.mod changed, want the v2 snapshot", got)
	}
	if got := codegenToolsMajor(dir); got != connectmajor.V2 {
		t.Fatalf("tool pins follow %v, want the run's v2 snapshot", got)
	}
	if got := connectTool(t, requiredProtoToolsFor(codegenToolsMajor(dir))); got.VersionModule != connectmajor.ModuleV2 {
		t.Fatalf("pinned plugin = %+v, want connect-go v2's", got)
	}
	if hint := goToolPackageHint(dir, "protoc-gen-connect-go"); hint != connectmajor.V2.PluginModule() {
		t.Fatalf("go get -tool hint = %q, want %q", hint, connectmajor.V2.PluginModule())
	}
	// Unbound, the go.mod decides again — standalone callers keep detection.
	bindCodegenToolsMajor(dir, 0)
	if got := codegenToolsMajor(dir); got != connectmajor.V1 {
		t.Fatalf("unbound tool major = %v, want the go.mod's v1", got)
	}
}

// A resolution cached for one major is not served to a run bound to the
// other; one cached with no major (an older entry) is.
func TestEnsureCodegenTools_RePinsForAnotherMajor(t *testing.T) {
	dir := t.TempDir()
	writeGoModIn(t, dir, v2ProjectGoMod)
	stubPinTools(t, func(string) (string, error) { return "", errors.New("no module graph") })
	key := absOr(dir) + "\x00" + os.Getenv("PATH")
	stale := &codegenTools{paths: map[string]string{}, major: connectmajor.V1}
	pinMu.Lock()
	pinCache[key] = stale
	pinMu.Unlock()
	t.Cleanup(func() {
		pinMu.Lock()
		delete(pinCache, key)
		pinMu.Unlock()
	})

	got, err := ensureCodegenTools(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if got == stale || got.major != connectmajor.V2 {
		t.Fatalf("served the v1 resolution to a v2 project (major %v)", got.major)
	}
	anyMajor := &codegenTools{paths: map[string]string{}}
	pinMu.Lock()
	pinCache[key] = anyMajor
	pinMu.Unlock()
	if got, _ := ensureCodegenTools(context.Background(), dir); got != anyMajor {
		t.Fatal("an entry with no recorded major was re-pinned")
	}
}

// stubPinTools replaces the go commands pinCodegenTools runs: goList answers
// `go list -m` per module, and a fake binary's build info is the
// "<package>@<version>" it holds (fakeTool), so `go version -m` of a binary
// installed by the recorded fake `go install` reports what was installed.
// PATH is prepended with a directory holding a fake buf (so pinBuf keeps
// it); the caller drops plugins there.
func stubPinTools(t *testing.T, goList func(module string) (string, error)) (pathDir string, installs *[]string) {
	t.Helper()
	prevList, prevVer, prevInstall := goListModuleVersion, goVersionM, goInstallPinned
	t.Cleanup(func() { goListModuleVersion, goVersionM, goInstallPinned = prevList, prevVer, prevInstall })
	goListModuleVersion = func(_ context.Context, _, module string) (string, error) { return goList(module) }
	goVersionM = func(_ context.Context, binPath string) (string, error) {
		// Only a fakeTool file answers; a real binary elsewhere on PATH
		// reads as one with no build info.
		b, err := os.ReadFile(binPath)
		pkg, version, ok := strings.Cut(strings.TrimSpace(string(b)), "@")
		if err != nil || !ok || len(b) > 256 || strings.ContainsAny(string(b), "\x00 ") {
			return "", errors.New("not a Go binary")
		}
		module := strings.TrimSuffix(pkg, "/cmd/"+filepath.Base(pkg))
		return "\tpath\t" + pkg + "\n\tmod\t" + module + "\t" + version + "\th1:x=\n", nil
	}
	var specs []string
	goInstallPinned = func(_ context.Context, spec, gobin string) error {
		specs = append(specs, spec)
		fakeTool(t, gobin, spec)
		return nil
	}
	t.Setenv(toolcache.EnvRoot, t.TempDir())
	pathDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(pathDir, "buf"+exeSuffix()), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return pathDir, &specs
}

// fakeTool writes an executable into dir named for spec's package, holding
// spec ("<package>@<version>") as its stubbed build info.
func fakeTool(t *testing.T, dir, spec string) {
	t.Helper()
	pkg, _, _ := strings.Cut(spec, "@")
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(pkg)+exeSuffix()), []byte(spec+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A go.mod that needs a tidy makes `go list -m` fail ("updates to go.mod
// needed"). protoc-gen-connect-go is the one tool two incompatible builds
// share a name for, so that failure must not mean "any binary on PATH": a v2
// project fell through to the v1 plugin there, buf emitted v1 stubs, and the
// build failed on undefined RegisterXHandler / XServerStream.
func TestPinCodegenTools_UntidyGoModKeepsTheConnectMajor(t *testing.T) {
	untidy := func(string) (string, error) { return "", errors.New("go: updates to go.mod needed") }
	const untidyV2 = "module example.com/app\n\ngo 1.26\n\nrequire (\n\tconnectrpc.com/connect/v2 v2.0.2\n\tgithub.com/reliant-labs/forge v0.0.0\n)\n\nreplace github.com/reliant-labs/forge => ../forge\n"
	for _, tc := range []struct {
		name        string
		gomod       string
		major       connectmajor.Major
		onPath      string // the protoc-gen-connect-go build on PATH, "" for none
		wantInstall string // the one install expected, "" for none
	}{
		{name: "v2 project, v1 plugin on PATH: pins go.mod's v2 require", gomod: untidyV2, major: connectmajor.V2,
			onPath: connectmajor.V1.PluginModule() + "@v1.19.0", wantInstall: connectmajor.V2.PluginModule() + "@v2.0.2"},
		{name: "v2 project, no plugin on PATH: pins go.mod's v2 require", gomod: untidyV2, major: connectmajor.V2,
			wantInstall: connectmajor.V2.PluginModule() + "@v2.0.2"},
		{name: "v2 project, another v2 version on PATH: kept", gomod: untidyV2, major: connectmajor.V2,
			onPath: connectmajor.V2.PluginModule() + "@v2.0.1"},
		{name: "v1 project, v1 plugin on PATH: kept, as before", gomod: v1ProjectGoMod, major: connectmajor.V1,
			onPath: connectmajor.V1.PluginModule() + "@v1.18.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeGoModIn(t, dir, tc.gomod)
			pathDir, installs := stubPinTools(t, untidy)
			if tc.onPath != "" {
				fakeTool(t, pathDir, tc.onPath)
			}
			res, err := pinCodegenTools(context.Background(), dir, tc.major)
			if err != nil {
				t.Fatal(err)
			}
			got, pinned := res.Path("protoc-gen-connect-go")
			if tc.wantInstall == "" {
				if len(*installs) != 0 || pinned {
					t.Fatalf("installs = %v (pinned %q), want the PATH plugin kept", *installs, got)
				}
				return
			}
			if len(*installs) != 1 || (*installs)[0] != tc.wantInstall {
				t.Fatalf("installs = %v, want exactly %s", *installs, tc.wantInstall)
			}
			if _, v, _ := strings.Cut(tc.wantInstall, "@"); !pinned || !strings.Contains(got, "protoc-gen-connect-go@"+v) {
				t.Fatalf("protoc-gen-connect-go resolves to %q (pinned %v), want the cache's %s build", got, pinned, v)
			}
		})
	}
}

func TestGoModRequireVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeGoModIn(t, dir, "module example.com/app\n\ngo 1.26\n\nrequire (\n\tconnectrpc.com/connect/v2 v2.0.2\n\tgoogle.golang.org/protobuf v1.36.9 // indirect\n)\n")
	for module, want := range map[string]string{
		connectmajor.ModuleV2:        "v2.0.2",
		"google.golang.org/protobuf": "v1.36.9",
		connectmajor.ModuleV1:        "",
	} {
		if got := goModRequireVersion(dir, module); got != want {
			t.Errorf("goModRequireVersion(%s) = %q, want %q", module, got, want)
		}
	}
	if got := goModRequireVersion(t.TempDir(), connectmajor.ModuleV2); got != "" {
		t.Errorf("no go.mod: %q, want \"\"", got)
	}
}

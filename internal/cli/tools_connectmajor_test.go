package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/connectmajor"
)

// connectTool returns the protoc-gen-connect-go entry of a tool set.
func connectTool(t *testing.T, tools []protoTool) protoTool {
	t.Helper()
	for _, tool := range tools {
		if tool.Binary == "protoc-gen-connect-go" {
			return tool
		}
	}
	t.Fatal("tool set has no protoc-gen-connect-go")
	return protoTool{}
}

// A connect-go v2 project installs protoc-gen-connect-go from connect's v2
// module, at the version go.mod resolves for that module; every other
// project keeps v1's — the set forge has always installed.
func TestProjectProtoTools_FollowsTheConnectMajor(t *testing.T) {
	const head = "module example.com/app\n\ngo 1.26\n\n"
	for _, tt := range []struct {
		name, gomod           string
		wantModule, wantVerOf string
	}{
		{"no go.mod", "", "connectrpc.com/connect/cmd/protoc-gen-connect-go", "connectrpc.com/connect"},
		{"v1", head + "require connectrpc.com/connect v1.21.1\n",
			"connectrpc.com/connect/cmd/protoc-gen-connect-go", "connectrpc.com/connect"},
		{"v2 only indirect", head + "require (\n\tconnectrpc.com/connect v1.21.1\n\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n",
			"connectrpc.com/connect/cmd/protoc-gen-connect-go", "connectrpc.com/connect"},
		{"v2", head + "require connectrpc.com/connect/v2 v2.0.2\n",
			"connectrpc.com/connect/v2/cmd/protoc-gen-connect-go", "connectrpc.com/connect/v2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.gomod != "" {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tt.gomod), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			tools := projectProtoTools(dir)
			got := connectTool(t, tools)
			if got.Module != tt.wantModule || got.VersionModule != tt.wantVerOf {
				t.Fatalf("protoc-gen-connect-go = %s (version of %s), want %s (version of %s)",
					got.Module, got.VersionModule, tt.wantModule, tt.wantVerOf)
			}
			// Only the connect plugin moves with the major.
			if len(tools) != len(requiredProtoTools) {
				t.Fatalf("tool set has %d tools, want %d", len(tools), len(requiredProtoTools))
			}
			for i, tool := range tools {
				if tool.Binary != "protoc-gen-connect-go" && tool != requiredProtoTools[i] {
					t.Errorf("%s changed with the connect major: %+v", tool.Binary, tool)
				}
			}
		})
	}
}

// The v1 default set is byte-for-byte the set forge installed before v2.
func TestRequiredProtoTools_V1DefaultUnchanged(t *testing.T) {
	want := protoTool{
		Binary:        "protoc-gen-connect-go",
		Module:        "connectrpc.com/connect/cmd/protoc-gen-connect-go",
		VersionModule: "connectrpc.com/connect",
	}
	if got := connectTool(t, requiredProtoTools); got != want {
		t.Fatalf("v1 default = %+v, want %+v", got, want)
	}
	if got := connectTool(t, requiredProtoToolsFor(0)); got != want {
		t.Fatalf("unset major = %+v, want the v1 default %+v", got, want)
	}
}

// Both majors ship a binary named protoc-gen-connect-go. The pinned-tool
// cache must never serve one major's build to a project on the other: their
// entries differ, and neither build satisfies the other's pin check.
func TestPinnedConnectPluginNeverCrossesMajors(t *testing.T) {
	t.Setenv("FORGE_TOOLS_CACHE", t.TempDir())
	v1 := connectTool(t, requiredProtoToolsFor(connectmajor.V1))
	v2 := connectTool(t, requiredProtoToolsFor(connectmajor.V2))

	d1, err := toolCacheDir(v1, "v1.21.1")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := toolCacheDir(v2, "v2.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if d1 == d2 {
		t.Fatalf("v1 and v2 plugins share a cache entry: %s", d1)
	}

	// `go version -m` of each plugin build: the mod line names its module.
	v1Info := "/x/protoc-gen-connect-go: go1.26.0\n" +
		"\tpath\tconnectrpc.com/connect/cmd/protoc-gen-connect-go\n" +
		"\tmod\tconnectrpc.com/connect\tv1.21.1\th1:x=\n" +
		"\tdep\tgoogle.golang.org/protobuf\tv1.36.11\th1:y=\n"
	v2Info := "/x/protoc-gen-connect-go: go1.26.0\n" +
		"\tpath\tconnectrpc.com/connect/v2/cmd/protoc-gen-connect-go\n" +
		"\tmod\tconnectrpc.com/connect/v2\tv2.0.2\th1:x=\n" +
		"\tdep\tgoogle.golang.org/protobuf\tv1.36.11\th1:y=\n"
	if _, ok := parseBuildInfoVersion(v1Info, v2.VersionModule); ok {
		t.Error("a v1 plugin build satisfies the v2 pin's module check")
	}
	if _, ok := parseBuildInfoVersion(v2Info, v1.VersionModule); ok {
		t.Error("a v2 plugin build satisfies the v1 pin's module check")
	}
	if got, ok := parseBuildInfoVersion(v2Info, v2.VersionModule); !ok || got != "v2.0.2" {
		t.Errorf("v2 build reads %q, %v; want v2.0.2", got, ok)
	}

	// `forge tools install` must not call the other major's binary installed.
	if m, ok := otherConnectMajorIn(v1Info, v2); !ok || m != connectmajor.V1 {
		t.Errorf("v1 binary for a v2 project: got %s, %v; want v1, true", m, ok)
	}
	if m, ok := otherConnectMajorIn(v2Info, v1); !ok || m != connectmajor.V2 {
		t.Errorf("v2 binary for a v1 project: got %s, %v; want v2, true", m, ok)
	}
	if _, ok := otherConnectMajorIn(v2Info, v2); ok {
		t.Error("a v2 binary for a v2 project was reported as the other major")
	}
	if _, ok := otherConnectMajorIn("not a go binary", v2); ok {
		t.Error("unreadable build info must not force a reinstall")
	}
}

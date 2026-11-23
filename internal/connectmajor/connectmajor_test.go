package connectmajor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect(t *testing.T) {
	const head = "module example.com/app\n\ngo 1.26\n\n"
	tests := []struct {
		name  string
		gomod string // "" → no go.mod at all
		want  Major
	}{
		{name: "no go.mod is v1", want: V1},
		{name: "neither major is v1", gomod: head + "require github.com/spf13/cobra v1.9.1\n", want: V1},
		{name: "direct v1 is v1", gomod: head + "require connectrpc.com/connect v1.21.1\n", want: V1},
		{
			// otelconnect v0.12 / vanguard pull v2 into every v1 project.
			name: "indirect v2 only is v1",
			gomod: head + "require (\n\tconnectrpc.com/connect v1.21.1\n" +
				"\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n",
			want: V1,
		},
		{name: "direct v2 is v2", gomod: head + "require connectrpc.com/connect/v2 v2.0.2\n", want: V2},
		{
			name: "both direct (mid-migration) is v2",
			gomod: head + "require (\n\tconnectrpc.com/connect v1.21.1\n" +
				"\tconnectrpc.com/connect/v2 v2.0.2\n)\n",
			want: V2,
		},
		{
			name: "direct v2 with indirect v1 is v2",
			gomod: head + "require (\n\tconnectrpc.com/connect v1.21.1 // indirect\n" +
				"\tconnectrpc.com/connect/v2 v2.0.2\n)\n",
			want: V2,
		},
		{
			// A directive newer than this forge's x/mod must not hide the
			// require block.
			name:  "unknown directive is tolerated",
			gomod: head + "futuredirective foo\n\nrequire connectrpc.com/connect/v2 v2.0.2\n",
			want:  V2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.gomod != "" {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tt.gomod), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Detect = %s, want %s", got, tt.want)
			}
			if d := DetectOrDefault(dir); d != tt.want {
				t.Fatalf("DetectOrDefault = %s, want %s", d, tt.want)
			}
		})
	}
}

func TestDetect_UnparseableGoModIsDefaultWithError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("require (\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Detect(dir)
	if err == nil {
		t.Fatal("want a parse error")
	}
	if got != Default {
		t.Fatalf("Detect = %s, want the default %s alongside the error", got, Default)
	}
	if DetectOrDefault(dir) != Default {
		t.Fatal("DetectOrDefault must fall back to the default")
	}
}

func TestMajorPaths(t *testing.T) {
	for _, tt := range []struct {
		m                       Major
		module, plugin, version string
	}{
		{V1, "connectrpc.com/connect", "connectrpc.com/connect/cmd/protoc-gen-connect-go", "connectrpc.com/connect"},
		{V2, "connectrpc.com/connect/v2", "connectrpc.com/connect/v2/cmd/protoc-gen-connect-go", "connectrpc.com/connect/v2"},
		// The zero value names v1's paths: an unset Major is the default.
		{0, "connectrpc.com/connect", "connectrpc.com/connect/cmd/protoc-gen-connect-go", "connectrpc.com/connect"},
	} {
		if got := tt.m.Module(); got != tt.module {
			t.Errorf("%s.Module() = %q, want %q", tt.m, got, tt.module)
		}
		if got := tt.m.RuntimeImport(); got != tt.module {
			t.Errorf("%s.RuntimeImport() = %q, want %q", tt.m, got, tt.module)
		}
		if got := tt.m.PluginModule(); got != tt.plugin {
			t.Errorf("%s.PluginModule() = %q, want %q", tt.m, got, tt.plugin)
		}
		if got := tt.m.VersionModule(); got != tt.version {
			t.Errorf("%s.VersionModule() = %q, want %q", tt.m, got, tt.version)
		}
	}
	if Major(0).OrDefault() != V1 || V2.OrDefault() != V2 {
		t.Error("OrDefault must fill only the zero value")
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Major{"v1": V1, "1": V1, "V2": V2, " 2 ": V2} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	if _, err := Parse("v3"); err == nil {
		t.Error("Parse(v3) must fail")
	}
}

func TestIsRuntimeImport(t *testing.T) {
	for path, want := range map[string]Major{
		"connectrpc.com/connect":         V1,
		"github.com/bufbuild/connect-go": V1,
		"connectrpc.com/connect/v2":      V2,
	} {
		if got, ok := IsRuntimeImport(path); !ok || got != want {
			t.Errorf("IsRuntimeImport(%q) = %s, %v; want %s", path, got, ok, want)
		}
	}
	for _, path := range []string{
		"connectrpc.com/connect/v2/connecthttp",
		"connectrpc.com/otelconnect",
		"example.com/app/internal/connect",
		"example.com/app/gen/ping/v1/pingv1connect",
	} {
		if _, ok := IsRuntimeImport(path); ok {
			t.Errorf("IsRuntimeImport(%q) must be false", path)
		}
	}
}

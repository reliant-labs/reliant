package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/connectmajor"
)

const (
	connectV1Plugin = "connectrpc.com/connect/cmd/protoc-gen-connect-go"
	connectV2Plugin = "connectrpc.com/connect/v2/cmd/protoc-gen-connect-go"
)

const v2GoMod = "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n"

func writeGoMod(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The scaffold names the protoc-gen-connect-go of the connect-go major the
// project targets: v1 for a fresh project, the go.mod's choice for a module
// adopted in place.
func TestProjectGenerator_ConnectMajor(t *testing.T) {
	t.Run("fresh project is v1", func(t *testing.T) {
		g := NewProjectGenerator("app", t.TempDir(), "example.com/app")
		if got := g.connectMajor(); got != connectmajor.V1 {
			t.Fatalf("connectMajor = %s, want v1", got)
		}
	})
	t.Run("adopted v2 module is v2", func(t *testing.T) {
		dir := t.TempDir()
		writeGoMod(t, dir, v2GoMod)
		g := NewProjectGenerator("app", dir, "example.com/app")
		if got := g.connectMajor(); got != connectmajor.V2 {
			t.Fatalf("connectMajor = %s, want v2", got)
		}
		if !g.forScaffold().ConnectV2 {
			t.Fatal("forScaffold().ConnectV2 = false for a v2 module")
		}
	})
}

func TestBootstrapScript_InstallsTheProjectsConnectPlugin(t *testing.T) {
	for _, tt := range []struct {
		major     connectmajor.Major
		want, not string
	}{
		{connectmajor.V1, connectV1Plugin + "@latest", connectV2Plugin},
		{connectmajor.V2, connectV2Plugin + "@latest", connectV1Plugin},
	} {
		t.Run(tt.major.String(), func(t *testing.T) {
			dir := t.TempDir()
			if tt.major == connectmajor.V2 {
				writeGoMod(t, dir, v2GoMod)
			}
			g := NewProjectGenerator("app", dir, "example.com/app")
			if err := g.generateBootstrapScript(); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "scripts", "bootstrap.sh"))
			if err != nil {
				t.Fatal(err)
			}
			script := string(raw)
			line := "install_go_tool protoc-gen-connect-go    " + tt.want
			if !strings.Contains(script, line) {
				t.Fatalf("bootstrap.sh lacks %q:\n%s", line, script)
			}
			if strings.Contains(script, tt.not) {
				t.Fatalf("bootstrap.sh names the other major's plugin %q", tt.not)
			}
		})
	}
}

func TestToolsGoTemplate_NamesTheProjectsConnectPlugin(t *testing.T) {
	for _, tt := range []struct {
		v2        bool
		want, not string
	}{
		{false, connectV1Plugin, connectV2Plugin},
		{true, connectV2Plugin, connectV1Plugin},
	} {
		dest := filepath.Join(t.TempDir(), "tools.go")
		if err := assets.WriteTemplateWithData("tools.go.tmpl", dest, projectTemplateData{ConnectV2: tt.v2}); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(dest)
		if err != nil {
			t.Fatal(err)
		}
		got := string(raw)
		if !strings.Contains(got, `_ "`+tt.want+`"`) || !strings.Contains(got, "go install "+tt.want+"\n") {
			t.Errorf("ConnectV2=%v: tools.go does not name %s:\n%s", tt.v2, tt.want, got)
		}
		if strings.Contains(got, tt.not) {
			t.Errorf("ConnectV2=%v: tools.go names the other major's plugin %s", tt.v2, tt.not)
		}
		// The import block stays gofmt-shaped in both majors.
		if !strings.Contains(got, "import (\n\t_ \"connectrpc.com/connect") || !strings.Contains(got, "\n\t_ \"google.golang.org/protobuf/cmd/protoc-gen-go\"\n)\n") {
			t.Errorf("ConnectV2=%v: import block is malformed:\n%s", tt.v2, got)
		}
	}
}

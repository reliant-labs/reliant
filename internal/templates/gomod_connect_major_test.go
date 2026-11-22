package templates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// forgeRequires is the version forge's own go.mod requires of module path.
func forgeRequires(t *testing.T, path string) string {
	t.Helper()
	gomod := filepath.Join("..", "..", "go.mod")
	raw, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	f, err := modfile.ParseLax(gomod, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.Require {
		if r.Mod.Path == path {
			return r.Mod.Version
		}
	}
	t.Fatalf("forge's go.mod requires no %s", path)
	return ""
}

// A connect-go v2 project's go.mod requires connectrpc.com/connect/v2 —
// directly, at the version forge itself builds forge/pkg's v2 APIs against —
// and no connectrpc.com/connect (v1). otelconnect and vanguard move to the
// releases forge requires, the first that speak v2. The direct require is
// also what internal/connectmajor reads the major back from.
func TestGoModTemplates_ConnectV2RequiresTheV2Runtime(t *testing.T) {
	type data struct {
		Module, GoVersion, ForgeVersion string
		RESTEnabled, GenModule          bool
		ConnectV2                       bool
	}
	d := data{Module: "github.com/example/demo", GoVersion: "1.26", ForgeVersion: "v1.0.0", RESTEnabled: true, GenModule: true, ConnectV2: true}
	for _, name := range []string{"go.mod.tmpl", "gen-go.mod.tmpl"} {
		out, err := ProjectTemplates().Render(name, d)
		if err != nil {
			t.Fatal(err)
		}
		f, err := modfile.Parse(name, out, nil)
		if err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, out)
		}
		got := map[string]string{}
		for _, r := range f.Require {
			if r.Indirect {
				t.Errorf("%s: %s is an indirect require; the major is read from a DIRECT one", name, r.Mod.Path)
			}
			got[r.Mod.Path] = r.Mod.Version
		}
		if v, want := got["connectrpc.com/connect/v2"], forgeRequires(t, "connectrpc.com/connect/v2"); v != want {
			t.Errorf("%s requires connectrpc.com/connect/v2 %q, want %q (forge's own)", name, v, want)
		}
		if v, ok := got["connectrpc.com/connect"]; ok {
			t.Errorf("%s still requires connectrpc.com/connect %s", name, v)
		}
		if name != "go.mod.tmpl" {
			continue
		}
		for _, mod := range []string{"connectrpc.com/otelconnect", "connectrpc.com/vanguard"} {
			if v, want := got[mod], forgeRequires(t, mod); v != want {
				t.Errorf("go.mod.tmpl requires %s %q, want %q (forge's own)", mod, v, want)
			}
		}
	}
	// And the v1 render is untouched by any of it.
	d.ConnectV2 = false
	out, err := ProjectTemplates().Render("go.mod.tmpl", d)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\tconnectrpc.com/connect v1.19.0\n") || strings.Contains(string(out), "connect/v2") {
		t.Errorf("v1 go.mod changed:\n%s", out)
	}
}

// The connectV2 template func reads a ConnectV2 field or key wherever the
// data carries one, and treats data that has none — every render that
// predates connect-go v2 support — as v1 instead of failing.
func TestConnectV2TemplateFunc(t *testing.T) {
	type with struct{ ConnectV2 bool }
	type without struct{ Name string }
	for _, tt := range []struct {
		name string
		data any
		want bool
	}{
		{"struct true", with{ConnectV2: true}, true},
		{"struct false", with{}, false},
		{"pointer", &with{ConnectV2: true}, true},
		{"nil pointer", (*with)(nil), false},
		{"no field", without{Name: "x"}, false},
		{"map true", map[string]any{"ConnectV2": true}, true},
		{"map missing", map[string]any{"Name": "x"}, false},
		{"map wrong type", map[string]any{"ConnectV2": "yes"}, false},
		{"nil", nil, false},
	} {
		if got := connectV2(tt.data); got != tt.want {
			t.Errorf("%s: connectV2 = %v, want %v", tt.name, got, tt.want)
		}
	}
}

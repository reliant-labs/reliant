package templates

import (
	"os"
	"path/filepath"
	"testing"
)

// TestConnectMajorTemplateGoldens snapshots the project templates whose
// output names connect-go, once per major (testdata/golden/connect/). The
// .v1 files pin the connect-go v1 render byte for byte — they were captured
// before the v2 branches existed — and the .v2 files are what a project born
// with `forge project new --connect v2` gets. Rewrite with -update.
func TestConnectMajorTemplateGoldens(t *testing.T) {
	type e2eMethod struct{ Name, InputType, OutputType string }
	type e2eData struct {
		Module, ServiceName, ServicePackage, ConnectServiceGoName string
		Methods                                                   []e2eMethod
		ConnectV2                                                 bool
	}
	e2e := func(v2 bool) e2eData {
		return e2eData{
			Module: "github.com/example/demo", ServiceName: "orders", ServicePackage: "orders",
			ConnectServiceGoName: "OrdersService",
			Methods:              []e2eMethod{{Name: "GetOrder", InputType: "GetOrderRequest", OutputType: "GetOrderResponse"}},
			ConnectV2:            v2,
		}
	}
	type modData struct {
		Module, GoVersion, ForgeVersion string
		RESTEnabled, GenModule          bool
		ConnectV2                       bool
	}
	mod := func(v2, rich bool) modData {
		d := modData{Module: "github.com/example/demo", GoVersion: "1.27", ConnectV2: v2}
		if rich {
			d.ForgeVersion, d.RESTEnabled, d.GenModule = "v1.2.3", true, true
		}
		return d
	}
	cases := []struct {
		name   string
		render func(t *testing.T, v2 bool) []byte
	}{
		// The v2 project's copy is its own template (the generator's
		// connectV2Twins): both stay plain Go before rendering.
		{"middleware.go", func(t *testing.T, v2 bool) []byte {
			name := "middleware.go.tmpl"
			if v2 {
				name = "middleware_v2.go.tmpl"
			}
			return renderProject(t, name, struct{ Name string }{"demo"})
		}},
		{"go.mod", func(t *testing.T, v2 bool) []byte { return renderProject(t, "go.mod.tmpl", mod(v2, false)) }},
		{"go.mod_rest_gen", func(t *testing.T, v2 bool) []byte { return renderProject(t, "go.mod.tmpl", mod(v2, true)) }},
		{"gen-go.mod", func(t *testing.T, v2 bool) []byte { return renderProject(t, "gen-go.mod.tmpl", mod(v2, true)) }},
		{"tools.go", func(t *testing.T, v2 bool) []byte { return renderProject(t, "tools.go.tmpl", mod(v2, false)) }},
		{"e2e_helpers_test.go", func(t *testing.T, v2 bool) []byte {
			out, err := TestTemplates().Render("e2e/helpers_test.go.tmpl", e2e(v2))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}},
		{"e2e_service_test.go", func(t *testing.T, v2 bool) []byte {
			out, err := TestTemplates().Render("e2e/service_test.go.tmpl", e2e(v2))
			if err != nil {
				t.Fatal(err)
			}
			return out
		}},
	}
	for _, c := range cases {
		for _, major := range []string{"v1", "v2"} {
			t.Run(c.name+"."+major, func(t *testing.T) {
				got := c.render(t, major == "v2")
				path := filepath.Join("testdata", "golden", "connect", c.name+"."+major+".golden")
				if *updateGolden {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, got, 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read %s (run with -update to create it): %v", path, err)
				}
				if string(got) != string(want) {
					t.Errorf("%s differs from %s; rerun with -update if intended.\n--- got ---\n%s", c.name+"."+major, path, got)
				}
			})
		}
	}
}

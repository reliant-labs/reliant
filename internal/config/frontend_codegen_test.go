package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// writeCodegenFile writes rel under root, creating parents.
func writeCodegenFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// viteFrontend lays down a Vite app at frontends/<name> with the given
// package.json body.
func viteFrontend(t *testing.T, root, name, pkg string) {
	t.Helper()
	writeCodegenFile(t, root, "frontends/"+name+"/vite.config.ts", "export default {}\n")
	writeCodegenFile(t, root, "frontends/"+name+"/package.json", pkg)
}

func discovered(t *testing.T, root, name string) FrontendConfig {
	t.Helper()
	for _, fe := range DiscoverInRepoFrontends(root) {
		if fe.Name == name {
			return fe
		}
	}
	t.Fatalf("frontend %s not discovered", name)
	return FrontendConfig{}
}

// TestFrontendAdoptedDetection pins the two records that say forge scaffolded
// a frontend, and that their absence means adopted. The prefix trap is the
// load-bearing case: generate records births INSIDE an adopted frontend (its
// hook tests, its seams), and "anything under the directory" would flip it to
// scaffolded on its second run, installing the starter set after all.
func TestFrontendAdoptedDetection(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, root string)
		adopted bool
	}{
		{
			name:    "no record at all: an app forge did not scaffold",
			setup:   func(*testing.T, string) {},
			adopted: true,
		},
		{
			name: "the ledger records its package.json",
			setup: func(t *testing.T, root string) {
				writeCodegenFile(t, root, ".forge/scaffolded/frontends/web/package.json.json", "{}\n")
			},
			adopted: false,
		},
		{
			name: "pre-ledger scaffold: forge's nested go.mod boundary marker",
			setup: func(t *testing.T, root string) {
				writeCodegenFile(t, root, "frontends/web/go.mod",
					"// Nested module boundary so `go test ./...` from the project root\n// skips node_modules.\nmodule x/frontends/web\n")
			},
			adopted: false,
		},
		{
			name: "births generate made inside an adopted frontend do not make it scaffolded",
			setup: func(t *testing.T, root string) {
				writeCodegenFile(t, root, ".forge/scaffolded/frontends/web/src/lib/connect.ts.json", "{}\n")
				writeCodegenFile(t, root, ".forge/scaffolded.json",
					`{"files": {"frontends/web/src/hooks/chat-service-hooks.test.tsx": {}}}`)
			},
			adopted: true,
		},
		{
			name: "a go.mod of the app's own is not forge's marker",
			setup: func(t *testing.T, root string) {
				writeCodegenFile(t, root, "frontends/web/go.mod", "module example.com/wasm\n")
			},
			adopted: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			viteFrontend(t, root, "web", `{"name": "web"}`)
			tc.setup(t, root)
			fe := discovered(t, root, "web")
			if fe.Adopted != tc.adopted {
				t.Fatalf("Adopted = %v, want %v", fe.Adopted, tc.adopted)
			}
			// An adopted frontend that declares no routes gets none: CRUD
			// pages are starter code. A scaffolded one keeps "every entity".
			wantRoutes := []string(nil)
			if tc.adopted {
				wantRoutes = []string{RoutesNone}
			}
			if !reflect.DeepEqual(fe.Routes, wantRoutes) {
				t.Fatalf("Routes = %v, want %v", fe.Routes, wantRoutes)
			}
		})
	}
}

func TestFrontendPackageForgeBlock(t *testing.T) {
	root := t.TempDir()
	viteFrontend(t, root, "web", `{
  "name": "web",
  "forge": {
    "routes": ["customers"],
    "hooks": ["ChatService", "reliant.v1.ProjectService"],
    "components_dir": "./src/components/forge-ui/"
  }
}`)
	fe := discovered(t, root, "web")
	if len(fe.CodegenProblems) != 0 {
		t.Fatalf("CodegenProblems = %v", fe.CodegenProblems)
	}
	if !reflect.DeepEqual(fe.Routes, []string{"customers"}) {
		t.Errorf("Routes = %v: a declared allowlist wins over the adopted default", fe.Routes)
	}
	if got := fe.ComponentsDirRel(); got != "src/components/forge-ui" {
		t.Errorf("ComponentsDirRel = %q, want the cleaned declaration", got)
	}
	if !fe.WantsHooksFor("ChatService", "reliant.v1.ChatService") {
		t.Error("ChatService is allowlisted by its short name")
	}
	if !fe.WantsHooksFor("ProjectService", "reliant.v1.ProjectService") {
		t.Error("ProjectService is allowlisted by its qualified name")
	}
	if fe.WantsHooksFor("ToolsDaemonService", "reliant.v1.ToolsDaemonService") {
		t.Error("a service the allowlist omits gets no hooks")
	}
	if got := fe.UnknownHooks(map[string]bool{"ChatService": true}); !reflect.DeepEqual(got, []string{"reliant.v1.ProjectService"}) {
		t.Errorf("UnknownHooks = %v", got)
	}
}

func TestFrontendPackageForgeBlockDefaults(t *testing.T) {
	root := t.TempDir()
	viteFrontend(t, root, "web", `{"name": "web"}`)
	fe := discovered(t, root, "web")
	if fe.Hooks != nil || !fe.WantsHooksFor("AnyService", "") {
		t.Errorf("no hooks key means every service; Hooks = %#v", fe.Hooks)
	}
	if fe.ComponentsDirRel() != DefaultComponentsDir {
		t.Errorf("ComponentsDirRel = %q", fe.ComponentsDirRel())
	}

	viteFrontend(t, root, "none", `{"forge": {"hooks": []}}`)
	if fe := discovered(t, root, "none"); fe.Hooks == nil || fe.WantsHooksFor("AnyService", "") {
		t.Errorf("an empty allowlist means no hooks; Hooks = %#v", fe.Hooks)
	}
}

// TestFrontendPackageForgeBlockProblems: a knob forge cannot honour is a
// problem generate refuses on, never a silent default.
func TestFrontendPackageForgeBlockProblems(t *testing.T) {
	for _, tc := range []struct{ name, block, want string }{
		{"misspelt key", `{"route": ["none"]}`, `unknown field "route"`},
		{"known keys are named", `{"route": ["none"]}`, "known keys: routes, hooks, components_dir, imported_hooks"},
		{"escaping components_dir", `{"components_dir": "../shared/ui"}`, "inside the frontend directory"},
		{"absolute components_dir", `{"components_dir": "/abs/ui"}`, "inside the frontend directory"},
		{"components_dir naming the frontend itself", `{"components_dir": "."}`, "frontend directory itself"},
		{"wrong type", `{"hooks": "ChatService"}`, "cannot unmarshal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			viteFrontend(t, root, "web", `{"forge": `+tc.block+`}`)
			fe := discovered(t, root, "web")
			if len(fe.CodegenProblems) != 1 || !strings.Contains(fe.CodegenProblems[0], tc.want) {
				t.Fatalf("CodegenProblems = %v, want one containing %q", fe.CodegenProblems, tc.want)
			}
		})
	}
}

// TestKCLDeclaredFrontendGetsDiskFacts: a KCL `path` at an existing app is how
// `forge project new --in-place` tells a user to adopt one, so it is read the
// same way, and the KCL routes stay authoritative over package.json's.
func TestKCLDeclaredFrontendGetsDiskFacts(t *testing.T) {
	root := t.TempDir()
	writeCodegenFile(t, root, "web/vite.config.ts", "export default {}\n")
	writeCodegenFile(t, root, "web/package.json", `{"forge": {"routes": ["jobs"], "hooks": ["ChatService"]}}`)

	got := DeriveFrontendsFromKCL(root, []KCLFrontend{{Name: "web", Type: "vite", Path: "web", Routes: []string{"customers"}}})
	if len(got) != 1 {
		t.Fatalf("derived %d frontends", len(got))
	}
	fe := got[0]
	if !fe.Adopted {
		t.Error("an app at a KCL path forge never scaffolded is adopted")
	}
	if !reflect.DeepEqual(fe.Routes, []string{"customers"}) {
		t.Errorf("Routes = %v: the KCL declaration is authoritative for the fields it states", fe.Routes)
	}
	if !reflect.DeepEqual(fe.Hooks, []string{"ChatService"}) {
		t.Errorf("Hooks = %v: KCL has no hooks field, so package.json supplies it", fe.Hooks)
	}
}

func TestFrontendComponentsDir(t *testing.T) {
	root := t.TempDir()
	writeCodegenFile(t, root, "package.json", `{"forge": {"components_dir": "src/forge-ui"}}`)
	if got := FrontendComponentsDir(root); got != "src/forge-ui" {
		t.Errorf("FrontendComponentsDir = %q", got)
	}
	if got := FrontendComponentsDir(t.TempDir()); got != DefaultComponentsDir {
		t.Errorf("no manifest: FrontendComponentsDir = %q", got)
	}
}

// TestFrontendPackageForgeImportedHooks: an imported_hooks entry resolves its
// proto_root against the FRONTEND directory into a project-relative buf
// module, and keeps a services allowlist that may span several packages of
// the module.
func TestFrontendPackageForgeImportedHooks(t *testing.T) {
	root := t.TempDir()
	writeCodegenFile(t, root, "proto-vendor/other/buf.yaml", "version: v2\n")
	viteFrontend(t, root, "web", `{"forge": {"imported_hooks": [{
  "proto_root": "../../proto-vendor/other/",
  "gen_dir": "./other/",
  "services": ["other.v1.FooService", "billing.v1.BarService", "other.v1.FooService"]
}]}}`)
	fe := discovered(t, root, "web")
	if len(fe.CodegenProblems) != 0 {
		t.Fatalf("CodegenProblems = %v", fe.CodegenProblems)
	}
	want := []ImportedHooks{{
		ProtoRoot: "proto-vendor/other",
		GenDir:    "other",
		Services:  []string{"other.v1.FooService", "billing.v1.BarService"},
	}}
	if !reflect.DeepEqual(fe.ImportedHooks, want) {
		t.Fatalf("ImportedHooks = %#v, want %#v", fe.ImportedHooks, want)
	}
	imp := fe.ImportedHooks[0]
	if !imp.WantsService("billing.v1.BarService") || imp.WantsService("BarService") || imp.WantsService("other.v1.BazService") {
		t.Error("WantsService matches exactly the fully-qualified allowlist")
	}
	if got := imp.UnknownServices(map[string]bool{"other.v1.FooService": true}); !reflect.DeepEqual(got, []string{"billing.v1.BarService"}) {
		t.Errorf("UnknownServices = %v", got)
	}
	if fe.Hooks != nil {
		t.Errorf("imported_hooks leaves the own-service allowlist alone; Hooks = %#v", fe.Hooks)
	}
}

// TestFrontendPackageForgeImportedHooksProblems: every imported_hooks entry
// forge cannot honour is a problem generate refuses on.
func TestFrontendPackageForgeImportedHooksProblems(t *testing.T) {
	entry := func(protoRoot, genDir, services string) string {
		return `{"imported_hooks": [{"proto_root": "` + protoRoot + `", "gen_dir": "` + genDir + `", "services": ` + services + `}]}`
	}
	ok := `["other.v1.FooService"]`
	for _, tc := range []struct{ name, block, want string }{
		{"proto_root escapes the project", entry("../../../elsewhere", "other", ok), "outside the project root"},
		{"absolute proto_root", entry("/abs/proto", "other", ok), "relative to the frontend directory"},
		{"missing proto_root dir", entry("../../nope", "other", ok), "is not a directory"},
		{"proto_root without buf.yaml", entry("../../proto-vendor/nobuf", "other", ok), "has no buf.yaml"},
		{"no proto_root", entry("", "other", ok), "proto_root: required"},
		{"no gen_dir", entry("../../proto-vendor/other", "", ok), "gen_dir: required"},
		{"escaping gen_dir", entry("../../proto-vendor/other", "../other", ok), "inside src/gen/"},
		{"empty services", entry("../../proto-vendor/other", "other", `[]`), "services: required"},
		{"missing services", `{"imported_hooks": [{"proto_root": "../../proto-vendor/other", "gen_dir": "other"}]}`, "services: required"},
		{"short service name", entry("../../proto-vendor/other", "other", `["FooService"]`), "not a fully-qualified service name"},
		{"unknown entry key", `{"imported_hooks": [{"proto_dir": "x"}]}`, `unknown field "proto_dir"`},
		{"duplicate gen_dir", `{"imported_hooks": [
			{"proto_root": "../../proto-vendor/other", "gen_dir": "other", "services": ["a.v1.A"]},
			{"proto_root": "../../proto-vendor/other", "gen_dir": "other/", "services": ["b.v1.B"]}]}`, "also imported_hooks[0]'s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeCodegenFile(t, root, "proto-vendor/other/buf.yaml", "version: v2\n")
			writeCodegenFile(t, root, "proto-vendor/nobuf/a.proto", "syntax = \"proto3\";\n")
			viteFrontend(t, root, "web", `{"forge": `+tc.block+`}`)
			fe := discovered(t, root, "web")
			if len(fe.CodegenProblems) != 1 || !strings.Contains(fe.CodegenProblems[0], tc.want) {
				t.Fatalf("CodegenProblems = %v, want one containing %q", fe.CodegenProblems, tc.want)
			}
		})
	}
}

// TestFrontendPackageForgeImportedHooksSymlinkEscape: a proto_root whose
// path is inside the project but which is a symlink to a directory outside
// it is a problem (forge#2707 review), while a symlink that stays inside the
// project is honoured.
func TestFrontendPackageForgeImportedHooksSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks need privileges on Windows")
	}
	root, outside := t.TempDir(), t.TempDir()
	writeCodegenFile(t, outside, "buf.yaml", "version: v2\n")
	writeCodegenFile(t, root, "proto-vendor/inside/buf.yaml", "version: v2\n")
	if err := os.Symlink(outside, filepath.Join(root, "proto-vendor", "escaping")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "proto-vendor", "inside"), filepath.Join(root, "proto-vendor", "linked")); err != nil {
		t.Fatal(err)
	}
	block := func(protoRoot string) string {
		return `{"forge": {"imported_hooks": [{"proto_root": "` + protoRoot + `", "gen_dir": "other", "services": ["other.v1.FooService"]}]}}`
	}

	viteFrontend(t, root, "web", block("../../proto-vendor/escaping"))
	fe := discovered(t, root, "web")
	if len(fe.CodegenProblems) != 1 || !strings.Contains(fe.CodegenProblems[0], "outside the project root") || len(fe.ImportedHooks) != 0 {
		t.Fatalf("an escaping symlinked proto_root must be one problem and no entry; problems %v, entries %v", fe.CodegenProblems, fe.ImportedHooks)
	}

	viteFrontend(t, root, "admin", block("../../proto-vendor/linked"))
	fe = discovered(t, root, "admin")
	if len(fe.CodegenProblems) != 0 || len(fe.ImportedHooks) != 1 || fe.ImportedHooks[0].ProtoRoot != "proto-vendor/linked" {
		t.Fatalf("a symlink inside the project is honoured; problems %v, entries %v", fe.CodegenProblems, fe.ImportedHooks)
	}
}

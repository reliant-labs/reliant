package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

func TestRenderContract_APIsDecode(t *testing.T) {
	mixed, _ := loadContract(t, "mixed")
	want := []APIEntity{
		{Name: "public", Kind: "openapi", Spec: "api/openapi.yaml", BasePath: "/v1", Database: "analytics"},
		{Name: "acme", Kind: "proto", Spec: "proto"},
	}
	if !reflect.DeepEqual(mixed.APIs, want) {
		t.Errorf("mixed apis = %+v, want %+v", mixed.APIs, want)
	}
	if hosted, _ := loadContract(t, "hosted"); hosted.APIs != nil {
		t.Errorf("hosted declares no API but decoded %+v", hosted.APIs)
	}
}

func writeSpec(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// An OpenAPI API's spec is checked when the env is loaded: OpenAPI 3.x in
// YAML or JSON passes; Swagger 2 is refused with "convert to OpenAPI 3"; a
// missing file, a document with no version, or another version is refused.
func TestValidateAPIs(t *testing.T) {
	dir := t.TempDir()
	writeSpec(t, dir, "api/openapi.yaml", "openapi: 3.1.0\ninfo: {title: x, version: '1'}\npaths: {}\n")
	writeSpec(t, dir, "api/openapi.json", `{"openapi": "3.0.3", "info": {"title": "x", "version": "1"}, "paths": {}}`)
	writeSpec(t, dir, "api/swagger.yaml", "swagger: \"2.0\"\ninfo: {title: x, version: '1'}\npaths: {}\n")
	writeSpec(t, dir, "api/none.yaml", "info: {title: x}\n")
	writeSpec(t, dir, "api/four.yaml", "openapi: 4.0.0\n")
	writeSpec(t, dir, "proto/svc/v1/svc.proto", "syntax = \"proto3\";\n")

	ok := []APIEntity{
		{Name: "yaml", Kind: APIKindOpenAPI, Spec: "api/openapi.yaml"},
		{Name: "json", Kind: APIKindOpenAPI, Spec: "api/openapi.json"},
		{Name: "proto", Kind: APIKindProto, Spec: "proto"},
	}
	if err := validateAPIs(dir, ok); err != nil {
		t.Fatalf("valid APIs refused: %v", err)
	}
	for _, tc := range []struct {
		api  APIEntity
		want string
	}{
		{APIEntity{Name: "legacy", Kind: APIKindOpenAPI, Spec: "api/swagger.yaml"}, "is Swagger 2.0, not OpenAPI 3 (swagger: \"2.0\"): convert it to OpenAPI 3"},
		{APIEntity{Name: "bare", Kind: APIKindOpenAPI, Spec: "api/none.yaml"}, `has no top-level "openapi" version`},
		{APIEntity{Name: "future", Kind: APIKindOpenAPI, Spec: "api/four.yaml"}, `declares openapi "4.0.0"`},
		{APIEntity{Name: "gone", Kind: APIKindOpenAPI, Spec: "api/missing.yaml"}, "no such file"},
		{APIEntity{Name: "dir", Kind: APIKindOpenAPI, Spec: "api"}, "is a directory"},
		{APIEntity{Name: "noproto", Kind: APIKindProto, Spec: "protos"}, "is not a directory"},
	} {
		err := validateAPIs(dir, []APIEntity{tc.api})
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `API "`+tc.api.Name+`"`) {
			t.Errorf("%s: err = %v, want it to name the API and say %q", tc.api.Name, err, tc.want)
		}
	}
}

// A spec is read only when its target, symlinks resolved, is inside the
// project: KCL refuses an absolute or `..` spec, and a symlink must not get a
// file outside the repository read during env loading (review B1 on #2214). A
// symlink that stays inside, and a project reached through a symlink, pass.
func TestValidateAPIs_SymlinkCannotLeaveTheProject(t *testing.T) {
	outside := t.TempDir()
	writeSpec(t, outside, "secret.yaml", "openapi: 3.1.0\ninfo: {title: x, version: '1'}\npaths: {}\n")
	writeSpec(t, outside, "protos/svc.proto", "syntax = \"proto3\";\n")
	real := t.TempDir()
	writeSpec(t, real, "api/openapi.yaml", "openapi: 3.1.0\ninfo: {title: x, version: '1'}\npaths: {}\n")
	writeSpec(t, real, "proto/svc/v1/svc.proto", "syntax = \"proto3\";\n")
	link := func(target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Skipf("cannot create a symlink here (%v); the containment check needs one", err)
		}
	}
	link(filepath.Join(outside, "secret.yaml"), filepath.Join(real, "api", "escape.yaml"))
	link(filepath.Join(outside, "protos"), filepath.Join(real, "escape-proto"))
	link(filepath.Join(real, "api", "openapi.yaml"), filepath.Join(real, "api", "inside.yaml"))
	viaLink := filepath.Join(t.TempDir(), "project")
	link(real, viaLink)

	for _, dir := range []string{real, viaLink} {
		ok := []APIEntity{
			{Name: "inside", Kind: APIKindOpenAPI, Spec: "api/inside.yaml"},
			{Name: "proto", Kind: APIKindProto, Spec: "proto"},
		}
		if err := validateAPIs(dir, ok); err != nil {
			t.Errorf("%s: symlinks inside the project refused: %v", dir, err)
		}
		for _, a := range []APIEntity{
			{Name: "escape", Kind: APIKindOpenAPI, Spec: "api/escape.yaml"},
			{Name: "escapedir", Kind: APIKindProto, Spec: "escape-proto"},
		} {
			err := validateAPIs(dir, []APIEntity{a})
			if err == nil || !strings.Contains(err.Error(), "resolves outside the project") || !strings.Contains(err.Error(), `API "`+a.Name+`"`) {
				t.Errorf("%s: %s: err = %v, want it refused as outside the project", dir, a.Name, err)
			}
		}
	}
}

// The derived proto API: a project with proto services and no proto API
// declared gets {name: <project>, kind: proto, spec: proto}; one that
// declares a proto API, or has no services, gets none.
func TestWithDerivedProtoAPI(t *testing.T) {
	declared := []projectAPI{{APIEntity: APIEntity{Name: "public", Kind: APIKindOpenAPI, Spec: "openapi.yaml"}, Origin: originKCL}}
	got := withDerivedProtoAPI(declared, "shop", true)
	if len(got) != 2 || got[1].APIEntity != (APIEntity{Name: "shop", Kind: APIKindProto, Spec: "proto"}) || got[1].Origin != originDerived {
		t.Errorf("derived = %+v", got)
	}
	if got := withDerivedProtoAPI(declared, "shop", false); len(got) != 1 {
		t.Errorf("no services must derive nothing: %+v", got)
	}
	withProto := append(declared, projectAPI{APIEntity: APIEntity{Name: "rpc", Kind: APIKindProto, Spec: "proto"}, Origin: originKCL})
	if got := withDerivedProtoAPI(withProto, "shop", true); len(got) != 2 {
		t.Errorf("a declared proto API must not get a derived twin: %+v", got)
	}
}

func TestUnionDeclaredAPIs(t *testing.T) {
	api := APIEntity{Name: "public", Kind: APIKindOpenAPI, Spec: "api/openapi.yaml"}
	spelled := api
	spelled.Spec = "./api/openapi.yaml"
	got, err := unionDeclaredAPIs(map[string]*KCLEntities{"dev": {APIs: []APIEntity{api}}, "prod": {APIs: []APIEntity{spelled}}})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].Envs, []string{"dev", "prod"}) {
		t.Fatalf("union = %+v, %v", got, err)
	}
	other := api
	other.BasePath = "/v2"
	if _, err := unionDeclaredAPIs(map[string]*KCLEntities{"dev": {APIs: []APIEntity{api}}, "prod": {APIs: []APIEntity{other}}}); err == nil ||
		!strings.Contains(err.Error(), `API "public" is declared differently in env prod than in env dev`) {
		t.Errorf("disagreeing envs: %v", err)
	}
}

// `forge project audit --json`'s resources category lists the databases and
// APIs with their origin: a derived db/migrations database and a derived
// proto API in a project that declares neither.
func TestAuditResources_LegacyProject(t *testing.T) {
	stubDeclarer(t, nil, nil)
	dir := writeDBProject(t, "name: shop\nmodule_path: example.com/shop\n", nil)
	if err := os.MkdirAll(filepath.Join(dir, "db", "migrations"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadProjectDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	cat := auditResources(cfg, dir)
	if cat.Status != "ok" {
		t.Fatalf("status %s: %s", cat.Status, cat.Summary)
	}
	raw, _ := json.Marshal(cat.Details)
	var details struct {
		Databases []map[string]any `json:"databases"`
		APIs      []map[string]any `json:"apis"`
	}
	if err := json.Unmarshal(raw, &details); err != nil {
		t.Fatal(err)
	}
	if len(details.Databases) != 1 || details.Databases[0]["origin"] != "derived" || details.Databases[0]["migrations_dir"] != "db/migrations" || details.Databases[0]["primary"] != true {
		t.Errorf("databases = %+v", details.Databases)
	}
	if len(details.APIs) != 0 {
		t.Errorf("a project with no proto services derives no API: %+v", details.APIs)
	}

	// A forge.yaml that declares the database says so.
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: shop\nmodule_path: example.com/shop\ndatabase:\n  migrations_dir: db/migrations\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat = auditResources(cfg, dir)
	raw, _ = json.Marshal(cat.Details)
	_ = json.Unmarshal(raw, &details)
	if details.Databases[0]["origin"] != "forge.yaml" {
		t.Errorf("origin = %v, want forge.yaml", details.Databases[0]["origin"])
	}
}

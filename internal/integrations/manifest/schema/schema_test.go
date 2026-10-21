package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// The checked-in schema files are generated; this fails when they are stale.
// Fix: go generate ./internal/integrations/manifest/schema (make
// generate-manifest-schema).
func TestPublishedSchemasAreCurrent(t *testing.T) {
	want, err := Generate(ProtoGoFile)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range want {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v (run go generate ./internal/integrations/manifest/schema)", name, err)
		}
		if !bytes.Equal(got, b) {
			t.Errorf("%s is stale: run go generate ./internal/integrations/manifest/schema (make generate-manifest-schema)", name)
		}
	}
}

func compile(t *testing.T) *gojsonschema.Resolved {
	t.Helper()
	b, err := os.ReadFile(ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	var s gojsonschema.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	r, err := s.Resolve(nil)
	if err != nil {
		t.Fatalf("the published schema does not resolve: %v", err)
	}
	return r
}

func validate(t *testing.T, r *gojsonschema.Resolved, doc []byte) error {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	return r.Validate(v)
}

// Every embedded manifest, assembled from its fragments, satisfies the
// published schema: the schema describes what the loader accepts.
func TestCatalogManifestsSatisfyTheSchema(t *testing.T) {
	r := compile(t)
	catalogDir := filepath.Join("..", "..", "catalog")
	files, err := filepath.Glob(filepath.Join(catalogDir, "*", "manifest.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no manifests under %s: %v", catalogDir, err)
	}
	for _, f := range files {
		rel, _ := filepath.Rel(catalogDir, f)
		doc, err := manifest.AssembledJSONDir(catalogDir, filepath.ToSlash(rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if err := validate(t, r, doc); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestSchemaRejectsWhatTheLoaderRejects(t *testing.T) {
	r := compile(t)
	const base = `{"id":"demo","version":1,"connection":{"base_url":"https://api.example.com"},
	  "actions":[{"id":"thing.get","placement":"server","request":{"method":"GET","path":"/x"}}]}`
	if err := validate(t, r, []byte(base)); err != nil {
		t.Fatalf("base manifest: %v", err)
	}
	cases := map[string]string{
		"unknown field":     `{"id":"demo","version":1,"bogus":1}`,
		"bad id":            `{"id":"Demo","version":1}`,
		"version 0":         `{"id":"demo","version":0}`,
		"bad placement":     `{"id":"demo","version":1,"actions":[{"id":"a","placement":"cloud"}]}`,
		"bad method":        `{"id":"demo","version":1,"actions":[{"id":"a","placement":"server","request":{"method":"FETCH","path":"/"}}]}`,
		"params not obj":    `{"id":"demo","version":1,"actions":[{"id":"a","placement":"server","params":{"type":"string"}}]}`,
		"two auth arms":     `{"id":"demo","version":1,"connection":{"auth":[{"basic":{},"delegated":{"broker":"b"}}]}}`,
		"trigger no events": `{"id":"demo","version":1,"triggers":[{"id":"t","data":{"type":"object"}}]}`,
		"bad pkce":          `{"id":"demo","version":1,"connection":{"auth":[{"oauth2":{"pkce":"S512"}}]}}`,
		"bad keyword":       `{"id":"demo","version":1,"keywords":["Upper"]}`,
		"param_order set":   `{"id":"demo","version":1,"actions":[{"id":"a","placement":"server","param_order":["x"]}]}`,
	}
	for name, doc := range cases {
		if err := validate(t, r, []byte(doc)); err == nil {
			t.Errorf("%s: the schema accepted %s", name, strings.Join(strings.Fields(doc), " "))
		}
	}
}

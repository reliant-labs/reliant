package cli

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/reliant-labs/forge/internal/schemagen"
)

const envSchemaPath = "../../schema/env.v1.schema.json"

// TestEnvSchemaIsCurrent regenerates the env schema from the Go types (with
// their doc comments, read from source) and fails when the committed file
// differs. FORGE_UPDATE_GOLDEN=1 rewrites it.
func TestEnvSchemaIsCurrent(t *testing.T) {
	got, err := generateEnvSchema(newSourceDescriber(t, "../.."))
	if err != nil {
		t.Fatalf("generate env schema: %v", err)
	}
	if os.Getenv("FORGE_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(envSchemaPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(envSchemaPath)
	if err != nil {
		t.Fatalf("read %s (FORGE_UPDATE_GOLDEN=1 writes it): %v", envSchemaPath, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("schema/env.v1.schema.json is stale: a type the env document decodes into changed.\n" +
			"Regenerate it: FORGE_UPDATE_GOLDEN=1 go test ./internal/cli -run TestEnvSchemaIsCurrent")
	}
	if !bytes.Equal(want, envSchemaJSON()) {
		t.Errorf("the embedded schema (schema.EnvV1) is not the committed file")
	}
}

// TestEnvSchemaRenderContractsValidate: every render-contract golden — the
// document a real KCL env renders — conforms to the schema, under forge's own
// validator and under an independent draft 2020-12 implementation, so the
// schema is a real JSON Schema and not only one forge agrees with.
func TestEnvSchemaRenderContractsValidate(t *testing.T) {
	compiled, err := schemagen.Compile(envSchemaJSON())
	if err != nil {
		t.Fatalf("compile env schema: %v", err)
	}
	var std jsonschema.Schema
	if err := json.Unmarshal(envSchemaJSON(), &std); err != nil {
		t.Fatalf("parse env schema as JSON Schema: %v", err)
	}
	resolved, err := std.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve env schema: %v", err)
	}
	files, _ := filepath.Glob("testdata/render_contract/*.json")
	if len(files) == 0 {
		t.Fatal("no render_contract goldens")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			raw, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			var wrapper struct {
				Output json.RawMessage `json:"output"`
			}
			if err := json.Unmarshal(raw, &wrapper); err != nil {
				t.Fatal(err)
			}
			doc, err := schemagen.DecodeJSON(wrapper.Output)
			if err != nil {
				t.Fatal(err)
			}
			if errs := compiled.Validate(doc); len(errs) > 0 {
				t.Errorf("does not conform to schema/env.v1.schema.json:\n%v", errs)
			}
			var plain any
			if err := json.Unmarshal(wrapper.Output, &plain); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(plain); err != nil {
				t.Errorf("an independent JSON Schema validator rejects it: %v", err)
			}
		})
	}
}

// TestEnvSchemaUnionsMatchDispatch: the schema's runtime and build unions are
// exactly the tags the parser dispatches on, so neither can grow a variant the
// other does not know.
func TestEnvSchemaUnionsMatchDispatch(t *testing.T) {
	for tag := range envRuntimeVariants {
		if _, err := dispatchRuntime("w", json.RawMessage(`{"type":"`+tag+`"}`)); err != nil && strings.Contains(err.Error(), "unrecognised") {
			t.Errorf("schema runtime %q is not one dispatchRuntime decodes: %v", tag, err)
		}
	}
	for _, tag := range []string{RuntimeHost, RuntimeCompose, RuntimeCluster, RuntimeHosted, RuntimeBuildOnly, RuntimeExternal} {
		if _, ok := envRuntimeVariants[tag]; !ok {
			t.Errorf("dispatchRuntime decodes runtime %q, which the schema does not declare", tag)
		}
	}
	for tag := range envBuildVariants {
		if _, err := dispatchBuild("w", json.RawMessage(`{"type":"`+tag+`"}`)); err != nil {
			t.Errorf("schema build %q is not one dispatchBuild decodes: %v", tag, err)
		}
	}
	if _, err := dispatchBuild("w", json.RawMessage(`{"type":"nope"}`)); err == nil {
		t.Fatal("dispatchBuild accepted an unknown type; this test can no longer tell")
	}
}

// TestEnvSchemaKeywordsAreImplemented: the generated schema uses only keywords
// forge's validator implements (Compile refuses any other).
func TestEnvSchemaKeywordsAreImplemented(t *testing.T) {
	if _, err := schemagen.Compile(envSchemaJSON()); err != nil {
		t.Fatal(err)
	}
}

// TestProjectSchemaPrintsTheEmbeddedSchema: `forge project schema env` prints
// exactly the schema this binary validates with, and capabilities lists it.
func TestProjectSchemaPrintsTheEmbeddedSchema(t *testing.T) {
	var out bytes.Buffer
	cmd := newProjectSchemaCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"env"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), envSchemaJSON()) {
		t.Fatal("forge project schema env is not the embedded schema")
	}
	var head struct {
		Dialect string `json:"$schema"`
		ID      string `json:"$id"`
	}
	if err := json.Unmarshal(out.Bytes(), &head); err != nil || head.Dialect != schemagen.Draft || head.ID != envSchemaID {
		t.Fatalf("schema header = %+v (%v)", head, err)
	}
	if err := runProjectSchema(&out, "nope"); err == nil || !strings.Contains(err.Error(), "env") {
		t.Fatalf("unknown schema name: %v", err)
	}
	found := false
	for _, s := range buildCapabilitiesSpec().Schemas {
		found = found || (s.Name == "env" && s.APIVersion == envDocumentAPIVersion)
	}
	if !found {
		t.Fatal("forge project capabilities does not list the env schema")
	}
}

// newSourceDescriber returns a schemagen Describe func reading doc comments
// from the module's source: the first paragraph of a type's or a field's
// comment. Only files declaring a requested type are parsed.
func newSourceDescriber(t *testing.T, moduleRoot string) func(reflect.Type, string) string {
	t.Helper()
	const module = "github.com/reliant-labs/forge"
	type key struct{ pkg, typ, field string }
	docs := map[key]string{}
	parsed := map[string]bool{}
	fset := token.NewFileSet()
	load := func(pkgPath, typ string) {
		dir := strings.TrimPrefix(strings.TrimPrefix(pkgPath, module), "/")
		cacheKey := pkgPath + "\x00" + typ
		if parsed[cacheKey] || !strings.HasPrefix(pkgPath, module) {
			return
		}
		parsed[cacheKey] = true
		files, _ := filepath.Glob(filepath.Join(moduleRoot, dir, "*.go"))
		sort.Strings(files)
		decl := regexp.MustCompile(`(?m)^(type\s+|\t)` + regexp.QuoteMeta(typ) + `\s+struct\b`)
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil || !decl.Match(src) {
				continue
			}
			af, err := parser.ParseFile(fset, f, src, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			for _, d := range af.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, sp := range gd.Specs {
					ts := sp.(*ast.TypeSpec)
					if ts.Name.Name != typ {
						continue
					}
					doc := ts.Doc
					if doc == nil && len(gd.Specs) == 1 {
						doc = gd.Doc
					}
					docs[key{pkgPath, typ, ""}] = firstParagraph(doc)
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, fld := range st.Fields.List {
						c := fld.Doc
						if c == nil {
							c = fld.Comment
						}
						for _, n := range fld.Names {
							docs[key{pkgPath, typ, n.Name}] = firstParagraph(c)
						}
					}
				}
			}
		}
	}
	return func(rt reflect.Type, field string) string {
		if rt.Name() == "" {
			return ""
		}
		load(rt.PkgPath(), rt.Name())
		return docs[key{rt.PkgPath(), rt.Name(), field}]
	}
}

// firstParagraph is a comment's first paragraph on one line, without the
// lint directives a doc comment may carry.
func firstParagraph(cg *ast.CommentGroup) string {
	if cg == nil {
		return ""
	}
	text := cg.Text()
	para, _, _ := strings.Cut(strings.TrimSpace(text), "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

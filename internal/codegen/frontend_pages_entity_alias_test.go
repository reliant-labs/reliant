package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jinzhu/inflection"
)

// An entity named `Link` made the born list page declare `Link` twice —
// `import type { Link } from "@/gen/…_pb"` beside `import Link from
// "next/link"` — and the scaffolded frontend failed its typecheck before the
// user wrote a line (found by an outside evaluator's first project, "linkbox").
// `Image`, `Head`, `Script` and `Form` are next/* default exports, and every
// name a page imports for itself — @tanstack/react-router's Link, a React hook,
// the component library's Button, Input or Resource — collides the same way.
//
// So a page binds the entity type under naming.EntityTypeLocal, never under
// the entity's own name. This renders every CRUD page, Next.js and Vite, for
// entities named after what the pages import, and requires each identifier to
// be bound once. TestE2EFrameworkNamedEntitiesTypecheck runs tsc over a real
// project with such entities.

// frameworkNamedEntities are entity names a generated page also imports, or
// may: next/* default exports, router and React identifiers, and components
// the pages import from the library.
var frameworkNamedEntities = []string{
	"Link", "Image", "Head", "Script", "Form", "Router",
	"Resource", "Button", "Input", "Suspense", "Plus", "PageHeader",
}

// crudPageForEntity builds the page data for a born entity `name` served by
// all five CRUD rpcs, shaped like the real descriptor (enumSvcForTest).
func crudPageForEntity(t *testing.T, name string) PageTemplateData {
	t.Helper()
	plural := inflection.Plural(name)
	pkg := "services.things.v1"
	svc := ServiceDef{
		Name:      "ThingService",
		Package:   pkg,
		ProtoFile: "proto/services/things/v1/things.proto",
		Methods: []Method{
			{Name: "List" + plural, InputType: "List" + plural + "Request", OutputType: "List" + plural + "Response",
				InputTypeFQ: pkg + ".List" + plural + "Request"},
			{Name: "Get" + name, InputType: "Get" + name + "Request", OutputType: "Get" + name + "Response"},
			{Name: "Create" + name, InputType: "Create" + name + "Request", OutputType: "Create" + name + "Response",
				InputTypeFQ: pkg + ".Create" + name + "Request"},
			{Name: "Update" + name, InputType: "Update" + name + "Request", OutputType: "Update" + name + "Response",
				InputTypeFQ: pkg + ".Update" + name + "Request"},
			{Name: "Delete" + name, InputType: "Delete" + name + "Request", OutputType: "Delete" + name + "Response"},
		},
		Messages: map[string][]MessageFieldDef{
			"List" + plural + "Response": {
				{Name: strings.ToLower(plural), ProtoType: "[]message", MessageType: pkg + "." + name},
				{Name: "next_page_token", ProtoType: "string"},
			},
			"Create" + name + "Request": {
				{Name: "title", ProtoType: "string"},
			},
			"Update" + name + "Request": {
				{Name: strings.ToLower(name), ProtoType: "message", MessageType: pkg + "." + name},
				{Name: "update_mask", ProtoType: "message", MessageType: "google.protobuf.FieldMask"},
			},
		},
		Schemas: map[string][]SchemaFieldDef{
			pkg + ".List" + plural + "Request": {
				{Name: "page_size", Kind: "int32"},
				{Name: "page_token", Kind: "string"},
			},
			pkg + ".Create" + name + "Request": {
				{Name: "title", Kind: "string"},
			},
			pkg + ".Update" + name + "Request": {
				{Name: strings.ToLower(name), Kind: "message", TypeName: pkg + "." + name},
				{Name: "update_mask", Kind: "message", TypeName: "google.protobuf.FieldMask"},
			},
			pkg + "." + name: {
				{Name: "id", Kind: "string"},
				{Name: "title", Kind: "string"},
			},
		},
	}
	pages := ExtractCRUDEntities(svc)
	if len(pages) != 1 {
		t.Fatalf("entity %s: expected 1 CRUD entity, got %d", name, len(pages))
	}
	page := pages[0]
	AttachEntityMeta(&page, EntityDef{
		Name:    name,
		PkField: "id",
		Fields: []EntityField{
			{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
			{Name: "title", ProtoType: "string", Kind: FieldKindScalar},
		},
	}, svc)
	return page
}

var (
	// tsImportStmtRe matches one import statement, single- or multi-line,
	// up to its `from "…"` (side-effect imports bind nothing).
	tsImportStmtRe = regexp.MustCompile(`(?ms)^import\s+(.+?)\s+from\s+["'][^"']+["'];?`)
	// tsTopLevelDeclRe matches a top-level function, class, const, let,
	// type or interface declaration's name.
	tsTopLevelDeclRe = regexp.MustCompile(`(?m)^(?:export\s+(?:default\s+)?)?(?:async\s+)?(?:function|class|const|let|var|type|interface|enum)\s+([A-Za-z_$][\w$]*)`)
	tsIdentRe        = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
)

// tsBindings counts the names a module's imports and top-level declarations
// bind. Any count above one is a duplicate declaration tsc refuses.
func tsBindings(src string) map[string]int {
	counts := map[string]int{}
	for _, m := range tsImportStmtRe.FindAllStringSubmatch(src, -1) {
		clause := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m[1]), "type "))
		if i := strings.Index(clause, "{"); i >= 0 {
			j := strings.LastIndex(clause, "}")
			for _, spec := range strings.Split(clause[i+1:j], ",") {
				spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(spec), "type "))
				if spec == "" {
					continue
				}
				if k := strings.Index(spec, " as "); k >= 0 {
					spec = strings.TrimSpace(spec[k+4:])
				}
				counts[spec]++
			}
			clause = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(clause[:i]), ","))
		}
		if strings.HasPrefix(clause, "* as ") {
			clause = strings.TrimSpace(strings.TrimPrefix(clause, "* as "))
		}
		if tsIdentRe.MatchString(clause) {
			counts[clause]++
		}
	}
	for _, m := range tsTopLevelDeclRe.FindAllStringSubmatch(src, -1) {
		counts[m[1]]++
	}
	return counts
}

func TestPages_EntityNamedLikeAnImportBindsEveryIdentifierOnce(t *testing.T) {
	templates := []struct{ kind, name string }{
		{"pages", "list-page.tsx.tmpl"},
		{"pages", "detail-page.tsx.tmpl"},
		{"pages", "create-page.tsx.tmpl"},
		{"pages", "edit-page.tsx.tmpl"},
		{"vite-spa-pages", "list-page.tsx.tmpl"},
		{"vite-spa-pages", "detail-page.tsx.tmpl"},
		{"vite-spa-pages", "create-page.tsx.tmpl"},
		{"vite-spa-pages", "edit-page.tsx.tmpl"},
	}
	for _, entity := range frameworkNamedEntities {
		t.Run(entity, func(t *testing.T) {
			page := crudPageForEntity(t, entity)
			for _, tc := range templates {
				out := renderPageTemplate(t, tc.kind, tc.name, page)
				if strings.Contains(out, "import { use }") {
					t.Fatalf("%s/%s for entity %s: the fixture resolved no hook names:\n%s", tc.kind, tc.name, entity, out)
				}
				for name, n := range tsBindings(out) {
					if n > 1 {
						t.Errorf("%s/%s for entity %s binds %q %d times (tsc: Duplicate identifier):\n%s", tc.kind, tc.name, entity, name, n, out)
					}
				}
			}
		})
	}
}

// The alias is the whole contract: the list page imports the entity type
// under EntityTypeLocal and types its columns and <Resource> with it.
func TestListPage_ImportsTheEntityTypeUnderItsAlias(t *testing.T) {
	page := crudPageForEntity(t, "Link")
	if got, want := page.EntityTypeLocal(), "LinkEntity"; got != want {
		t.Fatalf("EntityTypeLocal() = %q, want %q", got, want)
	}
	for _, kind := range []string{"pages", "vite-spa-pages"} {
		out := renderPageTemplate(t, kind, "list-page.tsx.tmpl", page)
		for _, want := range []string{
			`import type { Link as LinkEntity } from "@/gen/services/things/v1/things_pb";`,
			"const columns: ResourceColumn<LinkEntity>[] = [",
			"<Resource<LinkEntity>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s list page should contain %q:\n%s", kind, want, out)
			}
		}
		for _, bad := range []string{"import type { Link }", "ResourceColumn<Link>", "<Resource<Link>"} {
			if strings.Contains(out, bad) {
				t.Errorf("%s list page still binds the entity type under its own name (%q):\n%s", kind, bad, out)
			}
		}
	}
}

// `forge fix entity-type-alias` repairs pages scaffolded before the alias, and
// its tests hold its output to internal/codemod/testdata/entitytypealias's
// afters. This holds those afters to what the templates render today, so the
// codemod writes exactly the page a new project gets, and neither can drift
// alone.
func TestListPage_RendersWhatTheEntityTypeAliasCodemodWrites(t *testing.T) {
	page := crudPageForEntity(t, "Link")
	for kind, file := range map[string]string{"pages": "next-list.after.tsx", "vite-spa-pages": "vite-list.after.tsx"} {
		want, err := os.ReadFile(filepath.Join("..", "codemod", "testdata", "entitytypealias", file))
		if err != nil {
			t.Fatal(err)
		}
		if got := renderPageTemplate(t, kind, "list-page.tsx.tmpl", page); got != string(want) {
			t.Errorf("%s list page for entity Link no longer matches internal/codemod/testdata/entitytypealias/%s; "+
				"regenerate the fixture's after (and its before: the after with the alias removed) from this render:\n%s", kind, file, got)
		}
	}
}

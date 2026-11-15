package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// crud_owner_append_test.go pins what a scoped shim APPENDED to an existing
// handlers_crud.go reads its owner from (crud_owner_source.go).
//
// The scenario is growth's: every wrapper's FORGE_SCAFFOLD placeholder had
// been replaced with `owner, err := tenancy.TenantID(ctx)`, a hand-written
// List was deleted, and generate appended a replacement scoped by
// `claims.UserID` — a user id, compared against tenant ids.

// placeholderBlock is the birth scaffold's owner resolution, as rendered.
var placeholderBlock = regexp.MustCompile(`(?s)\tclaims, err := middleware\.GetUser\(ctx\)\n\tif err != nil \{\n\t\treturn nil, err // connect\.CodeUnauthenticated\n\t\}\n\t// FORGE_SCAFFOLD:.*?\n\towner := claims\.UserID\n`)

// resolverBlock is a customized owner resolution through fn.
func resolverBlock(fn string) string {
	return "\towner, err := " + fn + "(ctx)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n"
}

// bornOwnerShim scaffolds the crews quintet (company_id is forge:owner)
// and returns the project dir and the shim's path.
func bornOwnerShim(t *testing.T) (projectDir, shimPath string) {
	t.Helper()
	projectDir, handlerDir := ownerTestHandlerDir(t)
	generateCrews(t, projectDir)
	return projectDir, filepath.Join(handlerDir, "handlers_crud.go")
}

func generateCrews(t *testing.T, projectDir string) {
	t.Helper()
	svc, entities := ownerTestServiceAndEntity(true, true)
	if _, err := GenerateCRUDHandlers(svc, MatchCRUDMethods(svc, entities), "example.com/test", projectDir, nil); err != nil {
		t.Fatalf("GenerateCRUDHandlers: %v", err)
	}
}

// customizeOwners rewrites each named method's placeholder to block, and
// imports tenancy instead of the now-unused middleware.
func customizeOwners(t *testing.T, src string, blocks map[string]string) string {
	t.Helper()
	for method, block := range blocks {
		start := strings.Index(src, "func (s *Service) "+method+"(")
		if start < 0 {
			t.Fatalf("no method %s in shim", method)
		}
		end := start + 1 + strings.Index(src[start+1:], "\nfunc (s *Service) ")
		if end <= start {
			end = len(src)
		}
		body := src[start:end]
		if !placeholderBlock.MatchString(body) {
			t.Fatalf("%s has no placeholder block:\n%s", method, body)
		}
		src = src[:start] + placeholderBlock.ReplaceAllLiteralString(body, block) + src[end:]
	}
	if !strings.Contains(src, "\n\tclaims, err := middleware.GetUser(ctx)\n") {
		src = strings.Replace(src, "\t\"example.com/test/pkg/middleware\"\n", "\t\"example.com/test/internal/tenancy\"\n", 1)
	}
	return src
}

// deleteMethods cuts the named methods (and their doc comments) out of src.
func deleteMethods(t *testing.T, src string, names ...string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "shim.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	type span struct{ from, to int }
	var cuts []span
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		for _, n := range names {
			if fn.Name.Name == n {
				from := fn.Pos()
				if fn.Doc != nil {
					from = fn.Doc.Pos()
				}
				cuts = append(cuts, span{fset.Position(from).Offset, fset.Position(fn.End()).Offset})
			}
		}
	}
	if len(cuts) != len(names) {
		t.Fatalf("deleteMethods: found %d of %v", len(cuts), names)
	}
	for i := len(cuts) - 1; i >= 0; i-- {
		src = src[:cuts[i].from] + src[cuts[i].to:]
	}
	return src
}

// methodSource returns one method's source from src.
func methodSource(t *testing.T, src, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "shim.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("appended shim is not valid Go: %v\n%s", err, src)
	}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			from := fn.Pos()
			if fn.Doc != nil {
				from = fn.Doc.Pos()
			}
			return src[fset.Position(from).Offset:fset.Position(fn.End()).Offset]
		}
	}
	t.Fatalf("no method %s in:\n%s", name, src)
	return ""
}

func regenerateAfter(t *testing.T, projectDir, shimPath, edited string) string {
	t.Helper()
	if err := os.WriteFile(shimPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	generateCrews(t, projectDir)
	out, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// importCount counts the import specs of src naming importPath.
func importCount(t *testing.T, src, importPath string) int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "shim.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse imports: %v", err)
	}
	n := 0
	for _, imp := range f.Imports {
		if imp.Path.Value == `"`+importPath+`"` {
			n++
		}
	}
	return n
}

func readShim(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAppendedShim_ReusesTheSiblingsResolver is the growth scenario.
func TestAppendedShim_ReusesTheSiblingsResolver(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	all := map[string]string{}
	for _, m := range []string{"CreateCrew", "GetCrew", "ListCrews", "UpdateCrew", "DeleteCrew"} {
		all[m] = resolverBlock("tenancy.TenantID")
	}
	edited := deleteMethods(t, customizeOwners(t, readShim(t, shimPath), all), "ListCrews")

	got := regenerateAfter(t, projectDir, shimPath, edited)
	list := methodSource(t, got, "ListCrews")

	if !strings.Contains(list, resolverBlock("tenancy.TenantID")) {
		t.Errorf("appended ListCrews must resolve its owner as its siblings do:\n%s", list)
	}
	for _, unwanted := range []string{"claims.UserID", "middleware.GetUser", "FORGE_SCAFFOLD"} {
		if strings.Contains(list, unwanted) {
			t.Errorf("appended ListCrews must not carry %q:\n%s", unwanted, list)
		}
	}
	if !strings.Contains(list, `orm.WhereEq("company_id", owner)`) {
		t.Errorf("appended ListCrews must still scope the query:\n%s", list)
	}
	if !strings.Contains(list, "via tenancy.TenantID(ctx), the resolver this file's other company_id wrappers call") {
		t.Errorf("the Auth line must name the reused resolver:\n%s", list)
	}
	// The import it needs is the one the file already has; forge's seam is
	// not added back, since nothing calls it (an unused import does not
	// compile).
	if n := importCount(t, got, "example.com/test/pkg/middleware"); n != 0 {
		t.Errorf("no method calls the seam, so it must not be imported:\n%s", got)
	}
	if n := importCount(t, got, "example.com/test/internal/tenancy"); n != 1 {
		t.Errorf("tenancy imported %d times, want 1", n)
	}
}

// TestAppendedShim_ClassAffinity: a read borrows the reads' resolver even
// when writes use a stricter one.
func TestAppendedShim_ClassAffinity(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	edited := customizeOwners(t, readShim(t, shimPath), map[string]string{
		"CreateCrew": resolverBlock("tenancy.RequireAdmin"),
		"UpdateCrew": resolverBlock("tenancy.RequireAdmin"),
		"DeleteCrew": resolverBlock("tenancy.RequireAdmin"),
		"GetCrew":    resolverBlock("tenancy.TenantID"),
		"ListCrews":  resolverBlock("tenancy.TenantID"),
	})
	edited = deleteMethods(t, edited, "ListCrews", "DeleteCrew")
	got := regenerateAfter(t, projectDir, shimPath, edited)
	if list := methodSource(t, got, "ListCrews"); !strings.Contains(list, resolverBlock("tenancy.TenantID")) {
		t.Errorf("a read must reuse the reads' resolver:\n%s", list)
	}
	if del := methodSource(t, got, "DeleteCrew"); !strings.Contains(del, resolverBlock("tenancy.RequireAdmin")) {
		t.Errorf("a write must reuse the writes' resolver:\n%s", del)
	}
}

// TestAppendedShim_DisagreeingSiblingsDoNotCompile: when the siblings of the
// same class disagree, forge refuses to pick a principal. The wrapper
// calls an identifier declared nowhere, so the build names the choice.
func TestAppendedShim_DisagreeingSiblingsDoNotCompile(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	edited := customizeOwners(t, readShim(t, shimPath), map[string]string{
		"CreateCrew": resolverBlock("tenancy.TenantID"),
		"GetCrew":    resolverBlock("tenancy.TenantID"),
		"ListCrews":  resolverBlock("tenancy.TenantID"),
		"UpdateCrew": resolverBlock("tenancy.RequireAdmin"),
		"DeleteCrew": resolverBlock("tenancy.TenantID"),
	})
	edited = deleteMethods(t, edited, "CreateCrew")
	got := regenerateAfter(t, projectDir, shimPath, edited)
	create := methodSource(t, got, "CreateCrew")

	if !strings.Contains(create, "owner, err := FORGE_TODO_choose_company_id_resolver(ctx)") {
		t.Errorf("an undetermined owner must be a call to an undeclared identifier:\n%s", create)
	}
	for _, want := range []string{"FORGE_SCAFFOLD", "tenancy.RequireAdmin(ctx), tenancy.TenantID(ctx)", "DOES NOT COMPILE"} {
		if !strings.Contains(create, want) {
			t.Errorf("undetermined wrapper must say %q:\n%s", want, create)
		}
	}
	if strings.Contains(create, "claims.UserID") {
		t.Errorf("undetermined wrapper must not fall back to the placeholder:\n%s", create)
	}
	if !strings.Contains(create, "e.CompanyID = owner") {
		t.Errorf("the stamp must still be there for when the resolver is chosen:\n%s", create)
	}
	if n := importCount(t, got, "example.com/test/pkg/middleware"); n != 0 {
		t.Errorf("the stub calls no seam, so none is imported:\n%s", got)
	}
}

// TestAppendedShim_UntraceableSiblingDoesNotCompile: a sibling whose owner
// forge cannot trace (here, read off the request) leaves the policy
// unreadable, so the new wrapper is undetermined too.
func TestAppendedShim_UntraceableSiblingDoesNotCompile(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	edited := customizeOwners(t, readShim(t, shimPath), map[string]string{
		"CreateCrew": resolverBlock("tenancy.TenantID"),
		"GetCrew":    "\towner := req.Msg.CompanyId\n",
		"ListCrews":  resolverBlock("tenancy.TenantID"),
		"UpdateCrew": resolverBlock("tenancy.TenantID"),
		"DeleteCrew": resolverBlock("tenancy.TenantID"),
	})
	edited = deleteMethods(t, edited, "ListCrews")
	got := regenerateAfter(t, projectDir, shimPath, edited)
	list := methodSource(t, got, "ListCrews")
	if !strings.Contains(list, "FORGE_TODO_choose_company_id_resolver(ctx)") || !strings.Contains(list, "req.Msg.CompanyId") {
		t.Errorf("an untraceable sibling must make the new wrapper undetermined, naming it:\n%s", list)
	}
}

// TestAppendedShim_ClaimsFieldMapping: siblings that map the owner from a
// claims field other than the placeholder's are followed, marker-free.
func TestAppendedShim_ClaimsFieldMapping(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	src := readShim(t, shimPath)
	src = placeholderBlock.ReplaceAllLiteralString(src,
		"\tclaims, err := middleware.GetUser(ctx)\n\tif err != nil {\n\t\treturn nil, err // connect.CodeUnauthenticated\n\t}\n\towner := claims.OrgID\n")
	got := regenerateAfter(t, projectDir, shimPath, deleteMethods(t, src, "GetCrew"))
	get := methodSource(t, got, "GetCrew")
	if !strings.Contains(get, "owner := claims.OrgID") || strings.Contains(get, "FORGE_SCAFFOLD") {
		t.Errorf("appended GetCrew must reuse the siblings' claims.OrgID mapping:\n%s", get)
	}
	if n := importCount(t, got, "example.com/test/pkg/middleware"); n != 1 {
		t.Errorf("middleware imported %d times, want 1", n)
	}
}

// TestAppendedShim_PlaceholderSiblingsKeepThePlaceholder: an unfinished
// file (every sibling still on forge's placeholder) has no policy to copy;
// the appended wrapper matches its siblings, marker and all.
func TestAppendedShim_PlaceholderSiblingsKeepThePlaceholder(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	got := regenerateAfter(t, projectDir, shimPath, deleteMethods(t, readShim(t, shimPath), "ListCrews"))
	list := methodSource(t, got, "ListCrews")
	if !placeholderBlock.MatchString(list) {
		t.Errorf("with only placeholder siblings, the placeholder is kept:\n%s", list)
	}
}

// TestAppendedShim_FallsBackToTheProjectsWrappers: a file with no wrapper
// on the column borrows the resolver the project's other handlers_crud.go
// files use for it, and imports it.
func TestAppendedShim_FallsBackToTheProjectsWrappers(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	otherDir := filepath.Join(projectDir, "internal", "handlers", "fleets")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	other := `package fleets

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	"example.com/test/internal/tenancy"
	pb "example.com/test/gen/services/fleets/v1"
)

func (s *Service) GetFleet(ctx context.Context, req *connect.Request[pb.GetFleetRequest]) (*connect.Response[pb.GetFleetResponse], error) {
	tenantID, err := tenancy.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, req, orm.WhereEq("company_id", tenantID))
}
`
	if err := os.WriteFile(filepath.Join(otherDir, "handlers_crud.go"), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	// Keep only the header and imports of the crews shim: no evidence there.
	src := readShim(t, shimPath)
	src = deleteMethods(t, src, "CreateCrew", "GetCrew", "ListCrews", "UpdateCrew", "DeleteCrew")
	got := regenerateAfter(t, projectDir, shimPath, src)
	if list := methodSource(t, got, "ListCrews"); !strings.Contains(list, resolverBlock("tenancy.TenantID")) {
		t.Errorf("ListCrews must borrow the project's company_id resolver:\n%s", list)
	}
	if importCount(t, got, "example.com/test/internal/tenancy") != 1 {
		t.Errorf("the borrowed resolver's package must be imported:\n%s", got)
	}
}

// TestScanCRUDOwnerSources_Shapes pins the classifier on the spellings it
// must tell apart.
func TestScanCRUDOwnerSources_Shapes(t *testing.T) {
	src := `package p

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	tn "example.com/app/internal/tenancy"
	"example.com/app/pkg/middleware"
)

func (s *Service) GetA(ctx context.Context, req *connect.Request[pb.GetARequest]) (*connect.Response[pb.GetAResponse], error) {
	tenantID, err := tn.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	owner := tenantID
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}

func (s *Service) CreateA(ctx context.Context, req *connect.Request[pb.CreateARequest]) (*connect.Response[pb.CreateAResponse], error) {
	claims, err := middleware.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	owner := claims.UserID
	build := func(e *db.A) { e.TenantID = owner }
	_ = build
	return nil, nil
}

func (s *Service) ListA(ctx context.Context, req *connect.Request[pb.ListARequest]) (*connect.Response[pb.ListAResponse], error) {
	_ = orm.WhereEq("tenant_id", req.Msg.TenantId)
	owner := req.Msg.TenantId
	_ = orm.WhereEq("tenant_id", owner)
	return nil, nil
}

// scoped is a helper: its owner is a parameter, not a policy.
func (s *Service) scoped(ctx context.Context, tenantID string) orm.QueryOption {
	return orm.WhereEq("tenant_id", tenantID)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	uses := ScanCRUDOwnerSources(fset, f)
	if len(uses) != 3 {
		t.Fatalf("want 3 uses (GetA, CreateA, ListA), got %d: %+v", len(uses), uses)
	}
	get, create, list := uses[0], uses[1], uses[2]
	if get.Method != "GetA" || get.Column != "tenant_id" || !get.Read ||
		get.Source.Kind != CRUDOwnerFromResolver || get.Source.Func != "tn.TenantID" ||
		get.Source.Import != "tn example.com/app/internal/tenancy" {
		t.Errorf("GetA: renamed resolver result not traced: %+v", get)
	}
	if create.Method != "CreateA" || create.Field != "TenantID" || create.Read ||
		!create.Source.IsPlaceholder() {
		t.Errorf("CreateA: placeholder stamp not recognised: %+v", create)
	}
	if list.Method != "ListA" || list.Source.Kind != CRUDOwnerUnknown || list.Source.Expr != "req.Msg.TenantId" {
		t.Errorf("ListA: a wire-sourced owner must be unknown: %+v", list)
	}
}

// TestOwnerSourceFor_EntityAndKeyEvidence pins whose wrappers count: the
// entity's own first, other entities' on the same column name only when
// the entity has none, and never for a key-owner column (tenants.id and
// users.id name different principals).
func TestOwnerSourceFor_EntityAndKeyEvidence(t *testing.T) {
	src := func(fn string) CRUDOwnerSource { return CRUDOwnerSource{Kind: CRUDOwnerFromResolver, Func: fn} }
	uses := []CRUDOwnerUse{
		{Method: "GetTenant", Entity: "Tenant", Read: true, Column: "id", Source: src("tenancy.TenantID")},
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Source: src("tenancy.TenantID")},
		{Method: "GetNote", Entity: "Note", Read: true, Column: "tenant_id", Source: src("tenancy.UserID")},
	}
	if _, ok := ownerSourceFor(uses, ownerTarget{entity: "User", column: "id", field: "ID", read: true, isPK: true}); ok {
		t.Errorf("a key-owner column must not borrow another table's id wrapper")
	}
	d, ok := ownerSourceFor(uses, ownerTarget{entity: "Note", column: "tenant_id", field: "TenantID", read: true})
	if !ok || d.Source == nil || d.Source.Func != "tenancy.UserID" {
		t.Errorf("the entity's own wrapper is the evidence: %+v", d)
	}
	d, ok = ownerSourceFor(uses, ownerTarget{entity: "Crew", column: "tenant_id", field: "TenantID", read: true})
	if !ok || d.Undetermined == "" {
		t.Errorf("other entities disagreeing on the column must be undetermined: %+v", d)
	}
}

// TestOwnerSourceFor_BorrowedEvidenceMustBeUnanimous: another entity's
// wrappers are borrowed only when all of them agree. Class affinity and
// setting placeholders aside are readings of one entity's policy; across
// entities they would hide a table that scopes the column differently.
func TestOwnerSourceFor_BorrowedEvidenceMustBeUnanimous(t *testing.T) {
	src := func(fn string) CRUDOwnerSource { return CRUDOwnerSource{Kind: CRUDOwnerFromResolver, Func: fn} }
	placeholder := CRUDOwnerSource{Kind: CRUDOwnerFromClaims, Func: crudAuthSeam, Field: "UserID"}
	target := ownerTarget{entity: "Crew", column: "tenant_id", field: "TenantID", read: true}

	// Reads agree, but another table's write uses another principal.
	byClass := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Source: src("tenancy.TenantID")},
		{Method: "CreateNote", Entity: "Note", Read: false, Field: "TenantID", Source: src("tenancy.UserID")},
	}
	if d, ok := ownerSourceFor(byClass, target); !ok || d.Undetermined == "" {
		t.Errorf("borrowed reads must not hide another entity's disagreeing write: %+v", d)
	}

	// One table resolved the column, another kept the placeholder.
	mixed := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Source: src("tenancy.TenantID")},
		{Method: "GetNote", Entity: "Note", Read: true, Column: "tenant_id", Source: placeholder},
	}
	if d, ok := ownerSourceFor(mixed, target); !ok || d.Undetermined == "" {
		t.Errorf("a placeholder beside a resolver on other entities must be undetermined: %+v", d)
	}

	// The same mix within the target's own entity: its placeholders are
	// leftovers of the file it sits in, and its own resolver decides.
	own := []CRUDOwnerUse{
		{Method: "GetCrew", Entity: "Crew", Read: true, Column: "tenant_id", Source: src("tenancy.TenantID")},
		{Method: "UpdateCrew", Entity: "Crew", Read: false, Column: "tenant_id", Source: placeholder},
	}
	if d, ok := ownerSourceFor(own, ownerTarget{entity: "Crew", column: "tenant_id", field: "TenantID", read: false}); !ok || d.Source == nil || d.Source.Func != "tenancy.TenantID" {
		t.Errorf("the entity's own decided resolver is reused: %+v", d)
	}

	// Unanimous other entities are still borrowed.
	agree := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Source: src("tenancy.TenantID")},
		{Method: "CreateNote", Entity: "Note", Read: false, Field: "TenantID", Source: src("tenancy.TenantID")},
	}
	if d, ok := ownerSourceFor(agree, target); !ok || d.Source == nil || d.Source.Func != "tenancy.TenantID" {
		t.Errorf("unanimous other entities are the evidence: %+v", d)
	}
}

// TestOwnerSourceFor_BorrowedSourcesCompareByPackage: two files may bind
// one alias to different packages, so evidence read across files is
// compared by import path, never by spelling; and a function another file
// binds without an import cannot be called from this one.
func TestOwnerSourceFor_BorrowedSourcesCompareByPackage(t *testing.T) {
	target := ownerTarget{entity: "Crew", column: "tenant_id", field: "TenantID", read: true}
	resolver := func(fn, imp string) CRUDOwnerSource {
		return CRUDOwnerSource{Kind: CRUDOwnerFromResolver, Func: fn, Import: imp}
	}

	sameAlias := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Dir: "internal/handlers/channels",
			Source: resolver("tenancy.TenantID", "tenancy example.com/test/internal/tenancy")},
		{Method: "GetNote", Entity: "Note", Read: true, Column: "tenant_id", Dir: "internal/handlers/notes",
			Source: resolver("tenancy.TenantID", "tenancy example.com/test/internal/usertenancy")},
	}
	d, ok := ownerSourceFor(sameAlias, target)
	if !ok || d.Source != nil || d.Undetermined == "" {
		t.Fatalf("one alias bound to two packages must be undetermined: %+v", d)
	}
	for _, want := range []string{"(example.com/test/internal/tenancy)", "(example.com/test/internal/usertenancy)"} {
		if !strings.Contains(d.Undetermined, want) {
			t.Errorf("the disagreement must name package %s: %s", want, d.Undetermined)
		}
	}

	// One package under two aliases is one resolver.
	twoAliases := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Dir: "internal/handlers/channels",
			Source: resolver("tenancy.TenantID", "example.com/test/internal/tenancy")},
		{Method: "GetNote", Entity: "Note", Read: true, Column: "tenant_id", Dir: "internal/handlers/notes",
			Source: resolver("tn.TenantID", "tn example.com/test/internal/tenancy")},
	}
	if d, ok := ownerSourceFor(twoAliases, target); !ok || d.Source == nil {
		t.Errorf("one package under two aliases is one resolver: %+v", d)
	}

	// Not an imported package's function: bound in the other file's package.
	local := []CRUDOwnerUse{
		{Method: "GetChannel", Entity: "Channel", Read: true, Column: "tenant_id", Dir: "internal/handlers/channels",
			Source: resolver("tenancy.TenantID", "")},
	}
	if d, ok := ownerSourceFor(local, target); !ok || d.Source != nil || d.Undetermined == "" {
		t.Errorf("a resolver another file binds without an import must not be borrowed: %+v", d)
	}
}

// TestAppendedShim_SameAliasDifferentPackagesIsUndetermined is the
// cross-file case end to end: two other handler files spell their resolver
// tenancy.TenantID but bind tenancy to different packages, so the new
// wrapper borrows neither.
func TestAppendedShim_SameAliasDifferentPackagesIsUndetermined(t *testing.T) {
	projectDir, shimPath := bornOwnerShim(t)
	write := func(svc, entity, imp string) {
		dir := filepath.Join(projectDir, "internal", "handlers", svc)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := `package ` + svc + `

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	tenancy "` + imp + `"
	pb "example.com/test/gen/services/` + svc + `/v1"
)

func (s *Service) Get` + entity + `(ctx context.Context, req *connect.Request[pb.Get` + entity + `Request]) (*connect.Response[pb.Get` + entity + `Response], error) {
	tenantID, err := tenancy.TenantID(ctx)
	if err != nil {
		return nil, err
	}
	return s.get(ctx, req, orm.WhereEq("company_id", tenantID))
}
`
		if err := os.WriteFile(filepath.Join(dir, "handlers_crud.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fleets", "Fleet", "example.com/test/internal/tenancy")
	write("depots", "Depot", "example.com/test/internal/usertenancy")

	src := deleteMethods(t, readShim(t, shimPath), "CreateCrew", "GetCrew", "ListCrews", "UpdateCrew", "DeleteCrew")
	got := regenerateAfter(t, projectDir, shimPath, src)
	list := methodSource(t, got, "ListCrews")
	if !strings.Contains(list, "owner, err := FORGE_TODO_choose_company_id_resolver(ctx)") {
		t.Errorf("ListCrews must be undetermined, not borrow either package's resolver:\n%s", list)
	}
	for _, imp := range []string{"example.com/test/internal/tenancy", "example.com/test/internal/usertenancy"} {
		if n := importCount(t, got, imp); n != 0 {
			t.Errorf("neither resolver's package may be imported (%s):\n%s", imp, got)
		}
	}
}

package codegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE UNWIRED-STUB MARKER IS A CONTRACT, SO IT NEEDS A GUARD THAT CANNOT NAME
// ITS SUBJECT.
//
// `forge scaffold rpc` emitted a placeholder handler for two months with no
// `forge:gen unwired-stub` marker on it. The pipeline's handler templates
// stamped one; the scaffold command wrote the identical method and did not.
// Nothing failed, because the only assertion in the tree named ONE path — a
// substring match on the handlers.go a particular e2e scenario produced — so
// the path it did not name was free to drift, and did.
//
// A guard that names a path only ever guards that path. These derive the set:
// a stub emitter is any source that writes forge's unwired handler-stub SHAPE,
// discovered by walking the repo and the embedded template FS, and every member
// must stamp the marker. Add an emitter and it joins the set automatically.
// Find no emitters at all and the test FAILS rather than passing vacuously —
// an empty set is how this class of guard dies quietly.
// ─────────────────────────────────────────────────────────────────────────────

// The two probes that together identify a source as an unwired-stub emitter.
// Both templates and Go builders spell them the same way, because both are
// writing the same Go method: a *Service receiver, and a body that constructs
// the scaffold-stub error.
//
// The body probe is svcerr.ScaffoldStub, NOT svcerr.Unimplemented. The two are
// deliberately different errors: ScaffoldStub is the one only forge's own
// untouched stub emits, which is what lets the scaffold test row go red when
// somebody implements the RPC. Probing for Unimplemented would re-widen this
// set to every handler that answers "not implemented" on purpose.
const (
	handlerMethodProbe   = "func (s *Service) "
	unwiredStubBodyProbe = "svcerr.ScaffoldStub("
)

// markerReferences are the two legitimate ways a source stamps the marker:
// the literal text (templates, which are not Go), or the constructor every Go
// emitter calls. Both resolve to UnwiredStubMarkerPrefix, so the marker's text
// still lives in exactly one place.
func markerReferences() []string {
	return []string{UnwiredStubMarkerPrefix, "UnwiredStubMarkerComment"}
}

// TestEveryUnwiredStubEmitterStampsTheMarker walks forge's own tree, derives
// the set of sources that emit an unwired handler stub, and requires each of
// them to stamp the marker.
//
// Test files are excluded on purpose: several build the stub shape as a
// FIXTURE (a linter's input, an audit guardrail's synthetic package) and are
// not emitters.
func TestEveryUnwiredStubEmitterStampsTheMarker(t *testing.T) {
	root := forgeRepoRoot(t)

	var emitters, unmarked []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", ".next", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".tmpl") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !bytes.Contains(src, []byte(handlerMethodProbe)) || !bytes.Contains(src, []byte(unwiredStubBodyProbe)) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		emitters = append(emitters, rel)
		for _, ref := range markerReferences() {
			if bytes.Contains(src, []byte(ref)) {
				return nil
			}
		}
		unmarked = append(unmarked, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	sort.Strings(emitters)
	sort.Strings(unmarked)

	// An empty set is the failure this guard exists to make loud: it means the
	// probes stopped matching (the stub body was reworded, the receiver
	// renamed) and every assertion below became a no-op.
	if len(emitters) == 0 {
		t.Fatalf("found NO unwired-stub emitters in %s.\n"+
			"The probes %q + %q no longer match anything forge emits, so this guard — and every\n"+
			"other assertion keyed on the stub shape — is now vacuous. Re-derive the probes from\n"+
			"whatever the emitters write today.", root, handlerMethodProbe, unwiredStubBodyProbe)
	}

	if len(unmarked) > 0 {
		t.Errorf("%d of %d unwired-stub emitter(s) do not stamp the %q marker:\n  %s\n\n"+
			"  Every path that writes an unimplemented handler must stamp it. The marker — not the\n"+
			"  file's name, not its body — is what CRUD gen's stub→shim excision, `forge project\n"+
			"  audit`, and out-of-tree orchestrators read to find handlers still awaiting work. A\n"+
			"  stub emitted without it is invisible to all three.\n\n"+
			"  Go emitters call codegen.UnwiredStubMarkerComment(pkg, method); templates carry the\n"+
			"  literal line.\n\n  Derived emitter set: %s",
			len(unmarked), len(emitters), UnwiredStubMarkerPrefix,
			strings.Join(unmarked, "\n  "), strings.Join(emitters, ", "))
	}
}

// TestServiceTemplateStubsRenderMarked closes the gap the file-level scan
// above leaves for templates: a template can stamp the marker on three of its
// four stream shapes and still contain the literal. This renders EVERY service
// template in the embedded FS — the set derived from the FS, not from a list of
// names — and checks each *Service method the render actually produces.
func TestServiceTemplateStubsRenderMarked(t *testing.T) {
	names, err := templates.ServiceTemplates().List("")
	if err != nil {
		t.Fatalf("list service templates: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("the embedded service template category is empty — nothing was checked")
	}

	// One method per stream shape, so a template that special-cases a shape
	// cannot hide an unmarked branch behind a shape this test never renders.
	data := mapServiceDefToTemplateData(ServiceDef{
		Name:       "AdminService",
		Package:    "admin.v1",
		GoPackage:  "example.com/app/gen/services/admin/v1",
		PkgName:    "adminv1",
		ProtoFile:  "proto/services/admin/v1/admin.proto",
		ModulePath: "example.com/app",
		Methods: []Method{
			{Name: "Unary", InputType: "UnaryRequest", OutputType: "UnaryResponse"},
			{Name: "ServerStream", InputType: "ServerStreamRequest", OutputType: "ServerStreamResponse", ServerStreaming: true},
			{Name: "ClientStream", InputType: "ClientStreamRequest", OutputType: "ClientStreamResponse", ClientStreaming: true},
			{Name: "BidiStream", InputType: "BidiStreamRequest", OutputType: "BidiStreamResponse", ClientStreaming: true, ServerStreaming: true},
		},
	}, t.TempDir())

	totalStubs := 0
	for _, name := range names {
		out, rerr := templates.ServiceTemplates().Render(name, data)
		if rerr != nil {
			// Templates in this category that take a different data shape
			// (the CRUD shim/ops/test templates) cannot be rendered from a
			// ServiceTemplateData. They are covered by the file-level scan
			// above; skipping them here is safe because totalStubs below
			// refuses to let the whole loop come up empty.
			continue
		}
		marked, unmarked := unwiredStubMethods(t, name, out)
		totalStubs += len(marked) + len(unmarked)
		if len(unmarked) > 0 {
			t.Errorf("service template %s renders unwired stub(s) with no %q marker: %v\n%s",
				name, UnwiredStubMarkerPrefix, unmarked, out)
		}
	}

	// Four shapes across the fresh-file template and the append fragment.
	// Asserting a floor (rather than "> 0") keeps a template that renders one
	// shape and drops the rest from reading as a pass.
	if totalStubs < 8 {
		t.Fatalf("rendered only %d unwired stub method(s) across %d service templates, want at least 8 "+
			"(four stream shapes × the fresh-file and append templates) — the loop is no longer "+
			"exercising the stub emitters", totalStubs, len(names))
	}
}

// unwiredStubMethods parses rendered Go (wrapping a bare method fragment in a
// package clause so the append template parses too) and splits the *Service
// methods whose body is forge's unwired-stub shape into those carrying the
// marker and those not.
func unwiredStubMethods(t *testing.T, label string, src []byte) (marked, unmarked []string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, label, src, parser.ParseComments)
	if err != nil {
		wrapped := append([]byte("package rendered\n\n"), src...)
		file, err = parser.ParseFile(fset, label, wrapped, parser.ParseComments)
		if err != nil {
			return nil, nil // not Go (a .sql/.yaml template) — nothing to check
		}
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name == nil || !receiverIsService(fd) {
			continue
		}
		if fd.Body == nil || len(fd.Body.List) != 1 {
			continue
		}
		ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
		if !ok || !returnMentionsUnimplemented(ret) {
			continue
		}
		if fd.Doc != nil && hasUnwiredStubMarker(fd.Doc) {
			marked = append(marked, fd.Name.Name)
			continue
		}
		unmarked = append(unmarked, fd.Name.Name)
	}
	return marked, unmarked
}

func hasUnwiredStubMarker(doc *ast.CommentGroup) bool {
	for _, c := range doc.List {
		if UnwiredStubMarkerRE.MatchString(c.Text) {
			return true
		}
	}
	return false
}

// TestGeneratedHandlerStubsAreScannable is the behavioural half for the
// generate pipeline: both the fresh-file path and the append-a-fragment path
// must leave markers that ScanUnwiredStubMethods — the canonical reader — can
// find. Asserting through the reader rather than a substring means a change to
// either side that breaks the pair fails here.
func TestGeneratedHandlerStubsAreScannable(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "admin")

	svc := ServiceDef{
		Name:       "AdminService",
		Package:    "admin.v1",
		GoPackage:  "example.com/app/gen/services/admin/v1",
		PkgName:    "adminv1",
		ProtoFile:  "proto/services/admin/v1/admin.proto",
		ModulePath: "example.com/app",
		Methods: []Method{
			{Name: "SubmitOrder", InputType: "SubmitOrderRequest", OutputType: "SubmitOrderResponse"},
			{Name: "TailOrders", InputType: "TailOrdersRequest", OutputType: "TailOrdersResponse", ServerStreaming: true},
		},
	}

	// Fresh-file path: handlers.go does not exist yet.
	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}
	got := ScanUnwiredStubMethods(targetDir)
	for _, m := range []string{"SubmitOrder", "TailOrders"} {
		if !got[m] {
			t.Errorf("fresh-file scaffold: %s carries no unwired-stub marker (scanned: %v)", m, keysOf(got))
		}
	}

	// Append path: handlers.go exists, a new RPC arrives.
	svc.Methods = append(svc.Methods, Method{
		Name: "CancelOrder", InputType: "CancelOrderRequest", OutputType: "CancelOrderResponse",
	})
	if _, err := GenerateMissingHandlerStubs(svc, projectDir, targetDir, nil, nil); err != nil {
		t.Fatalf("GenerateMissingHandlerStubs: %v", err)
	}
	if got := ScanUnwiredStubMethods(targetDir); !got["CancelOrder"] {
		t.Errorf("append path: CancelOrder carries no unwired-stub marker (scanned: %v)", keysOf(got))
	}
}

// TestExciseUnwiredStubs_MatchesTheEmittedShape feeds the excision pass the
// REAL rendered stub instead of a hand-written approximation of it.
//
// It is red without the emitted-shape fix: isPristineUnwiredStub required the
// body to mention `connect.CodeUnimplemented`, and the emitters switched to
// `svcerr.Unimplemented` two days after excision landed. From then on the pass
// matched nothing — a marked stub survived the table that should have replaced
// it with a CRUD shim, CRUD gen filtered the method out as "already
// implemented", and the RPC stayed on Unimplemented. Both halves were green
// the whole time: the excision pass has no unit test, and the e2e that would
// have caught it sits behind a build tag no CI job passes.
func TestExciseUnwiredStubs_MatchesTheEmittedShape(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "catalog")

	svc := ServiceDef{
		Name:       "CatalogService",
		Package:    "catalog.v1",
		GoPackage:  "example.com/app/gen/services/catalog/v1",
		PkgName:    "catalogv1",
		ProtoFile:  "proto/services/catalog/v1/catalog.proto",
		ModulePath: "example.com/app",
		Methods: []Method{
			{Name: "GetSprocket", InputType: "GetSprocketRequest", OutputType: "GetSprocketResponse"},
			{Name: "Ping", InputType: "PingRequest", OutputType: "PingResponse"},
		},
	}
	if err := GenerateServiceStub(svc, targetDir); err != nil {
		t.Fatalf("GenerateServiceStub: %v", err)
	}

	removed, err := ExciseUnwiredStubs(projectDir, targetDir, map[string]bool{"GetSprocket": true})
	if err != nil {
		t.Fatalf("ExciseUnwiredStubs: %v", err)
	}
	if len(removed) != 1 || removed[0] != "GetSprocket" {
		t.Fatalf("excision removed %v, want [GetSprocket] — the pass does not recognise the shape "+
			"forge's own templates emit, so a stub→CRUD transition leaves the RPC on Unimplemented", removed)
	}

	// The untargeted stub is untouched, and the targeted one is gone.
	left := ScanUnwiredStubMethods(targetDir)
	if left["GetSprocket"] {
		t.Error("GetSprocket's marker survived excision")
	}
	if !left["Ping"] {
		t.Errorf("excision removed a method it was not asked for (remaining: %v)", keysOf(left))
	}
}

// TestExciseUnwiredStubs_LeavesEditedStubs keeps the fix from over-reaching:
// widening the body match must not turn "the user implemented it" into
// something forge deletes.
func TestExciseUnwiredStubs_LeavesEditedStubs(t *testing.T) {
	projectDir := t.TempDir()
	targetDir := filepath.Join(projectDir, "internal", "handlers", "catalog")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Marker intact, body implemented — the user's code.
	src := fmt.Sprintf(`package catalog

// GetSprocket implements the GetSprocket RPC.
%s
func (s *Service) GetSprocket(ctx context.Context, req *connect.Request[pb.GetSprocketRequest]) (*connect.Response[pb.GetSprocketResponse], error) {
	row, err := db.GetSprocket(ctx, s.deps.DB, req.Msg.Id)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	return connect.NewResponse(&pb.GetSprocketResponse{Sprocket: row}), nil
}
`, UnwiredStubMarkerComment("catalog", "GetSprocket"))
	path := filepath.Join(targetDir, "handlers.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	removed, err := ExciseUnwiredStubs(projectDir, targetDir, map[string]bool{"GetSprocket": true})
	if err != nil {
		t.Fatalf("ExciseUnwiredStubs: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("excised %v — an implemented handler is the user's, marker or not", removed)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "db.GetSprocket(") {
		t.Errorf("the user's implementation was destroyed:\n%s", after)
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// testStartDir is this package's source directory: the working directory
// `go test` starts the binary in, captured before any test runs because
// crd_kcl_parse.go chdirs (and restores) while it loads a package.
var testStartDir, testStartDirErr = os.Getwd()

// forgeRepoRoot walks up from this package's directory to the forge checkout.
//
// Not runtime.Caller: under -trimpath (the workspace image's GOFLAGS default)
// that reports "github.com/reliant-labs/forge/internal/...", a module-relative
// path that is not a file on disk. internal/trimpathguard holds the tree to it.
func forgeRepoRoot(t *testing.T) string {
	t.Helper()
	if testStartDirErr != nil {
		t.Fatalf("getwd: %v", testStartDirErr)
	}
	dir := testStartDir
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil && bytes.Contains(b, []byte("module github.com/reliant-labs/forge\n")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod declaring module github.com/reliant-labs/forge found above %s", testStartDir)
		}
		dir = parent
	}
}

package codegen

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// MethodTemplateData holds per-method data for the embedded service templates.
type MethodTemplateData struct {
	Name            string // RPC method name, e.g. "GetItem"
	InputType       string // proto input message name, e.g. "GetItemRequest"
	OutputType      string // proto output message name, e.g. "GetItemResponse"
	ClientStreaming bool   // true if the client streams requests
	ServerStreaming bool   // true if the server streams responses
	// AuthRequired mirrors Method.AuthRequired: true unless the proto
	// explicitly opts out. It carries into CRUDMethodTemplateData, where
	// it selects the handler comment's wording. It gates nothing.
	AuthRequired bool
}

// GoName is the method protoc-gen-connect-go declares for this RPC on the
// service's Handler interface — what a handler must name its method
// ("CreateOauth2token" → CreateOauth2Token). Name stays the proto
// spelling, which is what descriptor lookups and CRUD matching key on.
func (m MethodTemplateData) GoName() string { return naming.GoCamelCase(m.Name) }

// InputGoName / OutputGoName are the pb Go types protoc-gen-go generates
// for the request and response messages, unqualified.
func (m MethodTemplateData) InputGoName() string { return naming.GoCamelCase(m.InputType) }

// OutputGoName — see InputGoName.
func (m MethodTemplateData) OutputGoName() string { return naming.GoCamelCase(m.OutputType) }

// ServiceTemplateData holds the data shape expected by the embedded service templates.
type ServiceTemplateData struct {
	ServiceName    string // e.g. "EchoService" (or hyphenated CLI form)
	ServicePackage string // Go package CLAUSE, e.g. "echo" (disk-resolved for existing dirs)
	// ServiceImportPath is the internal/handlers/ directory leaf used in scaffolded
	// test imports (`{{.Module}}/internal/handlers/{{.ServiceImportPath}}`). Equals
	// ServicePackage for fresh scaffolds; for EXISTING dirs it is the real
	// directory name, which may legally differ from the package clause.
	ServiceImportPath   string
	Module              string               // e.g. "github.com/demo-project"
	ProtoImportPath     string               // e.g. "proto/services/echo" (without /v1)
	ProtoPackage        string               // same as ProtoImportPath for handlers.go.tmpl
	ProtoConnectPackage string               // e.g. "echov1connect"
	HandlerName         string               // connect-go's base for the service: "EchoService" (Unimplemented<base>Handler)
	ProtoFileSymbol     string               // e.g. "File_services_echo_v1_echo_proto"
	Methods             []MethodTemplateData // method data for handlers.go.tmpl and test templates
	// UnfilledDeps describes the required Deps fields the test harness
	// cannot construct ("Deps.SQL (*sql.DB)", UnfilledDepsFor). When any
	// exist, the scaffolded tests pass WithPlaceholderDeps() so they pass
	// as written.
	UnfilledDeps []string
	// TestHelperName is the disambiguated suffix that the bootstrap testing
	// generator uses for `app.NewTest<X>` and `app.NewTest<X>Server` helpers.
	// Equal to PascalCase(ServicePackage) when there's no cross-role
	// collision, else "Svc" + PascalCase(ServicePackage) — matching
	// AssignBootstrapAliases / GenerateBootstrapTesting's collision rule.
	// Test scaffold templates reference this rather than re-pascal-casing
	// ServiceName so the call site stays in sync with the actual factory.
	TestHelperName string
	// HasConfig is whether the project has typed config
	// (ProjectHasTypedConfig): service.go imports <module>/pkg/config and
	// declares Deps.Config only then.
	HasConfig bool
}

// protoImportPath derives the relative proto import path for a service
// from its descriptor's GoPackage. GoPackage is like
// "github.com/project/gen/proto/services/echo/v1"; the result strips the
// module + "/gen/" prefix and the trailing "/v1" (or /v2, …) version
// suffix, yielding "proto/services/echo". Returns "" when either
// ModulePath or GoPackage is empty, or when GoPackage doesn't carry the
// expected module+/gen/ prefix. Single source of the derivation shared by
// mapServiceDefToTemplateData (ProtoImportPath/ProtoPackage),
// buildCRUDTemplateData, and buildCRUDTestTemplateData.
func protoImportPath(svc ServiceDef) string {
	if svc.ModulePath == "" || svc.GoPackage == "" {
		return ""
	}
	// Strip module + "/gen/" prefix and "/v1" suffix.
	prefix := svc.ModulePath + "/gen/"
	rest, ok := strings.CutPrefix(svc.GoPackage, prefix)
	if !ok {
		return ""
	}
	// Remove trailing /v1, /v2, etc.
	if idx := strings.LastIndex(rest, "/v"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

// mapServiceDefToTemplateData converts a ServiceDef to the data shape expected by embedded templates.
// projectDir is used to detect cross-role package-name collisions for the
// `app.NewTest<X>` helper name (when there's an internal/<pkg> matching the
// service package, the bootstrap testing generator emits `NewTestSvc<X>`).
// Pass an empty projectDir when the caller has no project context (test-only
// helpers); the helper falls back to the no-collision form.
func mapServiceDefToTemplateData(svc ServiceDef, projectDir ...string) ServiceTemplateData {
	pd := ""
	if len(projectDir) > 0 {
		pd = projectDir[0]
	}
	// GoPackage is like "github.com/project/gen/proto/services/echo/v1"
	// We need ProtoImportPath = "proto/services/echo" (relative, no /v1)
	importPath := protoImportPath(svc)

	connectPkg := svc.PkgName + "connect"

	// Build proto file symbol: File_services_echo_v1_echo_proto
	// The buf module root is "proto/", so protoc sees paths relative to that.
	// Strip the leading "proto/" from the filesystem path before building the symbol.
	relProtoFile := strings.TrimPrefix(svc.ProtoFile, "proto/")
	protoFileSymbol := "File_" + strings.ReplaceAll(
		strings.ReplaceAll(relProtoFile, "/", "_"),
		".", "_",
	)

	var methods []MethodTemplateData
	for _, m := range svc.Methods {
		methods = append(methods, MethodTemplateData{
			Name:            m.Name,
			InputType:       m.InputType,
			OutputType:      m.OutputType,
			ClientStreaming: m.ClientStreaming,
			ServerStreaming: m.ServerStreaming,
			AuthRequired:    m.AuthRequired,
		})
	}

	// Scaffold-time synthesis: this data shape is consumed when CREATING a
	// brand-new handler dir (GenerateServiceStub), where forge picks the
	// name. Callers that re-render into an EXISTING dir must override
	// ServicePackage / ServiceImportPath / TestHelperName from disk truth
	// — see GenerateMissingHandlerStubs's applyDiskIdentity.
	pkgName := naming.ServicePackage(svc.Name)

	return ServiceTemplateData{
		ServiceName:         pkgName,
		ServicePackage:      pkgName,
		ServiceImportPath:   pkgName,
		Module:              svc.ModulePath,
		ProtoImportPath:     importPath,
		ProtoPackage:        importPath,
		ProtoConnectPackage: connectPkg,
		HandlerName:         svc.GoName(),
		ProtoFileSymbol:     protoFileSymbol,
		Methods:             methods,
		TestHelperName:      ComputeTestHelperName(pkgName, pd),
		// With no project to look at, keep the historical shape.
		HasConfig: pd == "" || ProjectHasTypedConfig(pd),
	}
}

// GenerateMock generates a mock file for a service.
// Services with zero RPCs are skipped — there is nothing to mock.
// The result says whether the mock was written, whether that changed its
// bytes, and why not when it was not.
func GenerateMock(svc ServiceDef, projectDir, mockDir string, cs *checksums.FileChecksums) (MockResult, error) {
	if len(svc.Methods) == 0 {
		return MockResult{Skipped: "no RPCs"}, nil
	}

	// Prepare template data
	data := prepareServiceData(svc)

	// Parse and execute template
	tmpl, err := template.New("mock").Parse(mockTemplate)
	if err != nil {
		return MockResult{}, err
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return MockResult{}, err
	}

	// Synthesis is safe here: naming.ServicePackage only picks the FILENAME
	// inside the shared mocks dir. The file's package clause is the
	// constant "mocks" and its imports come from the proto descriptor's
	// GoPackage/PkgName — no handler-dir identity is referenced, so the
	// disk-first resolver isn't needed.
	// Renamed <svc>_mock.go → <svc>_mock_gen.go so the name states the tier.
	// The old spelling read like a hand-written test double, which is exactly
	// the file a developer would reach for and edit.
	relPath := MockRelPath(svc)
	if projectDir != "" {
		RetireRenamedGenerated(projectDir, filepath.Join("internal", "handlers", "mocks", naming.ServicePackage(svc.Name)+"_mock.go"), cs)
		out, err := checksums.WriteGeneratedFileOutcome(projectDir, relPath, buf.Bytes(), cs, true)
		if err != nil || out.Wrote {
			return MockResult{Wrote: out.Wrote, Changed: out.Changed}, err
		}
		return MockResult{Skipped: "the write guard kept the file on disk (hand-edited, disowned, or a pristine older render awaiting --heal)"}, nil
	}

	mockFile := filepath.Join(mockDir, filepath.Base(relPath))
	if err := os.MkdirAll(filepath.Dir(mockFile), 0o755); err != nil {
		return MockResult{}, err
	}
	prev, readErr := os.ReadFile(mockFile)
	if err := atomicfile.WriteFile(mockFile, buf.Bytes(), 0o644); err != nil {
		return MockResult{}, err
	}
	return MockResult{Wrote: true, Changed: readErr != nil || !bytes.Equal(prev, buf.Bytes())}, nil
}

// MockResult is what GenerateMock did, so generate's progress line can
// report it rather than what was asked: every re-run renders every mock,
// and almost all of them come out byte-identical.
type MockResult struct {
	// Wrote is true when the render landed: the mock is current.
	Wrote bool
	// Changed is true when the write changed the file's bytes (or
	// created it). Only a changed mock is news.
	Changed bool
	// Skipped, when Wrote is false, says why nothing was written, phrased
	// to follow "<file> not written — ".
	Skipped string
}

// MockRelPath is the project-relative path of svc's generated mock — the
// one name both GenerateMock's write and generate's progress line use, so
// the log can never name a file the run did not write (it said
// <svc>_mock.go after the file became <svc>_mock_gen.go).
func MockRelPath(svc ServiceDef) string {
	return filepath.ToSlash(filepath.Join("internal", "handlers", "mocks", naming.ServicePackage(svc.Name)+"_mock_gen.go"))
}

// ServiceRowPrefix is the name prefix of the generated per-service row
// constructors in pkg/app/services_gen.go ("serviceRow" + FieldName,
// e.g. serviceRowBilling). The cli layer's registration parser matches
// identifiers in the user-owned pkg/app/services.go by this prefix, so
// the template (services_gen.go.tmpl / services.go.tmpl) and the parser
// must agree on it.
const ServiceRowPrefix = "serviceRow"

// ServiceRowFuncName returns the canonical (no-collision) row
// constructor name for a service, accepting any spelling the codebase
// uses (proto "AdminServerService", forge.yaml "admin-server", snake
// "admin_server"). Used for user-facing messages ("add this line:");
// when a cross-role package collision renames the FieldName (rare),
// the emitted constructor carries the collision-aware name instead and
// the registration parser's normalized matching still resolves it.
func ServiceRowFuncName(svcName string) string {
	fieldName := naming.ToPascalCase(strings.TrimSuffix(svcName, "Service"))
	if fieldName == "" {
		fieldName = naming.ToPascalCase(svcName)
	}
	return ServiceRowPrefix + fieldName
}

// foreignImport records one non-`pb` protobuf package the mock must import,
// with the alias used to qualify its message types.
type foreignImport struct {
	Alias string // e.g. "reliantv1"
	Path  string // e.g. "github.com/proj/gen/reliant/v1"
}

// GoPackageForProtoFile derives the generated Go import path for a proto
// file under the buf module root. The buf module root is "proto/", and the
// go_package convention forge projects use is `<module>/gen/<dir>` where
// <dir> is the proto file's directory relative to "proto/" (e.g.
// "reliant/v1/daemon_registry.proto" → "<module>/gen/reliant/v1"). Returns
// "" when modulePath is empty or the path has no directory component.
func GoPackageForProtoFile(protoFile, modulePath string) string {
	if modulePath == "" || protoFile == "" {
		return ""
	}
	rel := strings.TrimPrefix(protoFile, "proto/")
	dir := filepath.ToSlash(filepath.Dir(rel))
	if dir == "" || dir == "." {
		return ""
	}
	return modulePath + "/gen/" + dir
}

// mockTypeResolver maps a method's input/output message to the qualified Go
// type the mock should reference. Messages declared in the service's own
// proto file (the common case) keep the `pb.` qualifier — the package the
// connect stub itself lives in. Messages pulled in from an IMPORTED proto
// file (proto-split: a thin service surface that reuses request/response
// messages from a shared package) live in a DIFFERENT generated Go package,
// so the mock must import that package under a distinct alias and qualify
// the type with it. Before this resolver every type was hardcoded to `pb.`,
// which produced `undefined: pb.X` build failures for every cross-package
// reference.
type mockTypeResolver struct {
	svc      ServiceDef
	aliases  map[string]string // go import path -> alias
	imports  []foreignImport
	needsPb  bool
	needsEmp bool
}

func newMockTypeResolver(svc ServiceDef) *mockTypeResolver {
	return &mockTypeResolver{svc: svc, aliases: map[string]string{}}
}

// qualify returns the Go type reference (e.g. "pb.GetItemRequest",
// "reliantv1.CreateDaemonTokenRequest", "emptypb.Empty") for one message,
// registering any foreign import it implies as a side effect.
func (r *mockTypeResolver) qualify(msgType, fqType, protoFile string) string {
	if msgType == "google.protobuf.Empty" {
		r.needsEmp = true
		return "emptypb.Empty"
	}
	// Short name: drop any "pkg." qualifier the descriptor may carry on
	// InputType/OutputType (it is the proto short name in practice).
	short := msgType
	if i := strings.LastIndex(short, "."); i >= 0 {
		short = short[i+1:]
	}
	// The proto short name, cased the way protoc-gen-go declares the type.
	short = naming.GoCamelCase(short)
	goPkg := GoPackageForProtoFile(protoFile, r.svc.ModulePath)
	// Same-file (or unknown provenance) → the service's own package, `pb`.
	if protoFile == "" || goPkg == "" || protoFile == r.svc.ProtoFile || goPkg == r.svc.GoPackage {
		r.needsPb = true
		return "pb." + short
	}
	alias, ok := r.aliases[goPkg]
	if !ok {
		alias = mockImportAlias(goPkg, len(r.aliases))
		r.aliases[goPkg] = alias
		r.imports = append(r.imports, foreignImport{Alias: alias, Path: goPkg})
	}
	return alias + "." + short
}

// mockImportAlias builds a deterministic, collision-free import alias for a
// generated proto package path. It uses the last two path segments
// (dir + version, e.g. "reliant/v1" → "reliantv1") which is unique across a
// project's gen/ tree; the seq suffix is a defensive tiebreaker only used if
// two distinct paths ever normalize to the same alias.
func mockImportAlias(goPkg string, seq int) string {
	parts := strings.Split(strings.Trim(goPkg, "/"), "/")
	base := goPkg
	if len(parts) >= 2 {
		base = parts[len(parts)-2] + parts[len(parts)-1]
	} else if len(parts) == 1 {
		base = parts[0]
	}
	base = naming.GoPackage(base)
	if base == "" {
		base = fmt.Sprintf("pbx%d", seq)
	}
	return base
}

// prepareServiceData prepares template data for service generation
func prepareServiceData(svc ServiceDef) map[string]any {
	var methods []map[string]any
	res := newMockTypeResolver(svc)

	for _, method := range svc.Methods {
		methods = append(methods, map[string]any{
			"Name":       method.GoName(),
			"Signature":  buildMethodSignature(method, res),
			"ReturnType": buildReturnType(method, res),
			"ReturnStub": buildReturnStub(method),
			"CallArgs":   buildCallArgs(method),
		})
	}

	// A service may declare an RPC called Register — the obvious name for a
	// sign-up endpoint. The mock's mount helper is called Register too, and
	// one type cannot carry both, so the template renames the helper when
	// they would collide. Without this the generated mock does not compile,
	// and the error names a method the author never wrote.
	hasRegisterRPC := false
	for _, m := range svc.Methods {
		if m.GoName() == "Register" {
			hasRegisterRPC = true
			break
		}
	}

	return map[string]any{
		// The mock embeds and constructs connect-go's handler types, so it
		// is named by connect-go's base for the service.
		"ServiceName":      svc.GoName(),
		"ProtoServiceName": svc.Name,
		"ServicePackage":   naming.ServicePackage(svc.Name),
		"GoPackage":        svc.GoPackage,
		"PkgName":          svc.PkgName,
		"ModulePath":       svc.ModulePath,
		"Methods":          methods,
		"HasMethods":       len(svc.Methods) > 0,
		"HasRegisterRPC":   hasRegisterRPC,
		"NeedsEmptypb":     res.needsEmp,
		"NeedsPb":          res.needsPb,
		"ForeignImports":   res.imports,
	}
}

// inputGoType / outputGoType resolve a method's input/output message to its
// qualified Go type via the resolver, replacing Method.GoInputType's
// hardcoded `pb.` assumption for the mock path.
func inputGoType(m Method, r *mockTypeResolver) string {
	return r.qualify(m.InputType, m.InputTypeFQ, m.InputProtoFile)
}

func outputGoType(m Method, r *mockTypeResolver) string {
	return r.qualify(m.OutputType, m.OutputTypeFQ, m.OutputProtoFile)
}

func buildMethodSignature(m Method, r *mockTypeResolver) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		// Bidirectional streaming
		return fmt.Sprintf("ctx context.Context, stream *connect.BidiStream[%s, %s]", inputGoType(m, r), outputGoType(m, r))
	case m.ClientStreaming:
		// Client streaming
		return fmt.Sprintf("ctx context.Context, stream *connect.ClientStream[%s]", inputGoType(m, r))
	case m.ServerStreaming:
		// Server streaming
		return fmt.Sprintf("ctx context.Context, req *connect.Request[%s], stream *connect.ServerStream[%s]", inputGoType(m, r), outputGoType(m, r))
	default:
		// Unary
		return fmt.Sprintf("ctx context.Context, req *connect.Request[%s]", inputGoType(m, r))
	}
}

func buildReturnType(m Method, r *mockTypeResolver) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "error"
	case m.ClientStreaming:
		return fmt.Sprintf("(*connect.Response[%s], error)", outputGoType(m, r))
	case m.ServerStreaming:
		return "error"
	default:
		return fmt.Sprintf("(*connect.Response[%s], error)", outputGoType(m, r))
	}
}

func buildReturnStub(m Method) string {
	// An unprogrammed mock rpc is Unimplemented — a svcerr kind, wrapped so
	// the value is a *connect.Error too: a test reading it with svcerr.Code,
	// connect.CodeOf or errors.Is(err, svcerr.ErrUnimplemented) all agree.
	// Not svcerr.ScaffoldStub: that marks forge's own unwritten handler, and
	// an unprogrammed mock is not one. The message is unchanged from the
	// connect.NewError spelling this replaced.
	errExpr := fmt.Sprintf("svcerr.Wrap(svcerr.Unimplemented(%q))", "handler for "+m.Name+" not yet implemented")
	switch {
	case m.ClientStreaming && m.ServerStreaming, m.ServerStreaming:
		return errExpr
	default:
		return "nil, " + errExpr
	}
}

func buildCallArgs(m Method) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "ctx, stream"
	case m.ClientStreaming:
		return "ctx, stream"
	case m.ServerStreaming:
		return "ctx, req, stream"
	default:
		return "ctx, req"
	}
}

// DISK-FIRST RULE: name synthesis (naming.ServicePackage / naming.GoPackage)
// is only authoritative for components that don't exist on disk yet (fresh
// scaffolds) and for stable lookup KEYS (e.g. the webhookServices map). Any
// generator referencing an EXISTING handler/worker/operator directory must
// resolve identity via ResolveComponentDir / ResolveServiceComponent
// (disk_resolver.go) instead — re-synthesizing names for existing artifacts
// is the broken-imports bug class the resolver eliminates.

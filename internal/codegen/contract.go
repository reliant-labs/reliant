// Package codegen renders Go source files for the canonical scaffolds
// forge produces: handler stubs, CRUD handlers, auth
// middleware, bootstrap wiring, mock services, and config loaders.
//
// The behavioural surface is split into three small Services so callers
// can mock just the concern they touch:
//
//   - Service     — the file-emission orchestrator (every Generate*).
//   - Parser      — descriptor / proto / go.mod parsing (no I/O writes).
//   - Inspector   — Go AST source inspection (fallible-constructor and
//     Deps-DB-field detection on user packages).
//
// Data carriers (ServiceDef, EntityDef, FieldKind, ConfigField, the
// *TemplateData and *MethodData structs, MissingHandlerResult) remain as
// plain types — they have no behavioural seam to mock.
package codegen

import (
	"github.com/reliant-labs/forge/internal/checksums"
)

// Service is the file-emission surface of the codegen package.
//
// Every method writes one or more files into the user-project working
// directory. Methods are grouped by file produced so consumers (the
// generator orchestrator, tests) can stub a focused subset.
type Service interface {
	// Service-level handler scaffolds.
	GenerateServiceStub(svc ServiceDef, targetDir string, crudMethodNames ...map[string]bool) error
	RegenerateServiceFile(svc ServiceDef, targetDir string) error
	GenerateMissingHandlerStubs(svc ServiceDef, projectDir, targetDir string, crudMethodNames map[string]bool, cs *checksums.FileChecksums) (*MissingHandlerResult, error)
	GenerateMock(svc ServiceDef, projectDir, mockDir string, cs *checksums.FileChecksums) (MockResult, error)

	// Auth is owned code now (GenerateAuthSetup scaffolds the editable
	// internal/app/auth.go, a plain package func that writes once and never
	// regenerates). Access control is handler logic (middleware.GetUser) —
	// forge generates no access-control artifact, so none appears here.

	// CRUD generation.
	GenerateCRUDHandlers(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error)
	GenerateCRUDTests(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error)

	// Config loader / cmd-server wiring.
	GenerateCmdServer(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error
	GenerateCmdServerWithFields(configFields map[string]bool, targetDir string, cs *checksums.FileChecksums) error
	GenerateConfigLoader(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error

	// pkg/app bootstrap files.
	GenerateBootstrapTesting(in BootstrapTestingGenInput) error
	GenerateMigrate(targetDir string, modulePath string, hasMigrations bool, cs *checksums.FileChecksums) error
}

// Parser reads forge_descriptor.json + go.mod to produce ServiceDefs,
// EntityDefs, ConfigMessages, and the user project's module path. No
// files are written.
type Parser interface {
	ParseServicesFromProtos(dir string, projectDir string) ([]ServiceDef, error)
	ParseEntityProtos(projectDir string) ([]EntityDef, error)
	ParseConfigProto(protoPath string) ([]ConfigMessage, error)
	ParseConfigProtosFromDir(dir string) ([]ConfigMessage, error)
	GetModulePath(dir string) (string, error)
}

// Inspector walks user-project Go source to detect constructor shape and
// dependency fields. Used by bootstrap generation to decide between
// fallible / infallible wiring.
type Inspector interface {
	DetectFallibleConstructor(dir string) (bool, error)
	DetectDepsDBField(dir string) (bool, error)
}

// Deps is the dependency set for the codegen Services. Empty today; the
// package owns its template imports directly.
type Deps struct{}

// New constructs the file-emission Service.
//
// forge:no-observe
// Pure compute: empty Deps, and the work is template rendering and file
// emission inside a CLI process. There is no request lifecycle to
// correlate spans against and no remote call to time.
func New(_ Deps) Service { return &svc{} }

// NewParser constructs the descriptor / go.mod parser surface.
func NewParser(_ Deps) Parser { return &parserSvc{} }

// NewInspector constructs the Go AST inspector surface.
func NewInspector(_ Deps) Inspector { return &inspectorSvc{} }

// svc is the file-emission impl. All Service methods are wired in
// contract_methods.go to the package's free-function implementations.
type svc struct{}

// parserSvc satisfies Parser. All methods delegate to package-level
// helpers in parser_stubs.go.
type parserSvc struct{}

// inspectorSvc satisfies Inspector. All methods delegate to package-level
// helpers in fallible.go.
type inspectorSvc struct{}

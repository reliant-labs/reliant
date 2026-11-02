package codegen

import (
	"github.com/reliant-labs/forge/internal/checksums"
)

// This file wires the Service / Parser / Inspector contract methods on
// *svc into the package's free-function implementations. Keeping the
// implementations as package-level helpers preserves intra-package
// ergonomics; the methods here are the public seam external callers
// should depend on.

// --- Service: file-emission orchestration ---

func (s *svc) GenerateServiceStub(svc ServiceDef, targetDir string, crudMethodNames ...map[string]bool) error {
	return GenerateServiceStub(svc, targetDir, crudMethodNames...)
}

func (s *svc) RegenerateServiceFile(svc ServiceDef, targetDir string) error {
	return RegenerateServiceFile(svc, targetDir)
}

func (s *svc) GenerateMissingHandlerStubs(svc ServiceDef, projectDir, targetDir string, crudMethodNames map[string]bool, cs *checksums.FileChecksums) (*MissingHandlerResult, error) {
	return GenerateMissingHandlerStubs(svc, projectDir, targetDir, crudMethodNames, cs)
}

func (s *svc) GenerateMock(svc ServiceDef, projectDir, mockDir string, cs *checksums.FileChecksums) (MockResult, error) {
	return GenerateMock(svc, projectDir, mockDir, cs)
}

func (s *svc) GenerateCRUDHandlers(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error) {
	return GenerateCRUDHandlers(svc, crudMethods, modulePath, projectDir, cs)
}

func (s *svc) GenerateCRUDTests(svc ServiceDef, crudMethods []CRUDMethod, modulePath string, projectDir string, cs *checksums.FileChecksums) (CRUDResult, error) {
	return GenerateCRUDTests(svc, crudMethods, modulePath, projectDir, cs)
}

func (s *svc) GenerateCmdServer(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error {
	return GenerateCmdServer(messages, targetDir, cs)
}

func (s *svc) GenerateCmdServerWithFields(configFields map[string]bool, targetDir string, cs *checksums.FileChecksums) error {
	return GenerateCmdServerWithFields(configFields, targetDir, cs)
}

func (s *svc) GenerateConfigLoader(messages []ConfigMessage, targetDir string, cs *checksums.FileChecksums) error {
	return GenerateConfigLoader(messages, targetDir, cs)
}

func (s *svc) GenerateBootstrapTesting(in BootstrapTestingGenInput) error {
	return GenerateBootstrapTesting(in)
}

func (s *svc) GenerateMigrate(targetDir string, modulePath string, hasMigrations bool, cs *checksums.FileChecksums) error {
	return GenerateMigrate(targetDir, modulePath, hasMigrations, cs)
}

// --- Parser: descriptor + go.mod parsing ---

func (p *parserSvc) ParseServicesFromProtos(dir string, projectDir string) ([]ServiceDef, error) {
	return ParseServicesFromProtos(dir, projectDir)
}

func (p *parserSvc) ParseEntityProtos(projectDir string) ([]EntityDef, error) {
	return ParseEntityProtos(projectDir)
}

func (p *parserSvc) ParseConfigProto(protoPath string) ([]ConfigMessage, error) {
	return ParseConfigProto(protoPath)
}

func (p *parserSvc) ParseConfigProtosFromDir(dir string) ([]ConfigMessage, error) {
	return ParseConfigProtosFromDir(dir)
}

func (p *parserSvc) GetModulePath(dir string) (string, error) {
	return GetModulePath(dir)
}

// --- Inspector: AST source detection ---

func (i *inspectorSvc) DetectFallibleConstructor(dir string) (bool, error) {
	return DetectFallibleConstructor(dir)
}

func (i *inspectorSvc) DetectDepsDBField(dir string) (bool, error) {
	return DetectDepsDBField(dir)
}

// Compile-time interface checks.
var (
	_ Service   = (*svc)(nil)
	_ Parser    = (*parserSvc)(nil)
	_ Inspector = (*inspectorSvc)(nil)
)

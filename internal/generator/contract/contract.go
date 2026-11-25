// Package contract drives the four *_gen.go files (mock, middleware,
// tracing, metrics) emitted from a single hand-written contract.go.
//
// The behavioural surface is a single Service. Data carriers
// (File, InterfaceDef, MethodDef, ParamDef) remain plain types.
//
// This package generates mocks but gets none: internal/tools/mockgen links it
// to refresh forge's own mocks WITHOUT building forge (forge#1261), so a
// mock_gen.go here would put generated code back in the very import closure
// that must stay free of it — a method added to Service would leave the tool
// that regenerates the stale mock unable to compile. Nothing mocks Service.
//
//forge:exclude-contract: links into internal/tools/mockgen, whose import closure must carry no generated contract code (forge#1261); nothing mocks Service
package contract

// Service drives contract.go → *_gen.go generation. Generate parses the
// contract, applies the four generators, and writes results next to the
// input. ParseContract is exposed for callers that want the AST without
// emitting files (e.g. docs).
type Service interface {
	Generate(contractPath string) error
	ParseContract(path string) (*File, error)
}

// Deps is the dependency set for the contract Service. Empty today.
type Deps struct{}

// New constructs a contract.Service.
//
// forge:no-observe
// Pure compute: empty Deps; parses contract.go ASTs and emits mocks in
// process. Nothing here crosses a network or storage boundary.
func New(_ Deps) Service { return &svc{} }

type svc struct{}

// Generate delegates to the package-level Generate helper.
func (s *svc) Generate(contractPath string) error { return Generate(contractPath) }

// ParseContract delegates to the package-level ParseContract helper.
func (s *svc) ParseContract(path string) (*File, error) { return ParseContract(path) }

var _ Service = (*svc)(nil)

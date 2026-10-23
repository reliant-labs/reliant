// Package contract drives the four *_gen.go files (mock, middleware,
// tracing, metrics) emitted from a single hand-written contract.go.
//
// The behavioural surface is a single Service. Data carriers
// (File, InterfaceDef, MethodDef, ParamDef) remain plain types.
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

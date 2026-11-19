package testreport

import "io"

// Service is the behavioural surface of the testreport package: the
// read → judge → report pipeline `forge ci verify-test-run` drives.
//
// The package-level Parse / Analyze / Render functions are the
// implementation; this interface is the seam a caller holds when it wants to
// substitute the pipeline (for example, to test a command's exit-code mapping
// without constructing a real `go test -json` stream). Run, Analysis, Policy
// and the finding types are data carriers, not behaviour to mock.
type Service interface {
	// Parse reads a `go test -json` stream. It returns ErrNoInput when the
	// reader held nothing at all.
	Parse(r io.Reader) (*Run, error)

	// Analyze applies a policy to a parsed run.
	Analyze(run *Run, policy Policy) Analysis

	// Render writes the human-readable report for an analysis.
	Render(w io.Writer, a Analysis)
}

// Deps is the dependency set for the testreport Service. Empty: the package
// is pure computation over the stream it is handed.
type Deps struct{}

// New constructs a testreport.Service.
//
// forge:no-observe
// Pure compute: empty Deps; reads the reader it is given and writes the
// writer it is given. No I/O boundary of its own to instrument.
func New(_ Deps) Service { return &svc{} }

type svc struct{}

// Parse delegates to the package-level parser.
func (*svc) Parse(r io.Reader) (*Run, error) { return Parse(r) }

// Analyze delegates to the package-level analyzer.
func (*svc) Analyze(run *Run, policy Policy) Analysis { return Analyze(run, policy) }

// Render delegates to the package-level renderer.
func (*svc) Render(w io.Writer, a Analysis) { Render(w, a) }

// Canonical contract.go shape: `Service` interface, `Deps` struct, and
// `New(Deps) Service` constructor. Lint must produce zero findings.
package email

// Service is the package's behavioural seam.
type Service interface {
	Send(to, body string) error
}

// Deps holds the package's dependencies.
type Deps struct{}

// New constructs an email.Service.
func New(_ Deps) Service { return &svc{} }

type svc struct{}

func (s *svc) Send(to, body string) error { return nil }

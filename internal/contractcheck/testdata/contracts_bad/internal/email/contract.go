// Non-canonical contract.go: declares `Sender` / `Config` / `NewSender`.
// Lint must produce three findings (one for each missing canonical name).
package email

// Sender is the wrong name — bootstrap codegen emits `email.Service`.
type Sender interface {
	Send(to, body string) error
}

// Config is the wrong name — bootstrap codegen emits `email.Deps`.
type Config struct{}

// NewSender is the wrong name — bootstrap codegen emits `email.New(email.Deps{})`.
func NewSender(_ Config) Sender { return &svc{} }

type svc struct{}

func (s *svc) Send(to, body string) error { return nil }

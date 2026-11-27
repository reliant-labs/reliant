package fixtures

// A test that builds scaffold-shaped source as DATA. The FORGE_SCAFFOLD
// text below is the fixture the test asserts against, not a placeholder
// anyone can "fill in" — inside a raw string literal it begins a line, so
// a line-prefix scan mistakes it for real pending work.

const fixtureHandlersGo = `package tasks

// SubmitOrder implements the SubmitOrder RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
func (s *Service) SubmitOrder() error { return nil }

// TailEvents implements the TailEvents RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
func (s *Service) TailEvents() error { return nil }
`

func fixture() string { return fixtureHandlersGo }

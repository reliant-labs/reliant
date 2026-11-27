// Package conn is the private plumbing of a public SDK under pkg/. forge
// never composes it and generates no mock for a contract.go here, so the
// rule does not apply.
package conn

type Conn struct{}

func (c *Conn) Dial() error { return nil }

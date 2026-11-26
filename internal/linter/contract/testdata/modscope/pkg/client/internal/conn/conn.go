// Package conn is private to pkg/client: not in the root internal/ tree.
package conn

type Conn struct{}

func (c *Conn) Dial() error { return nil }

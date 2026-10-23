// Package stream stands in for a third-party dependency: a module the
// project imports but does not own, shipping BOTH an interface and a
// concrete struct under the same package qualifier.
package stream

import "context"

// Publisher is an INTERFACE, the shape jetstream.JetStream and
// client.Client really have. A Deps field typed with it is already
// correct and must not be flagged.
type Publisher interface {
	Publish(ctx context.Context, subject string, data []byte) error
}

// Conn is a concrete struct, the shape *nats.Conn really has. A Deps
// field typed `*stream.Conn` is the foot-gun and must be flagged.
type Conn struct{ addr string }

// Close does nothing; the fixture is never executed.
func (c *Conn) Close() {}

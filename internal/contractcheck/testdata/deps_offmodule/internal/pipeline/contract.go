// Fixture: the dep types live in ANOTHER MODULE, reached through a
// filesystem `replace` so the lookup is hermetic and needs no network.
//
// The rule must resolve `stream.Publisher` across the module boundary and
// stay silent, while still firing on `*stream.Conn`. Before off-module
// resolution existed, BOTH fired — the false-positive class that made
// error severity impossible.
package pipeline

import (
	"context"

	"example.com/streamlib"
)

type Service interface {
	Run(ctx context.Context) error
}

type Deps struct {
	// Off-module INTERFACE — must not fire.
	Events stream.Publisher
	// Off-module concrete struct pointer — must fire.
	Raw *stream.Conn
}

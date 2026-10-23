package releasetimeline

import (
	"context"
	"time"
)

// Source supplies the ledger data for one environment. Local (the machine
// ledger) and Cloud (a control plane) implement it, so the verbs assemble a
// timeline the same way whichever backs the environment. since bounds how far
// back a remote source pages (zero: as far as its cap); the machine ledger is
// always read whole.
//
//forge:contract
type Source interface {
	Load(ctx context.Context, env string, since time.Time) (Data, error)
}

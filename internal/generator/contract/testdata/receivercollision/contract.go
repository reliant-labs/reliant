// Package receivercollision is a regression fixture for forge bug #18:
// when contract methods take a parameter named "w" (e.g. io.Writer), the
// generated middleware/tracing/metrics wrappers must not also use "w" as
// the receiver — that would shadow the receiver and fail to compile.
package receivercollision

import (
	"context"
	"io"
)

// Service has methods whose parameter names collide with the historical
// receiver name "w" used by the generated wrappers.
type Service interface {
	// PrintReport writes a report to w. The "w" parameter is the collision.
	PrintReport(ctx context.Context, w io.Writer, title string) error
	// PrintJSON writes JSON to w. Second method to ensure repeated emit is safe.
	PrintJSON(ctx context.Context, w io.Writer) (int, error)
	// NoCollision is a normal method without the colliding parameter.
	NoCollision(ctx context.Context, id string) (string, error)
}

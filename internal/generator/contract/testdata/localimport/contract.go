package localimport

import (
	"context"

	"example.com/proj/internal/widgets"
)

// Service is the incident-shaped contract (control-plane 2026-07-08):
// its mock imports span stdlib, third-party (contractkit, added by the
// generator), and PROJECT-LOCAL packages, so the generated import block
// only survives a `goimports -local <module>` pass if the emitter's
// output is already in that canonical grouping.
type Service interface {
	Get(ctx context.Context, id string) (*widgets.Widget, error)
	List(ctx context.Context) ([]widgets.Widget, error)
	Save(ctx context.Context, w widgets.Widget) error
}

package triggers

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

type deliveryCatalogKey struct{}

// WithDeliveryCatalogCache shares successful pins within a sequential fan-out
// attempt. The full pin scope includes the owner (account workflow visibility),
// project and machine. A timed-out read never poisons the next trigger's read.
func WithDeliveryCatalogCache(ctx context.Context) context.Context {
	return workflowsource.WithPinnedDeliveryCatalogs(context.WithValue(ctx, deliveryCatalogKey{}, make(map[db.CatalogPinScope]string)))
}

func pinDeliveryCatalog(ctx context.Context, pinner catalogPinner, scope db.CatalogPinScope) (string, error) {
	pins, _ := ctx.Value(deliveryCatalogKey{}).(map[db.CatalogPinScope]string)
	if pin, ok := pins[scope]; ok {
		return pin, nil
	}
	pin, err := pinner.PinProjectCatalog(ctx, scope)
	if err == nil && pins != nil {
		pins[scope] = pin
	}
	return pin, err
}

// yours: scaffolded once, never touched again — forge will not overwrite this file
// Your metrics, DECLARED: name, type, UCUM unit, description and the attribute keys each may carry.
// An undeclared attribute panics in development and is dropped elsewhere, so a stray user id never
// becomes a series per user. Add one by copying recordsCreated and exposing a function that names
// what happened. The declarations are the schema (`forge obs metrics declared`). Depth: `observability`.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/observe/metrics"
)

// recordsCreated is the example: how many records a Create rpc has stored, per
// entity and outcome. The scaffolded CRUD Create handlers call RecordCreated.
// Rename it, retire it or keep it.
var recordsCreated = metrics.Default().Int64Counter(metrics.Spec{
	Name:        "records_created",
	Unit:        "{record}",
	Description: "Records created through a Create rpc, by entity and outcome.",
	Attributes: []metrics.Attribute{
		// entity is bounded by the number of entities in the schema, which is
		// why it lists no values; outcome is a closed set.
		{Key: "entity"},
		{Key: "outcome", Values: []string{"ok", "failed"}},
	},
	Stability: metrics.Experimental,
	Since:     "v0.1.0",
})

// RecordCreated counts one Create call for entity. A nil err is an "ok".
func RecordCreated(ctx context.Context, entity string, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	recordsCreated.Add(ctx, 1, metric.WithAttributes(
		attribute.String("entity", entity),
		attribute.String("outcome", outcome),
	))
}

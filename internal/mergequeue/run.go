// Copyright (c) 2025 Reliant Labs
package mergequeue

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// run takes the picked entries through one batch: build, gate, verdict. A
// pass lands (on) or is recorded as would-land (shadow); a red batch ejects
// its entries (gate_red); an unproven one goes back to waiting and the queue
// is degraded. Reruns and bisection (M8) refine the red and unproven cases.
func (r *round) run(ctx context.Context, picked []*core.QueueEntry) error {
	b, err := r.build(ctx, picked, core.QueueBatchNormal, "")
	if errors.Is(err, errRaced) {
		return nil
	}
	if err != nil || b == nil {
		return err
	}
	if err := r.gate(ctx, b, 1, nil, true); err != nil {
		return err
	}
	switch b.result.Class {
	case GatePass:
		return r.finish(ctx, b)
	case GateUnproven:
		return r.degrade(ctx, b.batch.ID)
	}
	return nil
}

// degrade leaves the round unproven: the batch's entries wait again, nothing
// lands, and the queue is degraded until a gate is proven.
func (r *round) degrade(ctx context.Context, batchIDs ...string) error {
	for _, id := range batchIDs {
		if _, err := r.s.deps.Store.RequeueQueueEntries(ctx, id); err != nil {
			return err
		}
	}
	r.report.Degraded = true
	return r.setQueueState(ctx, core.PipelineQueueDegraded)
}

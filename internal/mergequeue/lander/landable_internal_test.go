// Copyright (c) 2025 Reliant Labs
package lander

import (
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A runc batch lands only with the scheduler's unisolated_accepted mark
// (gate.allow_unisolated); sysbox always may.
func TestLandableAcceptsRuncOnlyWhenTheRepoOptedIn(t *testing.T) {
	t.Parallel()
	batch := func(iso core.QueueIsolationBackend, verdict string) *core.QueueBatch {
		return &core.QueueBatch{ID: "b", State: core.QueueBatchPassed, Kind: core.QueueBatchNormal, IsolationBackend: iso, VerdictJSON: []byte(verdict)}
	}
	if err := landable(batch(core.QueueIsolationSysbox, `{}`)); err != nil {
		t.Fatalf("sysbox: %v", err)
	}
	if err := landable(batch(core.QueueIsolationRunc, `{"unisolated_accepted": true}`)); err != nil {
		t.Fatalf("opted-in runc: %v", err)
	}
	for _, v := range []string{`{}`, `{"unisolated_accepted": false}`, ``} {
		if err := landable(batch(core.QueueIsolationRunc, v)); !errors.Is(err, ErrNotLandable) {
			t.Fatalf("runc with %q must not land: %v", v, err)
		}
	}
}

// Copyright (c) 2025 Reliant Labs
package app

import (
	"testing"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// The merge queue is inert unless pipeline.enabled is on and a control plane
// mints its GitHub tokens: nothing is built, so the worker root runs nothing.
func TestMergeQueueIsOffUntilThePipelineIs(t *testing.T) {
	t.Parallel()
	for name, d := range map[string]MergeQueueDeps{
		"no pipeline config": {},
		"pipeline off":       {Pipeline: &configv1.PipelineConfig{Enabled: false}},
		"no control plane":   {Pipeline: &configv1.PipelineConfig{Enabled: true}},
	} {
		q, err := NewMergeQueue(d)
		if err != nil || q != nil {
			t.Errorf("%s: %v %v, want nil", name, q, err)
		}
	}
}

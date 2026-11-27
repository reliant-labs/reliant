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
		"no pipeline config": {DB: nil, Router: nil, Pipeline: nil, ControlPlane: nil},
		"pipeline off":       {DB: nil, Router: nil, Pipeline: &configv1.PipelineConfig{Enabled: false}, ControlPlane: nil},
		"no control plane":   {DB: nil, Router: nil, Pipeline: &configv1.PipelineConfig{Enabled: true}, ControlPlane: nil},
	} {
		q, err := NewMergeQueue(d)
		if err != nil || q != nil {
			t.Errorf("%s: %v %v, want nil", name, q, err)
		}
	}
}

// The pipeline.yaml refresh is inert while pipeline.enabled is off.
func TestPipelineConfigRefreshIsOffUntilThePipelineIs(t *testing.T) {
	t.Parallel()
	for name, d := range map[string]PipelineConfigRefreshDeps{
		"no pipeline config": {DB: nil, Router: nil, Pipeline: nil},
		"pipeline off":       {DB: nil, Router: nil, Pipeline: &configv1.PipelineConfig{Enabled: false}},
	} {
		r, err := NewPipelineConfigRefresh(d)
		if err != nil || r != nil {
			t.Errorf("%s: %v %v, want nil", name, r, err)
		}
	}
}

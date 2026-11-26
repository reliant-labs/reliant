package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The record_due due scan runs only while pipeline.enabled is on: off (the
// default), the worker builds no scanner and no record_due trigger fires.
func TestRecordDueScannerFollowsThePipelineSwitch(t *testing.T) {
	env := storetest.New(t)
	for _, cfg := range []*configv1.PipelineConfig{nil, {Enabled: false}} {
		s, err := NewRecordDueScanner(RecordDueScannerDeps{DB: env.Repo, Temporal: &TemporalClient{}, Pipeline: cfg})
		require.NoError(t, err)
		require.Nil(t, s)
	}
	s, err := NewRecordDueScanner(RecordDueScannerDeps{DB: env.Repo, Temporal: &TemporalClient{}, Pipeline: &configv1.PipelineConfig{Enabled: true}})
	require.NoError(t, err)
	require.NotNil(t, s)
}

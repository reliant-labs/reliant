// Copyright (c) 2025 Reliant Labs

package ghmerge_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/ghmerge"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// The worker refuses to boot when a shipped manifest names an executor no
// linked package registered (app.checkExecutors). github/pr.merge@1 names
// this package's; importing it is what registers it. The behaviour is tested
// end to end in catalog/github (github_merge_test.go).
func TestPRMergeExecutorIsRegistered(t *testing.T) {
	require.Equal(t, "github.pr_merge", ghmerge.Executor)
	gh, err := catalog.MustBuiltin().Resolve("github/pr.merge@1")
	require.NoError(t, err)
	require.NoError(t, httpaction.Executors().CheckManifests([]*reliantv1.IntegrationManifest{gh.Manifest}))
}

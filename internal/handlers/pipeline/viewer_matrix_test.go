package pipeline

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
	"github.com/reliant-labs/reliant/pkg/config"
)

// The viewer matrix for PipelineService (control-plane
// docs/design/org-sharing.md §4): every rpc that takes a chat or a project
// answers a viewer of a shared one per viewertest.Matrix.
func TestPipelineServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestPipeline(t, WithPipelineDeps(Deps{Logger: testkit.DiscardLogger(), Config: &config.Config{}, Pipeline: &configv1.PipelineConfig{Enabled: true}, Store: repo}))
	viewertest.RunService(t, f, svc, "reliant.v1.PipelineService", map[string]viewertest.Request{
		"EnrolRepository": func(f *viewertest.Fixture) proto.Message {
			return &pb.EnrolRepositoryRequest{ProjectId: f.ProjectID, Repository: "acme/widgets"}
		},
	})
}

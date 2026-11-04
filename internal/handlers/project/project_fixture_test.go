package project

import (
	"context"
	"testing"
)

// newProjectService builds the service over a repository and a daemon router,
// the two collaborators server.go hands it, through the generated harness
// (which fills Logger and Config).
func newProjectService(t *testing.T, repo Repository, router Router) *Service {
	t.Helper()
	return NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Router: router}))
}

// placesAsNamed gives a test router the placement the project handlers rank
// checkouts by (daemonplacement.Machines): every named machine places as
// itself, and default resolution is "test-daemon-id", as toolexectest.Stub
// resolves it.
type placesAsNamed struct{}

func (placesAsNamed) PlacementDaemonID(_ context.Context, _, daemonID string) (string, error) {
	if daemonID == "" {
		return "test-daemon-id", nil
	}
	return daemonID, nil
}

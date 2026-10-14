package project

import "testing"

// newProjectService builds the service over a repository and a daemon router,
// the two collaborators server.go hands it, through the generated harness
// (which fills Logger and Config).
func newProjectService(t *testing.T, repo Repository, router Router) *Service {
	t.Helper()
	return NewTestProject(t, WithProjectDeps(Deps{Repo: repo, Router: router}))
}

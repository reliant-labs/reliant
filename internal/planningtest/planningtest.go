// Package planningtest builds the planning service over a test database.
package planningtest

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/planning"
	"github.com/reliant-labs/reliant/internal/store"
)

// New is the planning service over repo's database.
func New(t testing.TB, repo *db.Repo) planning.Service {
	t.Helper()
	svc, err := planning.NewInstrumented(planning.Deps{
		Plans:        store.NewPlanExtStore(repo.ORM()),
		Tasks:        store.NewTaskExtStore(repo.ORM()),
		Dependencies: store.NewTaskDependencyExtStore(repo.ORM()),
	})
	if err != nil {
		t.Fatalf("planningtest.New: %v", err)
	}
	return svc
}

package checkout

import (
	"context"
	"log/slog"

	"example.com/app/internal/db"
)

// stripeAdapter is a stand-in for a concrete outbound-boundary struct.
// The lint rule fires on the `Charger *stripeAdapter` field below — a
// concrete pointer in Deps defeats unit-testing this package with mocks.
type stripeAdapter struct{}

func (s *stripeAdapter) Charge(_ context.Context, _ string, _ int64) (string, error) {
	return "", nil
}

// Deps fires the rule twice: Charger is a concrete same-package struct
// pointer, Store is a concrete pointer into another package.
type Deps struct {
	Logger  *slog.Logger
	Charger *stripeAdapter         // SHOULD be an interface
	Store   *db.PostgresRepository // SHOULD be an interface
}

type service struct{ deps Deps }

func New(deps Deps) Service { return &service{deps: deps} }

func (s *service) Run(ctx context.Context) error {
	_, err := s.deps.Charger.Charge(ctx, "u1", 100)
	return err
}

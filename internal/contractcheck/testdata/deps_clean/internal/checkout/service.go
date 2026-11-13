package checkout

import (
	"context"
	"log/slog"

	"github.com/reliant-labs/forge/pkg/orm"
)

// Deps holds only interfaces (plus the always-allowed Logger) and
// primitive-shaped config DATA (allow-lists, scalar limits, byte
// secrets, string-keyed primitive maps). The lint rule should let all
// of these through.
type Deps struct {
	Logger        *slog.Logger
	DB            orm.Context
	Charger       Charger
	AllowedModels []string
	MaxAttempts   []int
	Flags         []bool
	Seed          []byte
	Headers       map[string]string
	Quotas        map[string]int
	Nested        [][]byte
}

type service struct{ deps Deps }

func New(deps Deps) Service { return &service{deps: deps} }

func (s *service) Run(ctx context.Context) error {
	_, err := s.deps.Charger.Charge(ctx, "u1", 100)
	return err
}

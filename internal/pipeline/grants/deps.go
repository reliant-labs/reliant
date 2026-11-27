// Copyright (c) 2025 Reliant Labs
package grants

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Store is the grants and the records they are judged against.
// store.PipelineGrantExtStore satisfies it.
type Store interface {
	InsertPipelineCapabilityGrant(ctx context.Context, g core.PipelineCapabilityGrantNew) (*core.PipelineCapabilityGrant, bool, error)
	ListLivePipelineCapabilityGrants(ctx context.Context, triggerID string, capability core.PipelineCapability, repoID string) ([]*core.PipelineCapabilityGrant, error)
	ListPipelineCapabilityGrantsForUser(ctx context.Context, userID, triggerID string, includeRevoked bool) ([]*core.PipelineCapabilityGrant, error)
	RevokePipelineCapabilityGrant(ctx context.Context, userID, id string, at time.Time) (*core.PipelineCapabilityGrant, error)
	PipelineGrantFactsFor(ctx context.Context, triggerID, poolID string) (core.PipelineGrantFacts, error)
	PipelineDaemonFactsFor(ctx context.Context, daemonID string) (core.PipelineDaemonFacts, error)
	ListPendingCapabilityGrantCards(ctx context.Context, triggerID string) ([]*core.Approval, error)
	GetCapabilityGrantCard(ctx context.Context, entityID string) (*core.Approval, error)
}

// Cards writes the approval card that asks for a grant, and reads the
// enrolled repository a request names. *db.Repo satisfies it.
type Cards interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error
	CreateApproval(ctx context.Context, approval *core.Approval) error
	UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error
	CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error
	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
}

// Deps are the service's collaborators. Store is required; Cards only for
// RequestGrant, which refuses without it.
type Deps struct {
	Store Store
	// forge:optional-dep
	Cards Cards
	// Now is the clock; nil is time.Now.
	// forge:optional-dep
	Now func() time.Time
	// forge:optional-dep
	Logger *slog.Logger
}

// Service decides, makes, lists and revokes grants.
type Service struct {
	deps Deps
	log  *slog.Logger
}

// New returns the service over deps.
func New(deps Deps) (*Service, error) {
	if deps.Store == nil {
		return nil, errors.New("grants: Store is required")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{deps: deps, log: log.With("component", "pipeline_grants")}, nil
}

func (s *Service) now() time.Time { return s.deps.Now().UTC() }

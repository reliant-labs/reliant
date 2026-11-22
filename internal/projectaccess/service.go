// Copyright (c) 2025 Reliant Labs
package projectaccess

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

type service struct {
	store Store
}

func (s *service) GetProjectWithUserCheck(ctx context.Context, id string, userID string) (*core.Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	if userID == "" {
		return nil, fmt.Errorf("user ID cannot be empty")
	}
	// The store read filters on the owner; authz confirms the row it returns,
	// as GetChatRowWithUserCheck does.
	project, err := s.store.GetProjectWithUserCheck(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	switch denial := authz.WriteProjectDenial(ctx, userID, project); {
	case denial == nil:
		return project, nil
	case errors.Is(denial, authz.ErrDenied):
		return nil, core.ErrProjectNotFound
	default:
		return nil, svcerr.WithCause(core.ErrProjectNotFound, denial)
	}
}

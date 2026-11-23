// Copyright (c) 2025 Reliant Labs
package projectpurge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/store"
)

type service struct {
	db orm.Context
}

func (s *service) ScheduleProjectDeletion(ctx context.Context, id, by string, deleteAfter time.Time, decided core.ProjectDecision) (core.ProjectDeletion, error) {
	if id == "" {
		return core.ProjectDeletion{}, fmt.Errorf("project ID cannot be empty")
	}
	var out core.ProjectDeletion
	err := s.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		out, err = store.ScheduleProjectDeletion(ctx, s.db, id, by, deleteAfter, decided)
		return err
	})
	return out, err
}

func (s *service) RestoreProject(ctx context.Context, id string, decided core.ProjectDecision) ([]string, error) {
	if id == "" {
		return nil, fmt.Errorf("project ID cannot be empty")
	}
	var resumed []string
	err := s.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		resumed, err = store.RestoreProjectDeletion(ctx, s.db, id, decided)
		return err
	})
	return resumed, err
}

func (s *service) ClaimProjectPurgeNow(ctx context.Context, id string, decided core.ProjectDecision) error {
	if id == "" {
		return fmt.Errorf("project ID cannot be empty")
	}
	var claimed bool
	err := s.db.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		var err error
		claimed, err = store.ClaimProjectPurgeNow(ctx, s.db, id, decided, time.Now())
		return err
	})
	if err != nil {
		return err
	}
	if !claimed {
		return core.ErrProjectNotFound
	}
	return nil
}

func (s *service) PurgeClaimedProject(ctx context.Context, id, owner string) ([]string, error) {
	return s.purge(ctx, id, owner)
}

func (s *service) ListDeletedProjects(ctx context.Context, userID string, org *core.OrgScope, includePurging bool) ([]*core.Project, error) {
	if userID == "" {
		return nil, errors.New("user ID cannot be empty")
	}
	return store.ListDeletedProjects(ctx, s.db, userID, org, includePurging)
}

// purgeDue purges the project if its scheduled deletion is due and no other
// purge holds it within lease: it reports whether it purged it.
func (s *service) purgeDue(ctx context.Context, id string, now time.Time, lease time.Duration) (bool, error) {
	owner, claimed, err := store.ClaimProjectPurge(ctx, s.db, id, now, lease)
	if err != nil || !claimed {
		return false, err
	}
	if _, err := s.purge(ctx, id, owner); err != nil {
		return false, err
	}
	return true, nil
}

func (s *service) purge(ctx context.Context, id, owner string) ([]string, error) {
	triggers, err := store.ListProjectTriggerIDs(ctx, s.db, id)
	if err != nil {
		return nil, err
	}
	if err := store.DeleteUserProject(ctx, s.db, id, owner); err != nil {
		return nil, err
	}
	return triggers, nil
}

func (s *service) PurgeDueProjects(ctx context.Context, now time.Time, lease time.Duration, limit int) (Result, error) {
	var res Result
	ids, err := store.ListDueProjectPurges(ctx, s.db, now, lease, limit)
	if err != nil {
		return res, err
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		purged, err := s.purgeDue(ctx, id, now, lease)
		switch {
		case err != nil:
			res.Failed++
			logging.WarnContext(ctx, "project purge: could not purge a project scheduled for deletion; a later pass resumes it",
				"project_id", id, "error", err)
		case purged:
			res.Purged++
			logging.InfoContext(ctx, "project purge: purged a project whose restore window ended", "project_id", id)
		}
	}
	n, err := store.SweepOrphanedProjectRows(ctx, s.db, limit)
	res.OrphansSwept = n
	if err != nil {
		logging.WarnContext(ctx, "project purge: could not sweep rows of deleted projects", "error", err)
	}
	return res, nil
}

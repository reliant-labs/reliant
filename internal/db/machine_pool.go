// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
)

// MachinePool is a named set of the owner's machines (core.MachinePool).
type MachinePool = core.MachinePool

// MachinePoolMemberRow is one pool member as placement reads it.
type MachinePoolMemberRow = core.MachinePoolMemberRow

// GetMachinePoolForUser reads one of userID's pools by id.
func (r *Repo) GetMachinePoolForUser(ctx context.Context, userID, id string) (*MachinePool, error) {
	return store.GetMachinePoolForUser(ctx, r.ORM(), userID, id)
}

// GetMachinePoolByName reads userID's pool of that name.
func (r *Repo) GetMachinePoolByName(ctx context.Context, userID, name string) (*MachinePool, error) {
	return store.GetMachinePoolByName(ctx, r.ORM(), userID, name)
}

// ListMachinePoolsForUser lists userID's pools by name.
func (r *Repo) ListMachinePoolsForUser(ctx context.Context, userID string) ([]*MachinePool, error) {
	return store.ListMachinePoolsForUser(ctx, r.ORM(), userID)
}

// PutMachinePool creates or replaces the owner's pool named p.Name.
func (r *Repo) PutMachinePool(ctx context.Context, p *MachinePool) (*MachinePool, error) {
	return store.PutMachinePool(ctx, r.ORM(), p, uuid.NewString(), time.Now().UTC())
}

// DeleteMachinePoolByName deletes userID's pool of that name.
func (r *Repo) DeleteMachinePoolByName(ctx context.Context, userID, name string) error {
	return store.DeleteMachinePoolByName(ctx, r.ORM(), userID, name)
}

// ListMachinePoolMembers reads a pool's members for projectID.
func (r *Repo) ListMachinePoolMembers(ctx context.Context, pool *MachinePool, projectID string) ([]MachinePoolMemberRow, error) {
	return store.ListMachinePoolMembers(ctx, r.ORM(), pool, projectID)
}

// LockMachinePool row-locks a pool; ctx must carry a transaction.
func (r *Repo) LockMachinePool(ctx context.Context, id string) error {
	return store.LockMachinePool(ctx, r.ORM(), id)
}

// CountMachinePoolMemberLiveRuns counts the live runs a pool placed on a member.
func (r *Repo) CountMachinePoolMemberLiveRuns(ctx context.Context, poolID, daemonID string) (int, error) {
	return store.CountMachinePoolMemberLiveRuns(ctx, r.ORM(), poolID, daemonID)
}

// SetDaemonLabels replaces one of userID's machines' durable labels; false
// when there is no such machine.
func (r *Repo) SetDaemonLabels(ctx context.Context, userID, daemonID string, labels map[string]string) (bool, error) {
	return store.SetDaemonLabels(ctx, r.ORM(), userID, daemonID, labels)
}

// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Store is what the Placer reads. *db.Repo implements it.
type Store interface {
	GetMachinePoolForUser(ctx context.Context, userID, id string) (*core.MachinePool, error)
	ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error)
	// LockMachinePool and CountMachinePoolMemberLiveRuns run inside the
	// launch transaction (SlotGuard).
	LockMachinePool(ctx context.Context, id string) error
	CountMachinePoolMemberLiveRuns(ctx context.Context, poolID, daemonID string) (int, error)
}

// Placer places runs on a pool's members.
type Placer struct {
	store Store
}

// NewPlacer builds a Placer.
func NewPlacer(store Store) *Placer {
	return &Placer{store: store}
}

// Placement is one Place answer: the pool, its members as read, and the
// decision over them.
type Placement struct {
	Pool     Pool
	Members  []Member
	Decision Decision
}

// Place reads poolID (userID's) and its members for projectID and decides.
// core.ErrMachinePoolNotFound when the pool is not the user's.
func (p *Placer) Place(ctx context.Context, userID, poolID, projectID string) (Placement, error) {
	pool, members, err := p.Read(ctx, userID, poolID, projectID)
	if err != nil {
		return Placement{}, err
	}
	return Placement{Pool: pool, Members: members, Decision: Decide(pool, members)}, nil
}

// Read loads a pool and its members as placement sees them.
func (p *Placer) Read(ctx context.Context, userID, poolID, projectID string) (Pool, []Member, error) {
	row, err := p.store.GetMachinePoolForUser(ctx, userID, poolID)
	if err != nil {
		return Pool{}, nil, err
	}
	rows, err := p.store.ListMachinePoolMembers(ctx, row, projectID)
	if err != nil {
		return Pool{}, nil, err
	}
	members := make([]Member, 0, len(rows))
	for _, r := range rows {
		m, err := memberFromRow(r)
		if err != nil {
			return Pool{}, nil, err
		}
		members = append(members, m)
	}
	return PoolFromCore(row), members, nil
}

// Capacity is how many runs a per-member concurrency of perMember allows on
// poolID for projectID right now (PerMemberCapacity over its members).
func (p *Placer) Capacity(ctx context.Context, userID, poolID, projectID string, perMember uint32) (uint32, error) {
	_, members, err := p.Read(ctx, userID, poolID, projectID)
	if err != nil {
		return 0, err
	}
	return PerMemberCapacity(members, perMember), nil
}

// PerMemberCapacity is perMember slots for each member that can take work
// (Eligible), plus the slots still held by live runs on a member that cannot
// (it died, failed, drifted from the pool's catalog): those runs hold
// trigger-capacity slots until they end, and charging them to the healthy
// members would starve the pool exactly when it has lost a machine. That
// allowance is at most perMember per member, the most of this workflow's
// runs it could have held. A pool with no eligible member allows only what
// is already running, so new runs wait rather than launch onto nothing.
func PerMemberCapacity(members []Member, perMember uint32) uint32 {
	eligible := map[string]bool{}
	for _, m := range Eligible(members) {
		eligible[m.DaemonID] = true
	}
	total := uint32(0)
	for _, m := range members {
		if eligible[m.DaemonID] {
			total += perMember
			continue
		}
		total += min(uint32(m.LiveRuns), perMember)
	}
	return total
}

// SlotGuard is the launch-transaction check that member still has a free
// slot in pool: it row-locks the pool, so concurrent launches onto it take
// slots one at a time, then counts the member's live runs. It returns a
// decline reason, or "" to proceed. A pool with no per-member bound only
// takes the lock.
func (p *Placer) SlotGuard(pool Pool, member Member) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if err := p.store.LockMachinePool(ctx, pool.ID); err != nil {
			return "", fmt.Errorf("lock machine pool %s: %w", pool.Name, err)
		}
		if pool.MaxRunsPerMember <= 0 {
			return "", nil
		}
		n, err := p.store.CountMachinePoolMemberLiveRuns(ctx, pool.ID, member.DaemonID)
		if err != nil {
			return "", err
		}
		if n >= pool.MaxRunsPerMember {
			return MemberBusy, nil
		}
		return "", nil
	}
}

// MemberBusy is SlotGuard's decline: a concurrent launch took the chosen
// member's last slot between the choice and the transaction. The caller
// places again.
const MemberBusy = "pool member busy"

// PoolFromCore converts a stored pool.
func PoolFromCore(row *core.MachinePool) Pool {
	return Pool{
		ID: row.ID, UserID: row.UserID, Name: row.Name, Selector: row.Selector,
		MaxRunsPerMember: row.MaxRunsPerMember, MinMembers: row.MinMembers,
		MaxMembers: row.MaxMembers, MemberSize: row.MemberSize,
	}
}

func memberFromRow(r core.MachinePoolMemberRow) (Member, error) {
	digest, err := CatalogDigest(r.WorkflowsJSON, r.PresetsJSON)
	if err != nil {
		return Member{}, fmt.Errorf("machine %s: %w", r.DaemonID, err)
	}
	var pushed time.Time
	if r.CatalogPushedAt != nil {
		pushed = *r.CatalogPushedAt
	}
	return Member{
		DaemonID: r.DaemonID, Name: r.Name, Size: r.Size, Phase: Phase(r.Phase),
		LiveRuns: r.LiveRuns, Installed: r.Installed,
		CatalogDigest: digest, CatalogPushedAt: pushed,
	}, nil
}

// Eligible is the members that can take work at all (load aside): the
// members Decide would consider before looking at slots and phase.
func Eligible(members []Member) []Member {
	expected := ExpectedCatalog(members)
	var out []Member
	for _, m := range members {
		if ineligible(m, expected) == "" && m.Phase != PhaseDisconnected {
			out = append(out, m)
		}
	}
	return out
}

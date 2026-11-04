// Copyright (c) 2025 Reliant Labs
package machinepool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// MemberReader reads a pool and its members. *db.Repo implements it.
type MemberReader interface {
	GetMachinePoolForUser(ctx context.Context, userID, id string) (*core.MachinePool, error)
	ListMachinePoolMembers(ctx context.Context, pool *core.MachinePool, projectID string) ([]core.MachinePoolMemberRow, error)
}

// Store is what the Placer reads. *db.Repo implements it.
type Store interface {
	MemberReader
	// LockMachinePool and CountMachinePoolMemberLiveRuns run inside the
	// launch transaction (SlotGuard).
	LockMachinePool(ctx context.Context, id string) error
	CountMachinePoolMemberLiveRuns(ctx context.Context, poolID, daemonID string) (int, error)
	// CountTriggerCapacityHeldByMember is, per member of poolID, how many
	// slots of one trigger-capacity scope (owner, project, workflow ref) its
	// runs hold: the admitted, unreleased executions whose chat the pool
	// placed there.
	CountTriggerCapacityHeldByMember(ctx context.Context, poolID, userID, projectID, workflowRef string) (map[string]int, error)
}

// ErrNoAgreedCatalog is a pool none of whose usable members has synced the
// project: there is no catalog its runs could be read from or placed on.
var ErrNoAgreedCatalog = errors.New("no member of the pool has synced the project")

// AgreedCatalog is the pool's agreed (expected) catalog for projectID and a
// usable member holding it, whose machine catalog every read about the
// pool's runs (declaration, filter, inputs, concurrency) must resolve
// against, so they agree with where Decide places the run. Members on the
// agreed catalog hold identical definitions, so any of them answers the
// same; the first by name is returned. ErrNoAgreedCatalog when none has
// synced the project.
func AgreedCatalog(ctx context.Context, r MemberReader, userID, poolID, projectID string) (daemonID, digest string, err error) {
	members, err := readMembers(ctx, r, userID, poolID, projectID)
	if err != nil {
		return "", "", err
	}
	digest = ExpectedCatalog(members)
	if digest == "" {
		return "", "", ErrNoAgreedCatalog
	}
	for _, m := range sortedByName(members) {
		if ineligible(m, digest) == "" {
			return m.DaemonID, digest, nil
		}
	}
	return "", "", ErrNoAgreedCatalog
}

// Catalogs answers AgreedCatalog over a bare MemberReader, for a caller that
// reads pools without placing on them (the trigger handlers, intake).
type Catalogs struct{ R MemberReader }

// AgreedCatalog is machinepool.AgreedCatalog over c.R.
func (c Catalogs) AgreedCatalog(ctx context.Context, userID, poolID, projectID string) (string, string, error) {
	return AgreedCatalog(ctx, c.R, userID, poolID, projectID)
}

// AgreedCatalog is machinepool.AgreedCatalog over the Placer's store.
func (p *Placer) AgreedCatalog(ctx context.Context, userID, poolID, projectID string) (string, string, error) {
	return AgreedCatalog(ctx, p.store, userID, poolID, projectID)
}

// Placer places runs on a pool's members.
type Placer struct {
	store Store
	waker Waker
}

// Waker wakes one of the owner's suspended machines. The worker's daemon
// router implements it; success means the wake is under way.
type Waker interface {
	WakeMember(ctx context.Context, userID, daemonID string) error
}

// NewPlacer builds a Placer.
func NewPlacer(store Store) *Placer {
	return &Placer{store: store}
}

// WithWaker lets the Placer act on a Wake decision.
func (p *Placer) WithWaker(w Waker) *Placer {
	p.waker = w
	return p
}

// errNoWaker is a Wake decision in a process with no waker wired: a
// configuration bug, reported rather than waited on silently.
var errNoWaker = errors.New("waking machine pool members is not configured on this worker")

// Wake wakes member daemonID of a pool for userID (a Wake decision). The run
// is not placed: it waits, and a later Place puts it on the member once the
// member is up and its checkout verified.
func (p *Placer) Wake(ctx context.Context, userID, daemonID string) error {
	if p.waker == nil {
		return errNoWaker
	}
	return p.waker.WakeMember(ctx, userID, daemonID)
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
	members, err := membersOf(ctx, p.store, row, projectID)
	if err != nil {
		return Pool{}, nil, err
	}
	return PoolFromCore(row), members, nil
}

func readMembers(ctx context.Context, r MemberReader, userID, poolID, projectID string) ([]Member, error) {
	row, err := r.GetMachinePoolForUser(ctx, userID, poolID)
	if err != nil {
		return nil, err
	}
	return membersOf(ctx, r, row, projectID)
}

func membersOf(ctx context.Context, r MemberReader, row *core.MachinePool, projectID string) ([]Member, error) {
	rows, err := r.ListMachinePoolMembers(ctx, row, projectID)
	if err != nil {
		return nil, err
	}
	members := make([]Member, 0, len(rows))
	for _, mr := range rows {
		m, err := memberFromRow(mr)
		if err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, nil
}

// Capacity is how many runs of one workflow (its trigger-capacity scope:
// owner, project, workflowRef) a per-member concurrency of perMember allows
// on poolID right now (PerMemberCapacity).
func (p *Placer) Capacity(ctx context.Context, userID, poolID, projectID, workflowRef string, perMember uint32) (uint32, error) {
	_, members, err := p.Read(ctx, userID, poolID, projectID)
	if err != nil {
		return 0, err
	}
	held, err := p.store.CountTriggerCapacityHeldByMember(ctx, poolID, userID, projectID, workflowRef)
	if err != nil {
		return 0, err
	}
	return PerMemberCapacity(members, perMember, held), nil
}

// PerMemberCapacity is perMember slots for each member that can take work
// (Eligible), plus, for a member that cannot (it died, failed, drifted from
// the pool's catalog), the slots THIS workflow's runs still hold there
// (held, by member id; at most perMember). Those runs keep their
// trigger-capacity slots until they end, and charging them to the healthy
// members would starve the pool exactly when it has lost a machine. Other
// workflows' runs on that member hold none of this workflow's slots, so they
// add nothing. A pool with no eligible member allows only what this workflow
// already runs, so new runs wait rather than launch onto nothing.
func PerMemberCapacity(members []Member, perMember uint32, held map[string]int) uint32 {
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
		total += min(uint32(max(held[m.DaemonID], 0)), perMember)
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
		LiveRuns: r.LiveRuns, Installed: r.Installed, Verified: r.Verified,
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

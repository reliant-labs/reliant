package approval

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// MemoryStore is an in-memory [Approvals] with the same semantics as [Store],
// for unit tests of code that proposes and decides. It is safe for
// concurrent use. A follow-up must not call back into the same MemoryStore:
// Decide and Redrive hold its lock while the follow-up runs, which is what
// makes the transition and the follow-up one atomic step.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
type MemoryStore struct {
	mu    sync.Mutex
	cfg   config
	items map[string]*Item
	order []string // insertion order
}

var _ Approvals = (*MemoryStore)(nil)

// NewMemoryStore returns an empty MemoryStore. [WithNow] applies; [WithSchema]
// is ignored.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func NewMemoryStore(opts ...StoreOption) *MemoryStore {
	return &MemoryStore{cfg: newConfig(opts), items: map[string]*Item{}}
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func clone(it *Item) Item {
	c := *it
	c.Payload = slices.Clone(it.Payload)
	if it.DecidedAt != nil {
		t := *it.DecidedAt
		c.DecidedAt = &t
	}
	return c
}

func matches(it *Item, f Filter) bool {
	if len(f.IDs) > 0 && !slices.Contains(f.IDs, it.ID) {
		return false
	}
	for _, c := range []struct{ want, got string }{
		{f.Scope, it.Scope}, {f.Kind, it.Kind}, {f.SubjectRef, it.SubjectRef}, {f.RunID, it.RunID},
	} {
		if c.want != "" && c.want != c.got {
			return false
		}
	}
	return len(f.States) == 0 || slices.Contains(f.States, it.State)
}

// Propose implements [Approvals].
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) Propose(_ context.Context, it Item) (Item, error) {
	it, err := validateProposal(it)
	if err != nil {
		return Item{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.items {
		if e.Scope == it.Scope && e.Kind == it.Kind && e.SubjectRef == it.SubjectRef {
			return clone(e), nil
		}
	}
	now := m.cfg.now()
	n := Item{
		ID: newUUID(), Scope: it.Scope, Kind: it.Kind, SubjectRef: it.SubjectRef, RunID: it.RunID,
		Payload: slices.Clone(it.Payload), Summary: it.Summary, State: StatePending, ExpiresAt: it.ExpiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	m.items[n.ID] = &n
	m.order = append(m.order, n.ID)
	return clone(&n), nil
}

// Get implements [Approvals].
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) Get(_ context.Context, id string) (Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[id]
	if !ok {
		return Item{}, svcerr.NotFound("approval")
	}
	return clone(it), nil
}

// List implements [Approvals]. Paging is by offset-free token over creation
// order, newest first.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) List(_ context.Context, f Filter) (Page, error) {
	var after int
	if f.PageToken != "" {
		_, id, err := decodePageToken(f.PageToken)
		if err != nil {
			return Page{}, err
		}
		after = -1
		m.mu.Lock()
		for i, oid := range m.order {
			if oid == id {
				after = i
			}
		}
		m.mu.Unlock()
		if after < 0 {
			return Page{}, ErrBadPageToken
		}
	} else {
		m.mu.Lock()
		after = len(m.order)
		m.mu.Unlock()
	}
	size := pageSize(f.PageSize)
	m.mu.Lock()
	defer m.mu.Unlock()
	var page Page
	for i := after - 1; i >= 0; i-- {
		it := m.items[m.order[i]]
		if !matches(it, f) {
			continue
		}
		if len(page.Items) == size {
			last := page.Items[size-1]
			page.NextPageToken = encodePageToken(last.CreatedAt, last.ID)
			break
		}
		page.Items = append(page.Items, clone(it))
	}
	return page, nil
}

// ExpireDue implements [Approvals].
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) ExpireDue(_ context.Context, scope string) ([]Item, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.cfg.now()
	var out []Item
	for _, id := range m.order {
		it := m.items[id]
		if it.State != StatePending || it.ExpiresAt.After(now) || (scope != "" && it.Scope != scope) {
			continue
		}
		t := now
		it.State, it.DecidedAt, it.UpdatedAt = StateExpired, &t, now
		out = append(out, clone(it))
	}
	return out, nil
}

// Cancel implements [Approvals].
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) Cancel(_ context.Context, f Filter, note string) (int, error) {
	if err := validateCancel(f); err != nil {
		return 0, err
	}
	f.States = nil
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.cfg.now()
	n := 0
	for _, it := range m.items {
		if it.State == StatePending && matches(it, f) {
			t := now
			it.State, it.Note, it.DecidedAt, it.UpdatedAt = StateCancelled, note, &t, now
			n++
		}
	}
	return n, nil
}

// Decide implements [Approvals]. A follow-up error leaves the item exactly
// as it was.
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) Decide(ctx context.Context, d Decision, fn FollowUp) (Item, error) {
	if err := validateDecision(d); err != nil {
		return Item{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[d.ID]
	if !ok {
		return Item{}, svcerr.NotFound("approval")
	}
	now := m.cfg.now()
	if it.State == StatePending && !it.ExpiresAt.After(now) {
		t := now
		it.State, it.DecidedBy, it.DecidedAt, it.Note, it.UpdatedAt = StateExpired, d.By, &t, d.Note, now
		return clone(it), nil
	}
	if it.State == StateExpired {
		if it.DecidedBy == "" {
			it.DecidedBy, it.Note, it.UpdatedAt = d.By, d.Note, now
		}
		return clone(it), nil
	}
	if it.State != StatePending {
		return Item{}, svcerr.FailedPrecondition(decidedMessage(it.State))
	}

	next := *it
	next.DecidedBy, next.Note, next.UpdatedAt = d.By, d.Note, now
	t := now
	next.DecidedAt = &t
	next.State = StateRejected
	if d.Approve {
		next.State = StateApproved
	}
	next.HandedOff = !d.Approve || fn == nil
	if d.Approve && fn != nil {
		if err := runMemoryFollowUp(ctx, &next, d, fn); err != nil {
			return Item{}, err
		}
	}
	*it = next
	return clone(it), nil
}

// runMemoryFollowUp applies fn's result to it (already approved).
func runMemoryFollowUp(ctx context.Context, it *Item, d Decision, fn FollowUp) error {
	outcome, err := fn(ctx, clone(it), d)
	var rej *Rejection
	var hand *HandoffError
	switch {
	case err == nil:
		it.HandedOff, it.Outcome = true, outcome
	case errors.As(err, &rej):
		it.State, it.HandedOff, it.Outcome = StateRejected, true, rej.Reason
	case errors.As(err, &hand):
		it.Outcome = outcome
	default:
		return err
	}
	return nil
}

// Redrive implements [Approvals].
//
// Deprecated: forge/pkg/approval is out of scope for forge and frozen; it will
// be removed in forge v2. Copy it into your project if you depend on it.
func (m *MemoryStore) Redrive(ctx context.Context, f Filter, fn FollowUp) ([]Item, error) {
	if fn == nil {
		return nil, invalid("redrive needs a follow-up")
	}
	f.States = nil
	m.mu.Lock()
	defer m.mu.Unlock()
	var cands []*Item
	for _, it := range m.items {
		if it.State == StateApproved && !it.HandedOff && matches(it, f) {
			cands = append(cands, it)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if !cands[i].UpdatedAt.Equal(cands[j].UpdatedAt) {
			return cands[i].UpdatedAt.Before(cands[j].UpdatedAt)
		}
		return cands[i].ID < cands[j].ID
	})
	if n := pageSize(f.PageSize); len(cands) > n {
		cands = cands[:n]
	}
	var done []Item
	var errs []error
	for _, it := range cands {
		next := *it
		if err := runMemoryFollowUp(ctx, &next, Decision{ID: it.ID, Approve: true, By: it.DecidedBy, Note: it.Note}, fn); err != nil {
			errs = append(errs, fmt.Errorf("approval %s: %w", it.ID, err))
			continue
		}
		next.UpdatedAt = m.cfg.now()
		*it = next
		if it.HandedOff {
			done = append(done, clone(it))
		}
	}
	return done, errors.Join(errs...)
}

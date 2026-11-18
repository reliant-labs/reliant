// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore is a StateStore that keeps everything in process: for tests and
// for a single-process deployment that accepts losing state on restart.
type MemoryStore struct {
	mu        sync.Mutex
	instances map[InstanceRef]Instance
	events    []Event
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{instances: map[InstanceRef]Instance{}}
}

var _ StateStore = (*MemoryStore)(nil)

// LoadOwner implements StateStore.
func (m *MemoryStore) LoadOwner(_ context.Context, owner string) ([]Instance, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Instance
	for _, in := range m.instances {
		if in.Owner == owner {
			out = append(out, in)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
}

// Owners implements StateStore.
func (m *MemoryStore) Owners(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := map[string]bool{}
	for _, in := range m.instances {
		set[in.Owner] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// Apply implements StateStore.
func (m *MemoryStore) Apply(_ context.Context, ch Changes) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range ch.Upsert {
		m.instances[InstanceRef{RuleKey: in.RuleKey, Fingerprint: in.Fingerprint}] = in
	}
	for _, r := range ch.Delete {
		delete(m.instances, r)
	}
	m.events = append(m.events, ch.Events...)
	return nil
}

// Events implements StateStore. History is kept in memory without bound; a
// long-running process should use a persistent store.
func (m *MemoryStore) Events(_ context.Context, q EventQuery) ([]Event, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for i := len(m.events) - 1; i >= 0 && len(out) < limit; i-- {
		e := m.events[i]
		if e.Owner == q.Owner && (q.Env == "" || e.Env == q.Env) && !e.At.Before(q.Since) {
			out = append(out, e)
		}
	}
	return out, nil
}

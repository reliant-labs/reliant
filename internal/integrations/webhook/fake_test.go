// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// fakeStore is the receiver's view of the database.
type fakeStore struct {
	mu       sync.Mutex
	triggers map[string]*core.Trigger
	creds    map[string]*core.TriggerWebhookCredentials
	routes   map[string][]*core.IntegrationTriggerRoute

	access        []accessGrant
	accessQueries int
	revocations   []core.IntegrationAccessRevocation
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		triggers: map[string]*core.Trigger{},
		creds:    map[string]*core.TriggerWebhookCredentials{},
		routes:   map[string][]*core.IntegrationTriggerRoute{},
	}
}

func (s *fakeStore) GetTrigger(_ context.Context, id string) (*core.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.triggers[id]
	if !ok {
		return nil, core.ErrTriggerNotFound
	}
	copied := *t
	return &copied, nil
}

func (s *fakeStore) GetTriggerWebhookCredentials(_ context.Context, id string) (*core.TriggerWebhookCredentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[id]
	if !ok {
		return nil, core.ErrTriggerNotFound
	}
	return c, nil
}

func (s *fakeStore) ListIntegrationTriggers(_ context.Context, integration string) ([]*core.IntegrationTriggerRoute, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.routes[integration], nil
}

// fakeIntake records what reached the trigger layer, deduping like the real
// trigger_events constraint.
type fakeIntake struct {
	mu       sync.Mutex
	accepted []acceptedEvent
	seen     map[string]bool
}

type acceptedEvent struct {
	TriggerID string
	Event     triggers.InboundEvent
}

func (f *fakeIntake) Accept(_ context.Context, trigger *core.Trigger, ev triggers.InboundEvent, _ triggers.AcceptOptions) (*triggers.AcceptResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	key := string(ev.Kind) + "|" + ev.DedupeKey
	if f.seen[key] {
		return &triggers.AcceptResult{EventID: key, Outcome: core.TriggerEventPending, Duplicate: true}, nil
	}
	f.seen[key] = true
	f.accepted = append(f.accepted, acceptedEvent{TriggerID: trigger.ID, Event: ev})
	return &triggers.AcceptResult{EventID: key, Outcome: core.TriggerEventPending}, nil
}

func (f *fakeIntake) triggerIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, a := range f.accepted {
		ids = append(ids, a.TriggerID)
	}
	sort.Strings(ids)
	return ids
}

func (f *fakeIntake) all() []acceptedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]acceptedEvent(nil), f.accepted...)
}

// fakeOpener opens what fakeSeal sealed, checking it is the owner's.
type fakeOpener struct{}

func fakeSeal(userID, triggerID, secret string) []byte {
	return []byte(userID + "|" + string(triggers.WebhookSecretAAD(triggerID)) + "|" + secret)
}

// accessGrant is one (user, account, resource) the fake store treats as a
// fresh grant.
type accessGrant struct{ user, account, resource, subject string }

// grantAccess records that user can see resource in account.
func (s *fakeStore) grantAccess(user, account, resource, subject string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.access = append(s.access, accessGrant{user, account, resource, subject})
}

func (s *fakeStore) ListAccessRoutedTriggers(_ context.Context, integration, account, resource string, _ time.Time) ([]*core.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessQueries++
	var out []*core.Trigger
	for _, r := range s.routes[integration] {
		if r.Trigger == nil || !r.Trigger.Enabled {
			continue
		}
		for _, g := range s.access {
			if g.user == r.Trigger.UserID && g.account == account && g.resource == resource {
				out = append(out, r.Trigger)
				break
			}
		}
	}
	return out, nil
}

func (s *fakeStore) RevokeIntegrationAccess(_ context.Context, _ string, rev core.IntegrationAccessRevocation) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revocations = append(s.revocations, rev)
	kept := s.access[:0]
	var n int64
	for _, g := range s.access {
		match := (rev.AccountKey == "" || g.account == rev.AccountKey) &&
			(rev.ResourceKey == "" || g.resource == rev.ResourceKey) &&
			(rev.SubjectID == "" || g.subject == rev.SubjectID)
		if match {
			n++
			continue
		}
		kept = append(kept, g)
	}
	s.access = kept
	return n, nil
}

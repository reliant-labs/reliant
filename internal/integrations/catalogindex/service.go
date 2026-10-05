// Copyright (c) 2025 Reliant Labs

package catalogindex

import (
	"context"
	"errors"
	"fmt"
	"sync"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

// ErrNotFound: no entry has the ref.
var ErrNotFound = errors.New("catalog entry not found")

// ConnectionLister is the slice of the connection store search needs: the
// caller's own connections. Declared here, where it is consumed.
type ConnectionLister interface {
	ListConnections(ctx context.Context, userID, integrationID string) ([]*core.Connection, error)
}

// DelegatedAvailable reports whether this deployment serves a delegated
// broker, so an integration it backs (GitHub through the control plane) is
// usable with no connection row. It has the shape of
// connections.DelegatedAvailable, which callers pass straight through.
type DelegatedAvailable func(brokerID string) (ok bool, reason string)

// Service answers catalog searches for a caller: the shared index plus the
// caller's connection state.
type Service struct {
	index       *Index
	connections ConnectionLister
	delegated   DelegatedAvailable
}

// NewService wires the service. A nil connections lister means no caller has
// a connection (only entries that need none, or that a delegated authority
// serves, are connected); a nil delegated func means none is served.
func NewService(index *Index, connections ConnectionLister, delegated DelegatedAvailable) *Service {
	return &Service{index: index, connections: connections, delegated: delegated}
}

var (
	builtinOnce  sync.Once
	builtinIndex *Index
	builtinErr   error
)

// Builtin is the index over the embedded catalog, built on first use and
// shared by every caller in the process.
func Builtin() (*Index, error) {
	builtinOnce.Do(func() {
		cat, err := catalog.Builtin()
		if err != nil {
			builtinErr = err
			return
		}
		builtinIndex, builtinErr = Build(cat.Manifests())
	})
	return builtinIndex, builtinErr
}

// MustBuiltin is Builtin for wiring; a broken embedded catalog is a build
// defect that catalog's load-all test catches first.
func MustBuiltin() *Index {
	idx, err := Builtin()
	if err != nil {
		panic(fmt.Sprintf("integration catalog index: %v", err))
	}
	return idx
}

// Index is the shared index the service searches.
func (s *Service) Index() *Index { return s.index }

// Search runs q for userID, filling in which integrations the user can use.
func (s *Service) Search(ctx context.Context, userID string, q Query) (*Result, error) {
	usable, err := s.Usable(ctx, userID)
	if err != nil {
		return nil, err
	}
	q.Usable = usable
	return s.index.Search(q)
}

// Get returns one entry, and whether the user can use it now.
func (s *Service) Get(ctx context.Context, userID, ref string) (*Entry, bool, error) {
	e, ok := s.index.Get(ref)
	if !ok {
		return nil, false, ErrNotFound
	}
	if !e.ConnectionRequired {
		return e, true, nil
	}
	usable, err := s.Usable(ctx, userID)
	if err != nil {
		return nil, false, err
	}
	return e, e.Connected(usable), nil
}

// Usable is the set of integration ids the user can use now:
//
//   - an integration the user has an ACTIVE connection to, of a kind the
//     integration still declares (a connection needing re-auth is not usable:
//     a run would fail on it); and
//   - an integration with a delegated method whose broker this deployment
//     serves, which needs no connection row at all.
//
// It is one indexed query per search (connections_user), and the catalog
// lookups are in memory.
func (s *Service) Usable(ctx context.Context, userID string) (map[string]bool, error) {
	usable := map[string]bool{}
	if s.delegated != nil {
		for id, in := range s.index.integrations {
			for _, a := range in.connection.GetAuth() {
				if d := a.GetDelegated(); d != nil {
					if ok, _ := s.delegated(d.GetBroker()); ok {
						usable[id] = true
					}
				}
			}
		}
	}
	if s.connections == nil || userID == "" {
		return usable, nil
	}
	conns, err := s.connections.ListConnections(ctx, userID, "")
	if err != nil {
		return nil, fmt.Errorf("listing connections: %w", err)
	}
	for _, c := range conns {
		if c.Status != core.ConnectionStatusActive {
			continue
		}
		// A connection counts only while the integration still offers its
		// kind, so one left over from a method the manifest has since dropped
		// does not.
		if in, ok := s.index.integrations[c.IntegrationID]; ok && in.authKinds[c.AuthKind] {
			usable[c.IntegrationID] = true
		}
	}
	return usable, nil
}

// Schemas returns an action's params and output JSON Schemas; for a trigger
// both are nil (its payload schema arrives with stream B's TriggerSpec).
func (e *Entry) Schemas() (params, output map[string]any) {
	if e.Kind != KindAction {
		return nil, nil
	}
	return manifest.ActionSchemas(e.Action)
}

// ToolName is the agent tool an action is exposed as, or "" when it is not.
func (e *Entry) ToolName() string {
	if e.Kind != KindAction || !e.Action.GetTool().GetExpose() {
		return ""
	}
	return manifest.ToolName(e.Manifest, e.Action)
}

// ConnectionSpec is the integration's connection declaration.
func (e *Entry) ConnectionSpec() *reliantv1.ConnectionSpec { return e.Manifest.GetConnection() }

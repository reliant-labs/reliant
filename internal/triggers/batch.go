// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// Batch is shared by the Accepts of one arrival that reaches many triggers
// (one webhook delivery fans out to every activation listening for it), so
// the catalog reads they have in common are made once.
//
// Every Accept pins its trigger's workflow catalog (db.Repo.PinProjectCatalog:
// the machine's whole synced catalog, often hundreds of KB, read, hashed and
// upserted as a snapshot) and then reads the pinned snapshot back, two or
// three times, to describe, validate and attribute the definition. A GitHub
// pull_request delivery reaches every activation of the PR pipeline, about
// fifteen, which all share one project, machine and owner, so it made
// fifteen pins and some forty snapshot reads of the same bytes, one after
// another. Under a burst (a sweep relabelling a hundred PRs in seconds) the
// late triggers ran out of time before their event row was written, and the
// event was lost (prod issues c1b84ec9, dcead605). Through one Batch, the
// triggers of one scope pin once and read the snapshot once.
//
// Sharing one pin is what a single arrival means: its triggers fire at the
// same moment, and pins are content addressed, so the triggers of one scope
// would have pinned the same snapshot anyway unless a machine pushed in the
// milliseconds between them.
//
// A Batch lives for one arrival, handed to one Intake: it never sees a push
// made after its first pin, which is right for triggers fired together and
// wrong for anything that lives longer. Its zero value is not usable; a nil
// *Batch is, and memoizes nothing.
//
// The same holds for what an Accept reads about the event and the owner
// rather than the trigger: the write-ledger match and the writing run's
// lineage (OriginEnricher), and the owner's GitHub identity every filter scope
// carries (OwnerOf, through batchOwners). A pull_request.labeled delivery
// reaches about eight label triggers, most of which skip it; each read those
// again, two or three times over for the owner.
type Batch struct {
	pins   batchMemo[db.CatalogPinScope, string]
	pinned batchMemo[string, pinnedAnswer]
	names  batchMemo[string, string]

	echoes   batchMemo[string, echoMatch]
	lineages batchMemo[string, lineage.Lineage]
	conns    batchMemo[connectionKey, *core.Connection]
	subjects batchMemo[subjectKey, string]
}

type connectionKey struct{ userID, id string }

type subjectKey struct{ userID, integrationID string }

// inBatch is in with its owner reads memoized for b (batchOwners); in itself
// when b is nil.
func (in *Intake) inBatch(b *Batch) *Intake {
	if b == nil {
		return in
	}
	if _, ok := in.repo.(batchOwners); ok {
		return in
	}
	shared := *in
	shared.repo = batchOwners{IntakeRepo: in.repo, batch: b}
	return &shared
}

// batchOwners is an intake's repo whose OwnerSource reads are memoized for one
// Batch. Every other read and write is the repo's own.
type batchOwners struct {
	IntakeRepo
	batch *Batch
}

// GetConnection reads a connection once per Batch.
func (o batchOwners) GetConnection(ctx context.Context, userID, id string) (*core.Connection, error) {
	return o.batch.conns.get(connectionKey{userID, id}, func() (*core.Connection, error) {
		return o.IntakeRepo.GetConnection(ctx, userID, id)
	})
}

// GetIntegrationAccessSubject reads the subject once per Batch. freshAfter
// is "now, less the freshness window" for every asker of one arrival, a few
// milliseconds apart, so the first asker's answer stands for the rest.
func (o batchOwners) GetIntegrationAccessSubject(ctx context.Context, userID, integrationID string, freshAfter time.Time) (string, error) {
	return o.batch.subjects.get(subjectKey{userID, integrationID}, func() (string, error) {
		return o.IntakeRepo.GetIntegrationAccessSubject(ctx, userID, integrationID, freshAfter)
	})
}

type pinnedAnswer struct {
	catalog *db.PinnedCatalog
	found   bool
}

// NewBatch is a Batch for one arrival.
func NewBatch() *Batch { return &Batch{} }

// workflows is the resolver an Accept in b reads through: workflows itself
// unless it is a LaunchWorkflows over a store that pins and reads pins, whose
// store is then fronted by b. Every optional read resolution and attribution
// ask the store for by assertion stays answerable (see batchStore).
func (b *Batch) workflows(workflows WorkflowResolver) WorkflowResolver {
	if b == nil {
		return workflows
	}
	lw, ok := workflows.(LaunchWorkflows)
	if !ok {
		return workflows
	}
	pinner, ok := lw.Repo.(catalogPinner)
	if !ok {
		return workflows
	}
	pinned, ok := lw.Repo.(workflowsource.PinnedStore)
	if !ok {
		return workflows
	}
	lw.Repo = batchStore{WorkflowResolver: lw.Repo, pinner: pinner, pinned: pinned, batch: b}
	return lw
}

// batchStore is a store whose pins, pinned-snapshot reads and machine names
// are memoized for one Batch. Every other read is the store's own: the
// launch.WorkflowResolver set (which includes workflowsource.MachineStore and
// SourceDiagnostics) is embedded, and provenance, the one remaining optional
// read, is forwarded as attribution reads a store that lacks it.
type batchStore struct {
	launch.WorkflowResolver
	pinner catalogPinner
	pinned workflowsource.PinnedStore
	batch  *Batch
}

var (
	_ catalogPinner                    = batchStore{}
	_ workflowsource.PinnedStore       = batchStore{}
	_ workflowsource.MachineStore      = batchStore{}
	_ workflowsource.SourceDiagnostics = batchStore{}
	_ workflowProvenanceStore          = batchStore{}
	_ machineNamer                     = batchStore{}
)

// PinProjectCatalog pins scope once per Batch.
func (s batchStore) PinProjectCatalog(ctx context.Context, scope db.CatalogPinScope) (string, error) {
	return s.batch.pins.get(scope, func() (string, error) {
		return s.pinner.PinProjectCatalog(ctx, scope)
	})
}

// GetPinnedProjectCatalog reads pin once per Batch.
func (s batchStore) GetPinnedProjectCatalog(ctx context.Context, pin string) (*db.PinnedCatalog, bool, error) {
	got, err := s.batch.pinned.get(pin, func() (pinnedAnswer, error) {
		c, found, err := s.pinned.GetPinnedProjectCatalog(ctx, pin)
		return pinnedAnswer{c, found}, err
	})
	return got.catalog, got.found, err
}

// ProjectWorkflowProvenance is the store's, or none from a store without it
// (attributeMachine then attributes nothing, as it does for such a store).
func (s batchStore) ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error) {
	p, ok := s.WorkflowResolver.(workflowProvenanceStore)
	if !ok {
		return nil, nil
	}
	return p.ProjectWorkflowProvenance(ctx, projectID)
}

// MachineDisplayName is the store's, once per machine per Batch; "" (no name)
// from a store without it.
func (s batchStore) MachineDisplayName(ctx context.Context, daemonID string) string {
	n, ok := s.WorkflowResolver.(machineNamer)
	if !ok {
		return ""
	}
	name, _ := s.batch.names.get(daemonID, func() (string, error) {
		return n.MachineDisplayName(ctx, daemonID), nil
	})
	return name
}

// batchMemo is one read's answers by argument for one Batch.
type batchMemo[K comparable, V any] struct {
	mu   sync.Mutex
	seen map[K]V
}

// get returns key's answer, reading it with load until a read succeeds. The
// lock is held across load, so concurrent askers of one key read it once.
//
// Only a success is kept. A failure (a timeout, a dropped connection) is the
// asker's alone, and the next trigger, with a budget of its own, reads again:
// sharing it would turn one trigger's lost event into every trigger's, and a
// sender like GitHub never redelivers on its own.
func (m *batchMemo[K, V]) get(key K, load func() (V, error)) (V, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.seen[key]; ok {
		return v, nil
	}
	v, err := load()
	if err != nil {
		return v, err
	}
	if m.seen == nil {
		m.seen = map[K]V{}
	}
	m.seen[key] = v
	return v, nil
}

// Copyright (c) 2026 Reliant Labs

// Package recorddue fires record_due triggers: the worker's due scan over
// the registered tables (pr_pipeline today; core.RecordDueTables), which
// turns each record whose next action has fallen due into one trigger event
// per enabled record_due activation of its project (pipeline redesign §2,
// "level-triggered decisions, edge-triggered wakeups"; build plan task M4).
//
// The scan starts no run unless something is due: with nothing due it is one
// indexed query (store.ListDuePRPipelines over pr_pipeline_due_idx) and
// nothing else. A due record is fired through the same inbound path as a
// webhook or a provider event (triggers.Intake.Accept): recorded as a
// trigger_events row, filtered by the activation's declaration, and launched
// by the event-fire workflow, which the redriver repairs.
//
// It clears nothing: whoever acts on the record (pr.next) moves or clears its
// next_action_at by compare-and-set. Until then the record stays due and is
// found again on each traversal, and the event's dedupe key (trigger, record,
// due time) makes each repeat a no-op: a record fires once per due time, however
// many scans, workers or redeliveries see it. Rows are scanned FOR UPDATE
// SKIP LOCKED and held while their events are recorded, so two workers'
// scans split the due rows rather than both firing them.
//
// Built by hand by internal/app's NewRecordDueScanner, only while
// pipeline.enabled is on: with it off there is no scanner.
//
//forge:provided
package recorddue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// scanInterval is how often a worker scans for due records.
const scanInterval = 15 * time.Second

// BatchSize bounds the due records one scan fires.
const BatchSize = 100

// Deps is what a scanner needs.
type Deps struct {
	// Store reads the due records and the activations they fire.
	Store Store
	// Intake records each due record's event and starts its fire.
	Intake Intake
	// Scope, when set, decorates the context each scan runs in (membership
	// and org policy, as the inbox relay's).
	// forge:optional-dep
	Scope func(context.Context) context.Context
}

// scanner is the due scan. It is safe for one Run at a time; Scan may run
// concurrently with another scanner's.
//
//forge:contract
type scanner struct {
	// mu serializes Scan, which advances after: the cursor of the next
	// page of due rows.
	mu       sync.Mutex
	after    *core.PRPipeline
	store    Store
	intake   Intake
	scope    func(context.Context) context.Context
	now      func() time.Time
	interval time.Duration
}

// New builds a scanner.
func New(deps Deps) Service {
	return &scanner{store: deps.Store, intake: deps.Intake, scope: deps.Scope, now: time.Now, interval: scanInterval}
}

// Run scans every interval until ctx ends. A failed scan is logged and the
// next one retries.
func (s *scanner) Run(ctx context.Context) error {
	if s.scope != nil {
		ctx = s.scope(ctx)
	}
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		if _, err := s.Scan(ctx); err != nil && ctx.Err() == nil {
			logging.WarnContext(ctx, "[recorddue] due scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Scan fires the records due now, once each per matching activation and due
// time, and returns how many events it recorded. A trigger whose event
// cannot be recorded is logged and skipped, so one broken activation cannot
// keep the others from firing; the error is a scan that could not run.
func (s *scanner) Scan(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	fired := 0
	next, err := s.store.WithDuePRPipelinesAfter(ctx, now, BatchSize, s.after, func(due []core.DuePRPipeline) error {
		if len(due) == 0 {
			return nil
		}
		activations, err := s.store.ListEnabledTriggersOfKind(ctx, core.TriggerKindRecordDue)
		if err != nil {
			return err
		}
		for _, d := range due {
			for _, t := range activations {
				if !Fires(t, core.RecordDueTablePRPipeline, d.Row) {
					continue
				}
				ok, err := s.fire(ctx, t, d)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					logging.WarnContext(ctx, "[recorddue] could not record a due record's event",
						"trigger_id", t.ID, "record_id", d.Row.RecordID, "error", err)
					continue
				}
				if ok {
					fired++
				}
			}
		}
		return nil
	})
	if err == nil {
		// Advance even when no activation fired; still-due, filtered or broken
		// records must not monopolize the next tick. Wrap after the last page
		// so newly due rows behind the cursor are examined on the next pass.
		s.after = next
	}
	return fired, err
}

// fire records one due record's event for one activation; false when it was
// recorded before (an earlier scan of the same due time).
func (s *scanner) fire(ctx context.Context, t *core.Trigger, d core.DuePRPipeline) (bool, error) {
	if d.Row.NextActionAt == nil {
		return false, nil
	}
	key := DedupeKey(t.ID, d.Row.RecordID, *d.Row.NextActionAt)
	// Most scans of a due record find its event already recorded (the record
	// stays due until it is acted on): one indexed read settles that, before
	// any declaration is resolved or filter evaluated.
	if _, err := s.store.GetTriggerEventByDedupe(ctx, core.TriggerEventKindRecordDue, key); err == nil {
		return false, nil
	} else if !errors.Is(err, core.ErrTriggerEventNotFound) {
		return false, err
	}
	res, err := s.intake.Accept(ctx, t, triggers.InboundEvent{
		Kind:       core.TriggerEventKindRecordDue,
		DedupeKey:  key,
		OccurredAt: d.Row.NextActionAt.UTC(),
		Payload:    Payload(core.RecordDueTablePRPipeline, d),
		Sender: &core.TriggerSender{
			// Nobody outside sends a due time, as nobody sends a schedule
			// its slot: verified, like a schedule's.
			Kind: core.TriggerSenderKindSchedule, ID: core.RecordDueTablePRPipeline,
			DisplayName: core.RecordDueTablePRPipeline + " due scan", Verified: true,
		},
	}, triggers.AcceptOptions{})
	if err != nil {
		return false, err
	}
	return !res.Duplicate, nil
}

// Fires reports whether activation t, a record_due trigger, fires for row of
// table: its config names the table, and it belongs to the row's project
// (and org, once the activation has one).
func Fires(t *core.Trigger, table string, row *core.PRPipeline) bool {
	if t.Kind != core.TriggerKindRecordDue || !t.Enabled || t.ProjectID != row.ProjectID {
		return false
	}
	if t.OrgID != nil && *t.OrgID != "" && *t.OrgID != row.OrgID {
		return false
	}
	var cfg core.RecordDueConfig
	if err := json.Unmarshal(t.Config, &cfg); err != nil {
		return false
	}
	return cfg.Table == table
}

// DedupeKey is a record_due event's dedupe key: one per activation, record
// and due time.
func DedupeKey(triggerID, recordID string, due time.Time) string {
	return fmt.Sprintf("%s:%s@%s", triggerID, recordID, due.UTC().Format(time.RFC3339Nano))
}

// Payload is a record_due event's trigger.payload, which the trigger root
// lifts into trigger.record (runtime.RecordCELValue).
func Payload(table string, d core.DuePRPipeline) map[string]any {
	due := ""
	if d.Row.NextActionAt != nil {
		due = d.Row.NextActionAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"table":       table,
		"record_id":   d.Row.RecordID,
		"repository":  d.Record.Repository,
		"number":      int64(d.Record.Number),
		"head_sha":    d.Record.HeadSHA,
		"next_action": d.Row.NextAction,
		"due_at":      due,
	}
}

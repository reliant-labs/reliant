// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/triggers/lineage"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// IntentWindow is how long after a write an event may still be attributed to it
// by target and content (an "intent" match). An echo arrives within moments; the
// window is generous enough for a slow webhook queue and short enough that a
// person who happens to say the same thing an hour later is not mistaken for us.
const IntentWindow = 10 * time.Minute

// LedgerReader is the write ledger as the origin enricher reads it. It is
// declared here, at the consumer; core.IntegrationWriteStore satisfies it.
type LedgerReader interface {
	GetIntegrationWriteByObject(ctx context.Context, integration, scope, objectKind, objectID string) (*core.IntegrationWrite, error)
	ListRecentIntegrationWrites(ctx context.Context, integration, scope string, since time.Time, limit int) ([]*core.IntegrationWrite, error)
}

// RunLineage finds the lineage of the run that wrote. *db.Repo satisfies it.
type RunLineage interface {
	lineage.LaunchEventReader
}

// OriginEnricher resolves trigger.origin for an arriving integration event: did
// a run of Reliant's cause it, and which.
//
// Attribution is evidence-weighed, strongest first:
//
//	exact    the event's own object id is one the ledger recorded a run creating
//	intent   same target, event type and content as a recent ledger entry
//	app      the event came from Reliant's own GitHub App; the run is unknown
//	claimed  a Reliant-Run commit trailer: spoofable, so it may only suppress
//
// Everything the event says about itself is sender-controlled, so none of it is
// ever authority: a match only ever tells a workflow "ignore this" or "this was
// us", and the one signal a stranger can forge outright (the trailer) is the
// weakest and carries no run identity.
type OriginEnricher struct {
	ledger  LedgerReader
	lineage RunLineage
	// appSlug is Reliant's own GitHub App slug, lowercased; "" disables the app
	// and claimed tiers' reliance on it.
	appSlug string
	now     func() time.Time
}

// NewOriginEnricher builds the enricher. appSlug is the GitHub App slug
// (reliant-labs in production); empty means no event is attributed to the App.
func NewOriginEnricher(ledger LedgerReader, lin RunLineage, appSlug string) *OriginEnricher {
	return &OriginEnricher{ledger: ledger, lineage: lin, appSlug: strings.ToLower(strings.TrimSpace(appSlug)), now: time.Now}
}

var _ Enricher = (*OriginEnricher)(nil)

// Enrich resolves the origin of ev for trigger. It never fails the event: an
// error reading the ledger is logged and the event is treated as not caused by
// Reliant. That is safe for the loop guard only because the lineage cap needs no
// ledger.
//
// An event that carries no echo (another source, another integration) is not
// looked at: it has no origin, which readers treat as "not caused by Reliant".
func (e *OriginEnricher) Enrich(ctx context.Context, trigger *core.Trigger, ev InboundEvent) (Enrichment, error) {
	if e == nil || ev.Echo == nil || ev.Kind != core.TriggerEventKindIntegration || ev.Echo.Scope == "" {
		return Enrichment{}, nil
	}
	integration, _ := ev.Payload["integration"].(string)
	if integration != "github" {
		return Enrichment{}, nil
	}
	return Enrichment{Origin: e.resolve(ctx, trigger, integration, ev.Echo)}, nil
}

func (e *OriginEnricher) resolve(ctx context.Context, trigger *core.Trigger, integration string, echo *provenance.Echo) *core.TriggerOrigin {
	none := &core.TriggerOrigin{Confidence: core.OriginNone, Actor: core.OriginActorUnknown}

	write, confidence := e.match(ctx, integration, echo)
	switch {
	case write != nil:
		return e.fromWrite(ctx, trigger, write, confidence)
	case e.isApp(echo):
		return &core.TriggerOrigin{CausedByReliant: true, Confidence: core.OriginApp, Actor: core.OriginActorApp}
	case echo.RunTrailer != "":
		return &core.TriggerOrigin{CausedByReliant: true, Confidence: core.OriginClaimed, Actor: core.OriginActorUnknown}
	}
	return none
}

// match finds the ledger entry an echo is the consequence of: by object id first
// (exact), then by target and content inside the window (intent).
func (e *OriginEnricher) match(ctx context.Context, integration string, echo *provenance.Echo) (*core.IntegrationWrite, core.OriginConfidence) {
	if e.ledger == nil {
		return nil, core.OriginNone
	}
	if echo.ObjectID != "" && echo.ObjectKind != "" {
		w, err := e.ledger.GetIntegrationWriteByObject(ctx, integration, echo.Scope, echo.ObjectKind, echo.ObjectID)
		switch {
		case err == nil && echoableBy(w, echo):
			return w, core.OriginExact
		case err != nil && !errors.Is(err, core.ErrIntegrationWriteNotFound):
			slog.Warn("origin: could not read the write ledger by object", "scope", echo.Scope, "error", err)
		}
	}
	recent, err := e.ledger.ListRecentIntegrationWrites(ctx, integration, echo.Scope, e.now().Add(-IntentWindow), 50)
	if err != nil {
		slog.Warn("origin: could not read the write ledger", "scope", echo.Scope, "error", err)
		return nil, core.OriginNone
	}
	for _, w := range recent {
		if intentMatches(w, echo) {
			return w, core.OriginIntent
		}
	}
	return nil, core.OriginNone
}

// echoableBy reports whether a write can produce this event type: an id match
// on a comment is only ours if the event is about a comment, not an unrelated
// event that happens to carry the same number.
func echoableBy(w *core.IntegrationWrite, echo *provenance.Echo) bool {
	c := provenance.CorrelationFromMap(w.Correlation)
	return len(c.Events) == 0 || contains(c.Events, echo.EventType)
}

// intentMatches is the weaker, content-based match: the write and the event
// agree on what was done, to what, in how many words. Every comparison is on
// fields both sides have; a field only one side has is not a mismatch (the
// ledger cannot know a creation's number yet), but at least one content field
// must agree, so a bare "same repository, same event type" never attributes.
func intentMatches(w *core.IntegrationWrite, echo *provenance.Echo) bool {
	c := provenance.CorrelationFromMap(w.Correlation)
	if c.ObjectKind != echo.ObjectKind || !contains(c.Events, echo.EventType) {
		return false
	}
	// Where: the same issue, pull request or branch. A creation's target is the
	// repository until its response names the number, so it matches by scope.
	if w.Target != w.Scope && echo.Target != "" && w.Target != echo.Target {
		return false
	}
	agreed := 0
	check := func(want, got string) bool {
		if want == "" || got == "" {
			return true
		}
		if want != got {
			return false
		}
		agreed++
		return true
	}
	if !check(c.BodySHA256, echo.BodySHA256) || !check(c.Label, echo.Label) ||
		!check(c.Branch, echo.Branch) || !check(c.ReviewState, echo.ReviewState) {
		return false
	}
	if c.HeadSHA != "" && echo.HeadSHA != "" {
		if !strings.HasPrefix(echo.HeadSHA, c.HeadSHA) {
			return false
		}
		agreed++
	}
	if c.State != "" && echo.State != "" {
		if !strings.EqualFold(c.State, echo.State) {
			return false
		}
		agreed++
	}
	// A label written among several (an issue created with labels) matches any.
	if c.Label == "" && echo.Label != "" && contains(c.Labels, echo.Label) {
		agreed++
	}
	return agreed > 0
}

// fromWrite builds the origin for an event a ledger entry caused.
//
// Whose run it was is shown only to the SAME owner's triggers. One event fans out
// to every user whose trigger it reaches, and another user's trigger learns that
// Reliant caused it and nothing about whose run. How DEEP the writing run's chain
// is, though, is not identity, and the loop cap must count it for everyone: a
// cross-user pair of workflows is a loop like any other.
func (e *OriginEnricher) fromWrite(ctx context.Context, trigger *core.Trigger, w *core.IntegrationWrite, confidence core.OriginConfidence) *core.TriggerOrigin {
	actor := core.OriginActorOwner
	if w.Actor == core.IntegrationActorApp {
		actor = core.OriginActorApp
	}
	var chain lineage.Lineage
	if w.ChatID != "" && e.lineage != nil {
		var err error
		if chain, err = lineage.Of(ctx, e.lineage, w.ChatID); err != nil {
			// An unreadable lineage must not silently become depth 0, which would
			// let a chain grow unbounded: treat it as AT the cap, which refuses to
			// launch from it.
			slog.Warn("origin: could not read the writing run's lineage; treating it as at the cap",
				"run_id", w.ChatID, "error", err)
			chain = make(lineage.Lineage, lineage.MaxDepth)
		}
	}
	if w.UserID != trigger.UserID {
		return &core.TriggerOrigin{CausedByReliant: true, Confidence: confidence, Actor: actor, Foreign: true, Depth: chain.Depth()}
	}
	return &core.TriggerOrigin{
		CausedByReliant: true, Confidence: confidence, Actor: actor,
		RunID: w.ChatID, Workflow: w.WorkflowName, TriggerName: w.TriggerName, WriteID: w.ID,
		SameWorkflow: sameWorkflow(trigger, w),
		Lineage:      chain.ToCore(), Depth: chain.Depth(),
	}
}

// sameWorkflow: the run that wrote is a run of the workflow trigger belongs to.
// Names compare the way the launcher and workflow_event matching do
// (runevents.WorkflowRef), so a display name and its slug are one workflow.
func sameWorkflow(trigger *core.Trigger, w *core.IntegrationWrite) bool {
	if trigger.Workflow == "" || w.WorkflowName == "" {
		return false
	}
	return runevents.WorkflowRef(trigger.Workflow) == runevents.WorkflowRef(w.WorkflowName)
}

// isApp: the event came from Reliant's own App: an object it created, or its bot
// as the sender.
func (e *OriginEnricher) isApp(echo *provenance.Echo) bool {
	if e.appSlug == "" {
		return false
	}
	if echo.AppSlug == e.appSlug {
		return true
	}
	return echo.SenderType == "Bot" && strings.EqualFold(strings.TrimSuffix(echo.SenderLogin, "[bot]"), e.appSlug)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// GitHubAppSlugEnv names the GitHub App Reliant's own writes appear as when
// they are made through it (reliant-labs in production). It is how an event the
// App caused is recognised when no ledger entry names the run. Unset, no event is
// attributed to the App: a deployment without one has no App to attribute to.
const GitHubAppSlugEnv = "RELIANT_GITHUB_APP_SLUG"

// OriginEnricherFromEnv builds the enricher a webhook receiver wires into its
// Intake, reading the App slug from GitHubAppSlugEnv.
func OriginEnricherFromEnv(ledger LedgerReader, lin RunLineage, getenv func(string) string) *OriginEnricher {
	return NewOriginEnricher(ledger, lin, getenv(GitHubAppSlugEnv))
}

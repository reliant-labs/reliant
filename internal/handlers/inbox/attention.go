package inbox

import (
	"context"
	"slices"
	"strconv"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"golang.org/x/sync/errgroup"
)

// failingAutomations combines actionable incident episodes with legacy trigger
// streaks. Only registered attention actors leave legacy streaks; diagnostic
// health still sees every firing. Incidents outlive paused/deleted triggers.
// Independent reads run together, each holding one of lim's tokens.
func (s *Service) failingAutomations(ctx context.Context, lim readLimiter, userID string, stored []*core.Trigger) ([]*reliantv1.InboxItem, error) {
	ids := make([]string, 0, len(stored))
	for _, t := range stored {
		if t.Enabled {
			ids = append(ids, t.ID)
		}
	}
	var (
		recent, episodes, diagnosticEpisodes map[string][]*core.TriggerEventWithRun
		regs                                 map[string]*core.TriggerRegistration
		incidents                            []*core.AttentionIncident
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			if len(ids) == 0 {
				return nil
			}
			recent, err = s.deps.Repo.RecentTriggerOutcomes(gctx, userID, ids, triggers.HealthWindow)
			return err
		})
	})
	// The episode is read separately from the health window: a streak longer
	// than the window must still be one item with an honest count.
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			if len(ids) == 0 {
				return nil
			}
			episodes, err = s.deps.Repo.FiringsSinceLastUnmanagedSuccess(gctx, userID, ids, triggers.EpisodeFirings)
			return err
		})
	})
	// Diagnostic full-episode counts keep the original unfiltered success
	// boundary, independently of the legacy inbox's attention-filtered episode.
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			if len(ids) == 0 {
				return nil
			}
			diagnosticEpisodes, err = s.deps.Repo.FiringsSinceLastSuccess(gctx, userID, ids, triggers.EpisodeFirings)
			return err
		})
	})
	// A polled source that cannot authenticate never fires, so it has no
	// failed firing to show; its source state is what says it is failing.
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			if len(ids) == 0 {
				return nil
			}
			regs, err = s.deps.Repo.ListTriggerRegistrations(gctx, userID, ids)
			return err
		})
	})
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			incidents, err = s.deps.Repo.ListAttentionIncidents(gctx, userID)
			return err
		})
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	chatIDs := []string{}
	for _, groups := range []map[string][]*core.TriggerEventWithRun{recent, episodes} {
		for _, firings := range groups {
			for _, f := range firings {
				if f.Event.Outcome == core.TriggerEventLaunched && f.Event.ChatID != nil {
					chatIDs = append(chatIDs, *f.Event.ChatID)
				}
			}
		}
	}
	slices.Sort(chatIDs)
	chatIDs = slices.Compact(chatIDs)
	var managed map[string]bool
	if len(chatIDs) > 0 {
		if err := lim.do(ctx, func() (err error) {
			managed, err = s.deps.Repo.AttentionManagedChats(ctx, userID, chatIDs)
			return err
		}); err != nil {
			return nil, err
		}
	}
	now := time.Now()

	// The store projects expired leases as action_required and validates current
	// chat/project ownership. Policy presence alone never suppresses legacy rows.
	out := attentionInboxItems(userID, incidents, stored, recent)
	for _, t := range stored {
		if !t.Enabled {
			continue
		}
		firings := recent[t.ID]
		if reg := regs[t.ID]; reg != nil && reg.Status == core.TriggerRegistrationNeedsReauth {
			// Not receiving events at all until the owner reconnects. The
			// episode is the registration's: it starts when the source
			// entered needs_reauth, so a dismissal holds until the next one.
			health := triggers.WithSource(triggers.ComputeHealth(firings), reg, now)
			out = append(out, &reliantv1.InboxItem{
				Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
				ItemId:       inboxFailingPrefix + "source:" + t.ID + ":" + strconv.FormatInt(reg.StatusSince.UnixMilli(), 10),
				TriggerId:    t.ID,
				TriggerName:  t.Name,
				ProjectId:    t.ProjectID,
				ProjectName:  t.ProjectName,
				WorkflowName: t.Workflow,
				WaitingSince: reg.StatusSince.UTC().Format(time.RFC3339Nano),
				Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: &reliantv1.InboxAutomationFailing{Health: health}},
			})
			continue
		}
		health := triggers.ComputeHealth(firings) // diagnostic: includes managed runs
		legacyEpisode := unmanagedFirings(episodes[t.ID], managed)
		streak := triggers.FailureStreak(legacyEpisode)
		if streak.First == nil {
			continue
		}
		if triggers.ComputeHealth(legacyEpisode).Status != reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING && streak.RunFailures == 0 {
			continue
		}
		health.ConsecutiveFailures = triggers.FailureStreak(diagnosticEpisodes[t.ID]).Count
		payload := &reliantv1.InboxAutomationFailing{Health: health}
		for _, f := range legacyEpisode {
			if f.Run != nil {
				chatID := f.Run.ChatID
				payload.LastRunChatId = &chatID
				break
			}
		}
		out = append(out, &reliantv1.InboxItem{
			Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
			ItemId:       inboxFailingPrefix + streak.First.Event.ID,
			TriggerId:    t.ID,
			TriggerName:  t.Name,
			ProjectId:    t.ProjectID,
			ProjectName:  t.ProjectName,
			WorkflowName: t.Workflow,
			WaitingSince: streak.First.Event.OccurredAt.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: payload},
		})
	}
	return out, nil
}

// unmanagedFirings removes only successfully registered launched actors. Source
// and launch failures have no managed run and must retain legacy behavior.
func unmanagedFirings(firings []*core.TriggerEventWithRun, managed map[string]bool) []*core.TriggerEventWithRun {
	out := make([]*core.TriggerEventWithRun, 0, len(firings))
	for _, f := range firings {
		if f.Event.Outcome == core.TriggerEventLaunched && f.Event.ChatID != nil && managed[*f.Event.ChatID] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// attentionInboxItems names an incident by episode, not by trigger or actor:
// repairs and repeat failures keep one dismissal, a reopened episode gets a new
// one. Recovering is not actionable here; expiry is projected by the store.
func attentionInboxItems(userID string, incidents []*core.AttentionIncident, stored []*core.Trigger, recent map[string][]*core.TriggerEventWithRun) []*reliantv1.InboxItem {
	byID := map[string]*core.Trigger{}
	for _, t := range stored {
		byID[t.ID] = t
	}
	var out []*reliantv1.InboxItem
	for _, incident := range incidents {
		if incident.UserID != userID || incident.ProjectID == "" || incident.EpisodeID == "" || incident.State != "action_required" {
			continue
		}
		payload := &reliantv1.InboxAutomationFailing{
			IncidentKey: incident.Key, AttentionState: incident.State,
			FailureClass: incident.FailureClass, Detail: incident.Detail,
		}
		if incident.ChatID != "" {
			payload.LastRunChatId = &incident.ChatID
		}
		it := &reliantv1.InboxItem{
			Kind:      reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
			ItemId:    inboxFailingPrefix + "incident:" + incident.EpisodeID,
			ProjectId: incident.ProjectID, ChatId: incident.ChatID,
			WaitingSince: incident.OpenedAt.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: payload},
		}
		// Metadata is advisory. A deleted/paused trigger cannot hide an open
		// incident, and an unrelated trigger can never supply its display data.
		if t := byID[incident.TriggerID]; t != nil && t.UserID == userID && t.ProjectID == incident.ProjectID {
			it.TriggerId, it.TriggerName = t.ID, t.Name
			it.ProjectName, it.WorkflowName = t.ProjectName, t.Workflow
			payload.Health = triggers.ComputeHealth(recent[t.ID])
		}
		out = append(out, it)
	}
	return out
}

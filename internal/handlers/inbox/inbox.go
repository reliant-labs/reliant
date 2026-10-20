// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

const defaultInboxLimit = 100

// Item id prefixes, one per kind, so an id names what it dismisses. A failure
// id embeds the FIRST failing event of the current episode, so more failures
// stay one item (and one dismissal), while a new episode after a success is a
// new item. A waiting-for-machine id embeds the chat and when the block began,
// so a later block is a new item. Approval, question and run-finished ids
// embed their row or chat. Storage ids are described at storageItemID.
const (
	inboxApprovalPrefix          = "approval:"
	inboxQuestionPrefix          = "question:"
	inboxWaitingForMachinePrefix = "waiting_for_machine:"
	inboxFailingPrefix           = "automation_failing:"
	inboxLaunchFailedPrefix      = "automation_launch_failed:"
	inboxRunFinishedPrefix       = "run_finished:"
	inboxStoragePrefix           = "storage:"
)

var inboxItemPrefixes = []string{
	inboxApprovalPrefix, inboxQuestionPrefix, inboxWaitingForMachinePrefix,
	inboxFailingPrefix, inboxLaunchFailedPrefix, inboxRunFinishedPrefix, inboxStoragePrefix,
}

// maxInboxDismissBatch bounds one Dismiss/Restore call ("Dismiss all" on a
// section). The list itself is capped at defaultInboxLimit.
const maxInboxDismissBatch = 500

// ListInbox returns everything waiting on the caller, optionally within one
// project. Pending rows come from one UNION ALL query; automation health reuses
// triggers.ComputeHealth over one batched firings query, so the cost does not
// grow with the number of triggers.
//
// Scoping is applied after dismissal so the scoped response can also say how
// many items wait in the caller's other projects (one read, not two).
func (s *Service) ListInbox(
	ctx context.Context,
	req *connect.Request[reliantv1.ListInboxRequest],
) (*connect.Response[reliantv1.ListInboxResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	limit := defaultInboxLimit
	if req.Msg.Limit != nil {
		limit = int(*req.Msg.Limit)
		if limit < 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("limit must not be negative"))
		}
	}

	projectID := strings.TrimSpace(req.Msg.GetProjectId())

	pending, err := s.deps.Repo.ListInboxPending(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to list inbox", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}
	failing, err := s.failingAutomations(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to compute failing automations for inbox", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}

	items := make([]*reliantv1.InboxItem, 0, len(pending)+len(failing))
	for _, p := range pending {
		items = append(items, inboxItemFromPending(p))
	}
	items = append(items, failing...)
	storage, err := s.storageItems(ctx, userID)
	if err != nil {
		// Disk state is advisory; a failure here must not blank the whole inbox.
		logging.WarnContext(ctx, "Failed to compute storage inbox items", "error", err)
	}
	items = append(items, storage...)

	items, err = s.dropDismissed(ctx, userID, items)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to filter dismissed inbox items", "error", err)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to list inbox"))
	}

	resp := &reliantv1.ListInboxResponse{}
	if projectID != "" {
		scoped := items[:0]
		for _, it := range items {
			// A machine's storage is not any project's: it shows in every scope.
			if it.ProjectId == projectID || it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE {
				scoped = append(scoped, it)
			} else {
				resp.OtherProjectsCount++
			}
		}
		items = scoped
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		if items[i].WaitingSince != items[j].WaitingSince {
			return items[i].WaitingSince < items[j].WaitingSince
		}
		return items[i].ItemId < items[j].ItemId
	})

	for _, it := range items {
		switch it.Kind {
		case reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
			reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION,
			reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE:
			resp.BlockingCount++
		default:
			resp.HasInformational = true
		}
	}
	if len(items) > limit {
		items = items[:limit]
		resp.Truncated = true
	}
	resp.Items = items
	return connect.NewResponse(resp), nil
}

// DismissInboxItem hides items from the caller's inbox. Hiding an approval or
// a question does not resolve it; the run still waits on it.
func (s *Service) DismissInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissInboxItemRequest],
) (*connect.Response[reliantv1.DismissInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.DismissInboxItems(ctx, userID, impliedDismissals(req.Msg.ItemIds), time.Now().UTC()); err != nil {
		logging.ErrorContext(ctx, "Failed to dismiss inbox items", "error", err, "count", len(req.Msg.ItemIds))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to dismiss inbox item"))
	}
	return connect.NewResponse(&reliantv1.DismissInboxItemResponse{}), nil
}

// RestoreInboxItem undoes DismissInboxItem (the Undo on the dismiss toast).
func (s *Service) RestoreInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.RestoreInboxItemRequest],
) (*connect.Response[reliantv1.RestoreInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.RestoreInboxItems(ctx, userID, impliedDismissals(req.Msg.ItemIds)); err != nil {
		logging.ErrorContext(ctx, "Failed to restore inbox items", "error", err, "count", len(req.Msg.ItemIds))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to restore inbox item"))
	}
	return connect.NewResponse(&reliantv1.RestoreInboxItemResponse{}), nil
}

func validateInboxItemIDs(itemIDs []string) error {
	if len(itemIDs) == 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("item_ids is required"))
	}
	if len(itemIDs) > maxInboxDismissBatch {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d item ids per call", maxInboxDismissBatch))
	}
	for _, id := range itemIDs {
		if !isInboxItemID(id) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("not an inbox item id: %q", id))
		}
	}
	return nil
}

func isInboxItemID(itemID string) bool {
	for _, prefix := range inboxItemPrefixes {
		if strings.HasPrefix(itemID, prefix) && len(itemID) > len(prefix) {
			return true
		}
	}
	return false
}

// failingAutomations returns one item per enabled trigger whose health is
// FAILING, using the same ComputeHealth as Trigger.health.
func (s *Service) failingAutomations(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	stored, err := s.deps.Repo.ListTriggers(ctx, core.TriggerFilters{UserID: userID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(stored))
	for _, t := range stored {
		if t.Enabled {
			ids = append(ids, t.ID)
		}
	}
	recent, err := s.deps.Repo.RecentTriggerFirings(ctx, userID, ids, triggers.HealthWindow)
	if err != nil {
		return nil, err
	}

	// The episode is read separately from the health window: a streak longer
	// than the window must still be one item with an honest count.
	episodes, err := s.deps.Repo.FiringsSinceLastSuccess(ctx, userID, ids, triggers.EpisodeFirings)
	if err != nil {
		return nil, err
	}
	// A polled source that cannot authenticate never fires, so it has no
	// failed firing to show; its source state is what says it is failing.
	regs, err := s.deps.Repo.ListTriggerRegistrations(ctx, userID, ids)
	if err != nil {
		return nil, err
	}
	now := time.Now()

	var out []*reliantv1.InboxItem
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
		health := triggers.ComputeHealth(firings)
		if health.Status != reliantv1.TriggerHealthStatus_TRIGGER_HEALTH_STATUS_FAILING {
			continue
		}
		streak := triggers.FailureStreak(episodes[t.ID])
		if streak.First == nil {
			continue
		}
		health.ConsecutiveFailures = streak.Count
		payload := &reliantv1.InboxAutomationFailing{Health: health}
		for _, f := range firings {
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

func (s *Service) dropDismissed(ctx context.Context, userID string, items []*reliantv1.InboxItem) ([]*reliantv1.InboxItem, error) {
	keys := make([][]string, len(items))
	candidates := make([]string, 0, len(items))
	for i, it := range items {
		keys[i] = dismissalKeys(it.ItemId)
		candidates = append(candidates, keys[i]...)
	}
	dismissed, err := s.deps.Repo.ListDismissedInboxItemIDs(ctx, userID, candidates)
	if err != nil {
		return nil, err
	}
	if len(dismissed) == 0 {
		return items, nil
	}
	kept := items[:0]
	for i, it := range items {
		if !slices.ContainsFunc(keys[i], func(id string) bool { return dismissed[id] }) {
			kept = append(kept, it)
		}
	}
	return kept, nil
}

func inboxItemFromPending(p *core.InboxPending) *reliantv1.InboxItem {
	item := &reliantv1.InboxItem{
		ItemId:       p.ItemKey,
		ChatId:       p.ChatID,
		RunId:        p.RunID,
		TriggerId:    p.TriggerID,
		TriggerName:  p.TriggerName,
		ProjectId:    p.ProjectID,
		ProjectName:  p.ProjectName,
		WorkflowName: p.WorkflowName,
		ChatTitle:    p.ChatTitle,
		WaitingSince: p.WaitingSince.UTC().Format(time.RFC3339Nano),
	}
	switch p.Kind {
	case core.InboxPendingApproval:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL
		item.ItemId = inboxApprovalPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_Approval{Approval: inboxApproval(p)}
	case core.InboxPendingQuestion:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION
		item.ItemId = inboxQuestionPrefix + p.ItemKey
		q := &reliantv1.InboxQuestion{QuestionId: p.ItemKey, ThreadId: p.Text1, Prompt: firstQuestionPrompt(p.Text2)}
		if p.Text2 != "" {
			meta := p.Text2
			q.Metadata = &meta
		}
		item.Payload = &reliantv1.InboxItem_Question{Question: q}
	case core.InboxPendingWaitingForMachine:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE
		item.ItemId = inboxWaitingForMachinePrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_WaitingForMachine{WaitingForMachine: &reliantv1.InboxWaitingForMachine{
			DaemonId: p.Text1, DaemonName: p.Text2,
		}}
	case core.InboxPendingLaunchFailed:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED
		item.ItemId = inboxLaunchFailedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_AutomationLaunchFailed{AutomationLaunchFailed: &reliantv1.InboxAutomationLaunchFailed{
			Reason: p.Text1, EventId: p.ItemKey, ConsecutiveFailures: p.Int1,
		}}
	case core.InboxPendingRunFinished:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_RUN_FINISHED
		item.ItemId = inboxRunFinishedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_RunFinished{RunFinished: &reliantv1.InboxRunFinished{}}
	}
	return item
}

func inboxApproval(p *core.InboxPending) *reliantv1.InboxApproval {
	return &reliantv1.InboxApproval{
		ApprovalId:   p.ItemKey,
		ApprovalType: reliantv1.ApprovalType(p.Int1),
		Title:        p.Text1,
	}
}

func firstQuestionPrompt(metadata string) string {
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if metadata == "" || json.Unmarshal([]byte(metadata), &payload) != nil || len(payload.Questions) == 0 {
		return ""
	}
	return payload.Questions[0].Question
}

// storageItems returns one item per machine with a volume under disk pressure
// or archived worktrees it declined to remove. Storage items are not tied to a
// project, so they appear in every project scope.
func (s *Service) storageItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	machines, err := s.storageView(ctx, userID)
	if err != nil || len(machines) == 0 {
		return nil, err
	}
	daemons, err := s.deps.Repo.ListDaemonsByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, d := range daemons {
		if d.Hostname != nil {
			names[d.ID] = *d.Hostname
		}
	}
	var out []*reliantv1.InboxItem
	for _, m := range machines {
		payload := &reliantv1.InboxStorage{
			DaemonId:   m.DaemonID,
			DaemonName: names[m.DaemonID],
			DiskLow:    m.DiskLow(),
			Online:     m.Online,
		}
		if m.Disk != nil {
			// Every volume, fullest first: the item names the one that is
			// full ("build caches & docker") and what is using it.
			payload.Volumes = m.Disk.Volumes
			payload.ReportedAt = time.UnixMilli(m.Disk.ReportedAtMs).UTC().Format(time.RFC3339)
		}
		for _, h := range m.Held {
			payload.Held = append(payload.Held, &reliantv1.HeldWorktree{
				WorktreeId: h.WorktreeID, Name: h.Name, ProjectName: h.ProjectName, Path: h.Path,
				Reason: h.Reason, Detail: h.Detail, SizeBytes: h.SizeBytes,
				Removable: h.Removable, Cleaning: h.Cleaning,
			})
		}
		out = append(out, &reliantv1.InboxItem{
			Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE,
			ItemId:       storageItemID(m.DaemonID, m.Severity, m.Disk.GetPressureSinceMs()),
			WaitingSince: m.Since.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_Storage{Storage: payload},
		})
	}
	return out, nil
}

const (
	storageKeptSuffix = ":kept"
	storageDiskInfix  = ":disk:"
)

// storageItemID names a machine's storage item by what the user is told, so a
// dismissal holds for exactly that and no longer:
//
//	storage:<daemon>:kept                           kept workspaces; the disk is fine
//	storage:<daemon>:disk:<episode ms>:<severity>   one severity of one pressure episode
//
// Nothing else goes in the id. Another workspace kept or cleaned up, free
// space drifting, the machine going offline: none of it is a new problem, and
// an id that moved with the picture re-raised a dismissed item on every
// change, better or worse. Within an episode a dismissal covers its severity
// and every lower one (dismissalKeys), so only a climb toward failing builds
// raises the item again; the disk recovering and filling up again later is a
// new episode (diskwatch.Stamp), and a new item.
func storageItemID(daemonID string, sev diskwatch.Severity, episodeMs int64) string {
	if sev == diskwatch.SeverityNone {
		return inboxStoragePrefix + daemonID + storageKeptSuffix
	}
	return inboxStoragePrefix + daemonID + storageDiskInfix + strconv.FormatInt(episodeMs, 10) + ":" + sev.String()
}

// parseStorageDiskID is storageItemID's inverse for a disk item; ok is false
// for every other id, the kept item's included.
func parseStorageDiskID(id string) (daemonID string, episodeMs int64, sev diskwatch.Severity, ok bool) {
	rest, found := strings.CutPrefix(id, inboxStoragePrefix)
	if !found {
		return "", 0, 0, false
	}
	i := strings.LastIndex(rest, storageDiskInfix)
	if i <= 0 {
		return "", 0, 0, false
	}
	episode, name, found := strings.Cut(rest[i+len(storageDiskInfix):], ":")
	if !found {
		return "", 0, 0, false
	}
	episodeMs, err := strconv.ParseInt(episode, 10, 64)
	if err != nil {
		return "", 0, 0, false
	}
	for s := diskwatch.SeverityLow; s <= diskwatch.SeverityFailing; s++ {
		if s.String() == name {
			return rest[:i], episodeMs, s, true
		}
	}
	return "", 0, 0, false
}

// dismissalKeys returns the ids whose dismissal hides an item: its own, and
// for a disk storage item every higher severity of the same episode too — the
// user who dismissed the disk at "critical" must not hear about it again
// because it eased back to "low".
func dismissalKeys(itemID string) []string {
	daemonID, episodeMs, sev, ok := parseStorageDiskID(itemID)
	if !ok {
		return []string{itemID}
	}
	keys := make([]string, 0, int(diskwatch.SeverityFailing-sev)+1)
	for s := sev; s <= diskwatch.SeverityFailing; s++ {
		keys = append(keys, storageItemID(daemonID, s, episodeMs))
	}
	return keys
}

// impliedDismissals is what dismissing (or restoring) itemIDs records: the ids
// themselves plus, for each disk storage item, its machine's kept item. The
// disk item listed those workspaces; when the disk recovers they are what is
// left, and that is not news.
func impliedDismissals(itemIDs []string) []string {
	out := slices.Clone(itemIDs)
	for _, id := range itemIDs {
		if daemonID, _, _, ok := parseStorageDiskID(id); ok {
			if kept := storageItemID(daemonID, diskwatch.SeverityNone, 0); !slices.Contains(out, kept) {
				out = append(out, kept)
			}
		}
	}
	return out
}

const storageViewTTL = 60 * time.Second

type cachedStorageView struct {
	at       time.Time
	machines []*worktreesweep.MachineStorage
}

// storageView is StorageView with a short per-user cache: ListInbox backs the
// nav badge, which polls, and the storage picture changes on the scale of
// minutes. A clean-up or dismissal that must show at once calls dropStorageView.
func (s *Service) storageView(ctx context.Context, userID string) ([]*worktreesweep.MachineStorage, error) {
	if v, ok := s.storageCache.Load(userID); ok {
		if c := v.(cachedStorageView); time.Since(c.at) < storageViewTTL {
			return c.machines, nil
		}
	}
	machines, err := worktreesweep.StorageView(ctx, s.deps.Repo, userID)
	if err != nil {
		return nil, err
	}
	s.storageCache.Store(userID, cachedStorageView{at: time.Now(), machines: machines})
	return machines, nil
}

// CleanupStorage runs the storage item's Clean up action on one machine.
func (s *Service) CleanupStorage(
	ctx context.Context,
	req *connect.Request[reliantv1.CleanupStorageRequest],
) (*connect.Response[reliantv1.CleanupStorageResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("user ID not found in context"))
	}
	if req.Msg.DaemonId == "" || len(req.Msg.WorktreeIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("daemon_id and worktree_ids are required"))
	}
	if len(req.Msg.WorktreeIds) > maxInboxDismissBatch {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d worktrees per call", maxInboxDismissBatch))
	}
	if s.deps.Sweeper == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("storage cleanup is not available"))
	}
	// The machine must be the caller's: Cleanup only ever acts on worktrees of
	// projects the caller owns, but naming a stranger's daemon is rejected
	// outright rather than quietly doing nothing.
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil || daemon == nil || daemon.UserID != userID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("machine not found"))
	}

	accepted, skipped, err := s.deps.Sweeper.StartCleanup(ctx, s.deps.Repo, userID, req.Msg.DaemonId, req.Msg.WorktreeIds)
	if errors.Is(err, worktreesweep.ErrOffline) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("that machine is offline; reconnect it and try again"))
	}
	if err != nil {
		logging.ErrorContext(ctx, "Storage cleanup failed to start", "error", err, "daemonID", req.Msg.DaemonId)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("storage cleanup failed"))
	}

	s.storageCache.Delete(userID)
	resp := &reliantv1.CleanupStorageResponse{}
	for _, id := range accepted {
		resp.Results = append(resp.Results, &reliantv1.CleanupStorageResult{
			WorktreeId: id, Outcome: reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_ACCEPTED,
		})
	}
	for _, r := range skipped {
		resp.Results = append(resp.Results, &reliantv1.CleanupStorageResult{
			WorktreeId: r.WorktreeID, Outcome: reliantv1.CleanupStorageOutcome_CLEANUP_STORAGE_OUTCOME_SKIPPED, Message: r.Message,
		})
	}
	return connect.NewResponse(resp), nil
}

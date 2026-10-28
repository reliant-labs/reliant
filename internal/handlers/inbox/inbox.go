// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/activity"
	"github.com/reliant-labs/forge/pkg/svcerr"
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
	inboxNeedsSetupPrefix        = "automation_needs_setup:"
	inboxRecoveryPrefix          = "recovery:"
)

var inboxItemPrefixes = []string{
	inboxApprovalPrefix, inboxQuestionPrefix, inboxWaitingForMachinePrefix,
	inboxFailingPrefix, inboxLaunchFailedPrefix, inboxRunFinishedPrefix, inboxStoragePrefix,
	inboxNeedsSetupPrefix, inboxRecoveryPrefix,
}

// maxInboxDismissBatch bounds one Dismiss/Restore call ("Dismiss all" on a
// section). The list itself is capped at defaultInboxLimit.
const maxInboxDismissBatch = 500

// ListInbox returns everything waiting on the caller, optionally within one
// project. Pending rows come from one UNION ALL query; automation health reuses
// triggers.ComputeHealth over one batched firings query, so the cost does not
// grow with the number of triggers. The reads are independent and run
// together (readInbox); only the dismissal check waits for all of them.
//
// Scoping is applied after dismissal so the scoped response can also say how
// many items wait in the caller's other projects (one read, not two).
func (s *Service) ListInbox(
	ctx context.Context,
	req *connect.Request[reliantv1.ListInboxRequest],
) (*connect.Response[reliantv1.ListInboxResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	limit := defaultInboxLimit
	if req.Msg.Limit != nil {
		limit = int(*req.Msg.Limit)
		if limit < 0 {
			return nil, svcerr.InvalidArgument("limit must not be negative")
		}
	}

	projectID := strings.TrimSpace(req.Msg.GetProjectId())

	read, err := s.sharedRead(ctx, userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list inbox"))
	}
	items := make([]*reliantv1.InboxItem, 0, len(read.pending)+len(read.failing)+len(read.storage)+len(read.declarations))
	for _, p := range read.pending {
		items = append(items, inboxItemFromPending(p))
	}
	// The read may be shared with concurrent calls (sharedRead): this
	// response gets its own copies.
	for _, shared := range [][]*reliantv1.InboxItem{read.failing, read.storage, read.declarations, read.recovery} {
		for _, it := range shared {
			items = append(items, proto.CloneOf(it))
		}
	}
	items = dropSupersededByBroken(items)

	items, err = s.dropDismissed(ctx, userID, items)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list inbox"))
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
		if inboxItemBlocks(it) {
			resp.BlockingCount++
		} else {
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

// inboxReadConcurrency bounds how many of ONE ListInbox's reads hold a pool
// connection at a time. Running the independent reads together cuts a request
// to its longest chain (the triggers, then their firings) instead of the sum
// of about nine round trips, but a burst of requests multiplies whatever one
// request holds: at 05:22 on 2026-10-23 one client's ~360 ListInbox calls a
// minute held all 25 of the api-server's connections. Three keeps one request
// from taking more than a slice of the pool.
const inboxReadConcurrency = 3

// inboxReads is what readInbox gathers: everything ListInbox lists, before
// dismissals and scoping.
type inboxReads struct {
	pending []*core.InboxPending
	failing []*reliantv1.InboxItem
	storage []*reliantv1.InboxItem
	// declarations are the items read from each enabled activation's
	// declaration: needs-setup and broken (declarationItems).
	declarations []*reliantv1.InboxItem
	recovery     []*reliantv1.InboxItem
}

// inboxReadTimeout bounds a shared read (sharedRead), which runs detached
// from any one caller's cancellation.
const inboxReadTimeout = 15 * time.Second

// sharedRead is readInbox, shared by every ListInbox of the same user that
// arrives while one is in flight: the reads are the same whatever the scope
// or limit (those are applied after), so N concurrent calls — a list and its
// badge, several tabs, a reconnect replay — cost one set of queries and one
// slice of the pool, not N. The read runs on a context detached from the
// first caller's cancellation (so one caller giving up does not fail the
// others) with its own deadline. Callers only read the result.
func (s *Service) sharedRead(ctx context.Context, userID string) (*inboxReads, error) {
	v, err, _ := s.inboxReads.Do(userID, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inboxReadTimeout)
		defer cancel()
		return s.readInbox(rctx, userID)
	})
	if err != nil {
		return nil, err
	}
	return v.(*inboxReads), nil
}

// readLimiter hands out inboxReadConcurrency tokens, one per read holding a
// connection. A section that holds a token never asks for a second one, so the
// readers cannot deadlock on each other.
type readLimiter chan struct{}

func (l readLimiter) do(ctx context.Context, read func() error) error {
	select {
	case l <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-l }()
	return read()
}

// readInbox runs ListInbox's reads concurrently, at most inboxReadConcurrency
// at once. The pending rows and automation health are the inbox: either one
// failing fails the read. Storage and needs-setup are advisory and only warn.
func (s *Service) readInbox(ctx context.Context, userID string) (*inboxReads, error) {
	lim := make(readLimiter, inboxReadConcurrency)
	var out inboxReads
	g, gctx := errgroup.WithContext(ctx)
	// advisory logs an advisory read's failure, unless the read only failed
	// because a required one already had and cancelled it.
	advisory := func(msg string, err error) {
		if err != nil && gctx.Err() == nil {
			logging.WarnContext(ctx, msg, "error", err)
		}
	}

	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			out.pending, err = s.deps.Repo.ListInboxPending(gctx, userID)
			return err
		})
	})
	g.Go(func() error {
		// Both automation sections start from the caller's triggers; read
		// them once.
		var stored []*core.Trigger
		if err := lim.do(gctx, func() (err error) {
			stored, err = s.deps.Repo.ListTriggers(gctx, core.TriggerFilters{UserID: userID})
			return err
		}); err != nil {
			return err
		}
		tg, tctx := errgroup.WithContext(gctx)
		tg.Go(func() (err error) {
			out.failing, err = s.failingAutomations(tctx, lim, userID, stored)
			return err
		})
		tg.Go(func() error {
			declarations, err := s.declarationAutomations(tctx, lim, stored)
			// Advisory like storage: the badge and the other sections still work.
			advisory("Failed to compute needs-setup and broken automations for inbox", err)
			out.declarations = declarations
			return nil
		})
		return tg.Wait()
	})
	g.Go(func() error {
		err := lim.do(gctx, func() (err error) {
			out.storage, err = s.storageItems(gctx, userID)
			return err
		})
		// Disk state is advisory; a failure here must not blank the whole inbox.
		advisory("Failed to compute storage inbox items", err)
		return nil
	})
	// Recoveries are durable catalog entries, NOT machine storage observations.
	// A missing source row or disconnected/deleted daemon cannot hide them.
	g.Go(func() error {
		return lim.do(gctx, func() error {
			rows, err := s.deps.Repo.ListWorktreeRecoveries(gctx, userID, "")
			if err != nil {
				return err
			}
			for _, row := range rows {
				if row.Phase == "refs_deleted" {
					continue
				}
				out.recovery = append(out.recovery, recoveryInboxItem(row))
			}
			return nil
		})
	})
	// The group's FIRST error is the cause; reads that failed after it only
	// saw the cancellation it triggered.
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return &out, nil
}

// recoveryInboxItem intentionally requires no source/project/daemon joins.
func recoveryInboxItem(row *core.WorktreeRecovery) *reliantv1.InboxItem {
	detail := row.LastError
	for _, checkout := range row.Checkouts {
		if checkout.LastError != "" {
			if detail != "" {
				detail += "; "
			}
			detail += checkout.RepoID + ": " + checkout.LastError
		}
	}
	return &reliantv1.InboxItem{ItemId: inboxRecoveryPrefix + row.ID, Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_RECOVERY, ProjectId: row.ProjectID, WaitingSince: row.CreatedAt.UTC().Format(time.RFC3339Nano), Payload: &reliantv1.InboxItem_Recovery{Recovery: &reliantv1.RecoveryInboxItem{RecoveryId: row.ID, SourceWorktreeId: row.SourceWorktreeID, ProjectId: row.ProjectID, Phase: row.Phase, CheckoutCount: int32(len(row.Checkouts)), LastError: detail, Operation: row.Operation, OperationKey: row.OperationKey, TargetDaemonId: row.TargetDaemonID}}}
}

// inboxItemBlocks reports whether an item blocks a live run: an approval, a
// question a thread is parked on, a run waiting for its machine. An async
// question waits on the user but blocks nothing — its agent kept working.
func inboxItemBlocks(it *reliantv1.InboxItem) bool {
	switch it.Kind {
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_APPROVAL,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE:
		return true
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION:
		return !it.GetQuestion().GetAsync()
	default:
		return false
	}
}

// DismissInboxItem hides items from the caller's inbox. Hiding an approval or
// a question does not resolve it; the run still waits on it.
func (s *Service) DismissInboxItem(
	ctx context.Context,
	req *connect.Request[reliantv1.DismissInboxItemRequest],
) (*connect.Response[reliantv1.DismissInboxItemResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.DismissInboxItems(ctx, userID, impliedDismissals(req.Msg.ItemIds), time.Now().UTC()); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to dismiss inbox item"))
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
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if err := validateInboxItemIDs(req.Msg.ItemIds); err != nil {
		return nil, err
	}
	if err := s.deps.Repo.RestoreInboxItems(ctx, userID, impliedDismissals(req.Msg.ItemIds)); err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to restore inbox item"))
	}
	return connect.NewResponse(&reliantv1.RestoreInboxItemResponse{}), nil
}

func validateInboxItemIDs(itemIDs []string) error {
	if len(itemIDs) == 0 {
		return svcerr.InvalidArgument("item_ids is required")
	}
	if len(itemIDs) > maxInboxDismissBatch {
		return svcerr.InvalidArgument(fmt.Sprintf("at most %d item ids per call", maxInboxDismissBatch))
	}
	for _, id := range itemIDs {
		if !isInboxItemID(id) {
			return svcerr.InvalidArgument(fmt.Sprintf("not an inbox item id: %q", id))
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
// FAILING, using the same ComputeHealth as Trigger.health. Its three reads are
// independent and run together, each holding one of lim's tokens.
func (s *Service) failingAutomations(ctx context.Context, lim readLimiter, userID string, stored []*core.Trigger) ([]*reliantv1.InboxItem, error) {
	ids := make([]string, 0, len(stored))
	for _, t := range stored {
		if t.Enabled {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	var (
		recent, episodes map[string][]*core.TriggerEventWithRun
		regs             map[string]*core.TriggerRegistration
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			recent, err = s.deps.Repo.RecentTriggerOutcomes(gctx, userID, ids, triggers.HealthWindow)
			return err
		})
	})
	// The episode is read separately from the health window: a streak longer
	// than the window must still be one item with an honest count.
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			episodes, err = s.deps.Repo.FiringsSinceLastSuccess(gctx, userID, ids, triggers.EpisodeFirings)
			return err
		})
	})
	// A polled source that cannot authenticate never fires, so it has no
	// failed firing to show; its source state is what says it is failing.
	g.Go(func() error {
		return lim.do(gctx, func() (err error) {
			regs, err = s.deps.Repo.ListTriggerRegistrations(gctx, userID, ids)
			return err
		})
	})
	if err := g.Wait(); err != nil {
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

// declarationAutomations returns the items read from each enabled workflow
// automation's declaration, as it resolves NOW (the same rules Trigger.health
// reads):
//
//   - NEEDS_SETUP (triggers.NeedsSetupHealth): its params leave a required
//     input unset, so a workflow update or a legacy `{}` activation that the
//     person did not just touch still reaches them;
//   - BROKEN (a *triggers.DeclarationError): its workflow is gone or does not
//     parse, or no longer declares the trigger, or changed in a way it cannot
//     follow. Listed as AUTOMATION_FAILING with BROKEN health.
//
// Both are recomputed on every read from the stored workflows, so a synced
// .reliant/workflows change (a deleted file, a renamed trigger, a field this
// server does not know) reaches the owner on their next look, not on the
// activation's next fire, which for a weekly schedule or a rare webhook may
// be days away or never. Either item leaves the list the moment its cause is
// gone. A paused automation is the person's choice and not nagged about.
//
// Resolving a declaration may read the workflow store, one workflow after
// another, so the whole loop holds one of lim's tokens.
func (s *Service) declarationAutomations(ctx context.Context, lim readLimiter, stored []*core.Trigger) ([]*reliantv1.InboxItem, error) {
	var out []*reliantv1.InboxItem
	err := lim.do(ctx, func() error {
		out = s.declarationItems(ctx, stored)
		return nil
	})
	return out, err
}

func (s *Service) declarationItems(ctx context.Context, stored []*core.Trigger) []*reliantv1.InboxItem {
	workflows := triggers.NewCachedWorkflows(triggers.LaunchWorkflows{Repo: s.deps.Repo})
	var out []*reliantv1.InboxItem
	for _, t := range stored {
		if !t.Enabled || t.WorkflowTrigger == nil {
			continue
		}
		decl, err := triggers.ResolveDeclaration(ctx, workflows, t)
		var declErr *triggers.DeclarationError
		if errors.As(err, &declErr) {
			out = append(out, brokenAutomationItem(t, declErr))
			continue
		}
		if err != nil {
			continue // a store failure, not a verdict: the next read retries
		}
		issues := triggers.SetupIssues(decl, t)
		if len(issues) == 0 {
			continue
		}
		wire := make([]*reliantv1.TriggerSetupIssue, len(issues))
		inputs := make([]string, len(issues))
		for i, issue := range issues {
			wire[i] = &reliantv1.TriggerSetupIssue{Input: issue.Input, Message: issue.Message}
			inputs[i] = issue.Input
		}
		out = append(out, &reliantv1.InboxItem{
			Kind: reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_NEEDS_SETUP,
			// The missing inputs are part of the identity: dismissing "needs
			// repos" does not hide "now also needs team".
			ItemId:       inboxNeedsSetupPrefix + t.ID + ":" + strings.Join(inputs, ","),
			TriggerId:    t.ID,
			TriggerName:  t.Name,
			ProjectId:    t.ProjectID,
			ProjectName:  t.ProjectName,
			WorkflowName: t.Workflow,
			WaitingSince: t.UpdatedAt.UTC().Format(time.RFC3339Nano),
			Payload:      &reliantv1.InboxItem_AutomationNeedsSetup{AutomationNeedsSetup: &reliantv1.InboxAutomationNeedsSetup{Issues: wire}},
		})
	}
	return out
}

// brokenAutomationItem is the item for an enabled activation whose
// declaration cannot be used. The reason is part of its identity, so
// dismissing "the workflow no longer declares nightly" does not hide a later
// "its workflow does not resolve".
func brokenAutomationItem(t *core.Trigger, declErr *triggers.DeclarationError) *reliantv1.InboxItem {
	health := triggers.BrokenHealth(declErr)
	reason := sha256.Sum256([]byte(health.LastFailureDetail))
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING,
		ItemId:       inboxFailingPrefix + brokenItemInfix + t.ID + ":" + hex.EncodeToString(reason[:6]),
		TriggerId:    t.ID,
		TriggerName:  t.Name,
		ProjectId:    t.ProjectID,
		ProjectName:  t.ProjectName,
		WorkflowName: t.Workflow,
		WaitingSince: t.UpdatedAt.UTC().Format(time.RFC3339Nano),
		Payload:      &reliantv1.InboxItem_AutomationFailing{AutomationFailing: &reliantv1.InboxAutomationFailing{Health: health}},
	}
}

// brokenItemInfix follows inboxFailingPrefix on a broken automation's id.
const brokenItemInfix = "broken:"

// dropSupersededByBroken removes an automation's failing and could-not-start
// items when the same automation is listed as broken: every firing of a broken
// activation fails to launch, so those items restate the one cause the broken
// item names, with a less useful reason.
func dropSupersededByBroken(items []*reliantv1.InboxItem) []*reliantv1.InboxItem {
	broken := map[string]bool{}
	for _, it := range items {
		if strings.HasPrefix(it.ItemId, inboxFailingPrefix+brokenItemInfix) {
			broken[it.TriggerId] = true
		}
	}
	if len(broken) == 0 {
		return items
	}
	return slices.DeleteFunc(items, func(it *reliantv1.InboxItem) bool {
		if !broken[it.TriggerId] || strings.HasPrefix(it.ItemId, inboxFailingPrefix+brokenItemInfix) {
			return false
		}
		return it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_FAILING ||
			it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_LAUNCH_FAILED
	})
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
		q := &reliantv1.InboxQuestion{QuestionId: p.ItemKey, ThreadId: p.Text1, Prompt: firstQuestionPrompt(p.Text2), Async: p.Int1 == 1}
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
		return nil, svcerr.Unauthenticated("user ID not found in context")
	}
	if req.Msg.DaemonId == "" || len(req.Msg.WorktreeIds) == 0 {
		return nil, svcerr.InvalidArgument("daemon_id and worktree_ids are required")
	}
	if len(req.Msg.WorktreeIds) > maxInboxDismissBatch {
		return nil, svcerr.InvalidArgument(fmt.Sprintf("at most %d worktrees per call", maxInboxDismissBatch))
	}
	if s.deps.Sweeper == nil {
		return nil, svcerr.Unavailable("storage cleanup is not available")
	}
	// The machine must be the caller's: Cleanup only ever acts on worktrees of
	// projects the caller owns, but naming a stranger's daemon is rejected
	// outright rather than quietly doing nothing.
	daemon, err := s.deps.Repo.GetDaemon(ctx, req.Msg.DaemonId)
	if err != nil && !svcerr.IsNotFound(err) {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get machine"))
	}
	if err != nil || daemon == nil || daemon.UserID != userID {
		return nil, svcerr.NotFound("machine")
	}

	accepted, skipped, err := s.deps.Sweeper.StartCleanup(ctx, s.deps.Repo, userID, req.Msg.DaemonId, req.Msg.WorktreeIds)
	if errors.Is(err, worktreesweep.ErrOffline) {
		return nil, svcerr.FailedPrecondition("that machine is offline; reconnect it and try again")
	}
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("storage cleanup failed"))
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
	if len(accepted) == 0 {
		activity.Skip(ctx) // every worktree was skipped: nothing is being cleaned up
	}
	return connect.NewResponse(resp), nil
}

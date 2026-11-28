// Copyright (c) 2025 Reliant Labs
package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/diskwatch"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Item id prefixes, one per kind, so an id names what it dismisses. A failure
// id embeds the FIRST failing event of the current episode, so more failures
// stay one item (and one dismissal), while a new episode after a success is a
// new item. A waiting-for-machine id embeds the chat and when the block began,
// so a later block is a new item. Approval and question ids embed their row. Storage ids are described at storageItemID.
const (
	inboxApprovalPrefix          = "approval:"
	inboxQuestionPrefix          = "question:"
	inboxWaitingForMachinePrefix = "waiting_for_machine:"
	inboxFailingPrefix           = "automation_failing:"
	inboxLaunchFailedPrefix      = "automation_launch_failed:"
	inboxStoragePrefix           = "storage:"
	inboxNeedsSetupPrefix        = "automation_needs_setup:"
	inboxRecoveryPrefix          = "recovery:"
	inboxRunPausedPrefix         = "automation_run_paused:"
	// A broken definition's id is its version's identity (project, machine,
	// file, content hash): another firing of it is the same item, a new
	// broken version is a new one.
	inboxDefinitionBrokenPrefix = "automation_definition_broken:"
	// A worktree-reclaim id names the machine and the set of proposals it
	// asks about (reclaimItemID).
	inboxWorktreeReclaimPrefix = "worktree_reclaim:"
	// A marked worktree's id names the worktree and the mark: a new mark
	// after a cancel is a new item (markedItemID).
	inboxWorktreeMarkedPrefix = "worktree_marked:"
	// A chat asking before it uses the person's own AI account in place of
	// its org's: the chat and when it asked.
	inboxPersonalFundingPrefix = "personal_funding:"
)

var inboxItemPrefixes = []string{
	inboxApprovalPrefix, inboxQuestionPrefix, inboxWaitingForMachinePrefix,
	inboxFailingPrefix, inboxLaunchFailedPrefix, inboxStoragePrefix,
	inboxNeedsSetupPrefix, inboxRecoveryPrefix, inboxRunPausedPrefix, inboxDefinitionBrokenPrefix,
	inboxWorktreeReclaimPrefix, inboxWorktreeMarkedPrefix, inboxPersonalFundingPrefix, inboxOrgKeyRejectedPrefix,
	inboxNoOrgAccountPrefix, inboxOnPersonalAccountsPrefix,
}

// maxInboxDismissBatch bounds one Dismiss/Restore call ("Dismiss all" on a
// section). The list itself is capped at defaultInboxLimit.
const maxInboxDismissBatch = 500

// inboxReadConcurrency bounds how many of ONE ListInbox's reads hold a pool
// connection at a time. Running the independent reads together cuts a request
// to its longest chain (the triggers, then their firings) instead of the sum
// of about nine round trips, but a burst of requests multiplies whatever one
// request holds: at 05:22 on 2026-10-23 one client's ~360 ListInbox calls a
// minute held all 25 of the api-server's connections. Three keeps one request
// from taking more than a slice of the pool.
const inboxReadConcurrency = 3

// inboxHealthReadConcurrency is the separate budget of failingAutomations'
// reads (recent outcomes, the failure episodes, registrations), so they never
// queue behind the reads of inboxReadConcurrency. With one shared budget they
// waited for whichever token the pending read (2.6s) and the declaration loop
// (2.2s) left free and ran one after the other: 1.06s and 1.03s, at +520ms
// and +1718ms, in prod trace ed2301a0… (2026-11-18).
//
// One read set may now hold 3+2 = 5 of an api-server's 25 connections, a
// fifth. That is safe against the 2026-10-23 exhaustion because what
// exhausted the pool was N concurrent ListInbox calls of one client, each
// holding a read set of its own; sharedRead, which landed with the budget
// above, makes those at most two read sets per user and credential (one in
// flight, one queued behind it), so a burst no longer multiplies it. The pool's two exhaustions since (2026-11-18 08:30Z
// and 11:40Z) were stream fan-outs, not the inbox. Two tokens let the two
// firing reads run together.
const inboxHealthReadConcurrency = 2

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
	// orgKeys are the rejected API keys of the orgs the caller administers.
	orgKeys []*reliantv1.InboxItem
	// automationNotices are the org-provider-accounts §11 notices: automations
	// paused for want of an org account, and the caller's automations still
	// on their own accounts in a team org.
	automationNotices []*reliantv1.InboxItem
	// orgs is the org of each chat, project ("chat:<id>", "project:<id>")
	// and trigger ("trigger:<id>") the items are about, nil for one with no
	// org yet: what ListInbox's org scope decides an item by (itemOrg).
	orgs map[string]*string
}

// sharedReadKey is the class of callers whose reads are interchangeable: the
// same user, through the same kind of credential (only a person hears of an
// org's rejected keys, orgKeyItems) bound to the same org (a credential bound
// to an org reads only that org's rows, authz.WithCredentialOrg). Anything
// readInbox decides by beyond the user belongs here.
func sharedReadKey(ctx context.Context, userID string) string {
	bound, _, _ := authz.CredentialRefusal(ctx)
	return userID + "\x00" + strconv.Itoa(int(auth.CredentialFromContext(ctx))) + "\x00" + bound
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
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_WAITING_FOR_MACHINE,
		reliantv1.InboxItemKind_INBOX_ITEM_KIND_PERSONAL_FUNDING:
		return true
	case reliantv1.InboxItemKind_INBOX_ITEM_KIND_QUESTION:
		return !it.GetQuestion().GetAsync()
	default:
		return false
	}
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

// brokenDefinitions gathers one AUTOMATION_DEFINITION_BROKEN item per broken
// version of a workflow file, in the order first seen.
type brokenDefinitions struct {
	byKey map[string]*reliantv1.InboxItem
	items []*reliantv1.InboxItem
}

func (b *brokenDefinitions) add(t *core.Trigger, def *core.TriggerEventDefinition) {
	key := sha256.Sum256([]byte(strings.Join([]string{t.ProjectID, def.DaemonID, def.Path, def.ContentHash}, "\x00")))
	id := inboxDefinitionBrokenPrefix + hex.EncodeToString(key[:12])
	since := t.UpdatedAt.UTC().Format(time.RFC3339Nano)
	refused := &reliantv1.InboxRefusedAutomation{TriggerId: t.ID, TriggerName: t.Name}
	if it := b.byKey[id]; it != nil {
		payload := it.GetAutomationDefinitionBroken()
		payload.Automations = append(payload.Automations, refused)
		if since < it.WaitingSince {
			it.WaitingSince = since
		}
		return
	}
	it := &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_DEFINITION_BROKEN,
		ItemId:       id,
		ProjectId:    t.ProjectID,
		ProjectName:  t.ProjectName,
		WorkflowName: t.Workflow,
		WaitingSince: since,
		Payload: &reliantv1.InboxItem_AutomationDefinitionBroken{AutomationDefinitionBroken: &reliantv1.InboxAutomationDefinitionBroken{
			Definition:  triggers.DefinitionToProto(def),
			Automations: []*reliantv1.InboxRefusedAutomation{refused},
		}},
	}
	b.byKey[id] = it
	b.items = append(b.items, it)
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
	case core.InboxPendingPersonalFunding:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_PERSONAL_FUNDING
		item.ItemId = inboxPersonalFundingPrefix + p.ItemKey
	case core.InboxPendingRunPaused:
		item.Kind = reliantv1.InboxItemKind_INBOX_ITEM_KIND_AUTOMATION_RUN_PAUSED
		item.ItemId = inboxRunPausedPrefix + p.ItemKey
		item.Payload = &reliantv1.InboxItem_AutomationRunPaused{AutomationRunPaused: &reliantv1.InboxAutomationRunPaused{
			Reason: p.Text1,
		}}
	}
	return item
}

func inboxApproval(p *core.InboxPending) *reliantv1.InboxApproval {
	return &reliantv1.InboxApproval{
		ApprovalId:   p.ItemKey,
		ApprovalType: reliantv1.ApprovalType(p.Int1),
		Title:        p.Text1,
		BatchId:      p.Text2,
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

const (
	storageKeptSuffix  = ":kept"
	storageDiskInfix   = ":disk:"
	storageProcsInfix  = ":procs:"
	maxTitledProcTrees = 5
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

// itemReads decides inbox items through authz, reading each chat and project
// once per inbox read: an item about a chat or project the caller may no
// longer read (an org they have left, a credential's other org) is not
// listed. An item of no org (its project is gone: a recovery outlives its
// source by design; or it names none) is decided as authz decides a row of
// no org: the owner's, reached by a credential only through its owner's
// default org.
type itemReads struct {
	s        *Service
	mu       sync.Mutex
	chats    map[string]bool
	projects map[string]bool
	// orgs is the org of each chat and project read ("chat:<id>",
	// "project:<id>"), nil for one with no org yet or a project that is gone.
	orgs map[string]*string
}

func (s *Service) readableItem() *itemReads {
	return &itemReads{s: s, chats: map[string]bool{}, projects: map[string]bool{}, orgs: map[string]*string{}}
}

func (r *itemReads) chat(ctx context.Context, userID, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ok, seen := r.chats[id]
	if !seen {
		chat, err := r.s.deps.Chats.GetChat(ctx, id)
		ok = err == nil && authz.CanReadChat(ctx, userID, chat)
		r.chats[id] = ok
		if err == nil {
			r.orgs["chat:"+id] = chat.OrgID
		}
	}
	return ok
}

func (r *itemReads) project(ctx context.Context, userID, id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ok, seen := r.projects[id]
	if !seen {
		project, err := r.s.deps.ProjectRows.GetProject(ctx, id)
		switch {
		case svcerr.IsNotFound(err):
			ok = authz.CanReadOwn(ctx, userID, nil)
			r.orgs["project:"+id] = nil
		default:
			ok = err == nil && authz.CanReadProject(ctx, userID, project)
			if err == nil {
				r.orgs["project:"+id] = project.OrgID
			}
		}
		r.projects[id] = ok
	}
	return ok
}

func (r *itemReads) pending(ctx context.Context, userID string, p *core.InboxPending) bool {
	switch {
	case p.ChatID != "":
		return r.chat(ctx, userID, p.ChatID)
	case p.ProjectID != "":
		return r.project(ctx, userID, p.ProjectID)
	}
	return authz.CanReadOwn(ctx, userID, nil)
}

// incident requires live authority over both the chat and its project, even
// when its trigger has been deleted.
func (r *itemReads) incident(ctx context.Context, userID string, a *core.AttentionIncident) bool {
	if a.ChatID == "" || a.ProjectID == "" {
		return false
	}
	return r.chat(ctx, userID, a.ChatID) && r.project(ctx, userID, a.ProjectID)
}

func (r *itemReads) recovery(ctx context.Context, userID string, row *core.WorktreeRecovery) bool {
	if row.ProjectID == "" {
		return authz.CanReadOwn(ctx, userID, nil)
	}
	return r.project(ctx, userID, row.ProjectID)
}

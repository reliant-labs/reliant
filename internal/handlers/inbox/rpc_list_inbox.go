package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/worktreesweep"
)

const defaultInboxLimit = 100

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
	scope, err := s.listScope(ctx, userID, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	read, err := s.sharedRead(withCallerJWT(ctx, auth.BearerToken(req.Header().Get("Authorization"))), userID)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list inbox"))
	}
	items := make([]*reliantv1.InboxItem, 0, len(read.pending)+len(read.failing)+len(read.storage)+len(read.declarations))
	for _, p := range read.pending {
		items = append(items, inboxItemFromPending(p))
	}
	// The read may be shared with concurrent calls (sharedRead): this
	// response gets its own copies.
	for _, shared := range [][]*reliantv1.InboxItem{read.failing, read.storage, read.declarations, read.recovery, read.orgKeys, read.automationNotices} {
		for _, it := range shared {
			items = append(items, proto.CloneOf(it))
		}
	}
	items = dropSupersededByBroken(items)

	items, err = s.dropDismissed(ctx, userID, items)
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list inbox"))
	}

	items = read.inScope(items, scope)

	resp := &reliantv1.ListInboxResponse{}
	if projectID != "" {
		scoped := items[:0]
		for _, it := range items {
			// A machine's storage, and its worktrees, are not any project's:
			// they show in every scope.
			if it.ProjectId == projectID || it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE ||
				it.Kind == reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM {
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

// inboxReadTimeout bounds a shared read (sharedRead), which runs detached
// from any one caller's cancellation.
const inboxReadTimeout = 15 * time.Second

// sharedRead is readInbox, shared by the ListInbox calls of the same user that
// arrive while one is in flight: the reads are the same whatever the scope
// or limit (those are applied after), so N concurrent calls — a list and its
// badge, several tabs, a reconnect replay — cost at most two sets of queries
// and slices of the pool, not N. A call joins only a read that starts after
// it arrived (freshFlight), never one already in flight: the refetch after
// answering a question must not get the list from before the answer. The
// read runs on a context detached from any caller's cancellation (so one
// caller giving up does not fail the others) with its own deadline. Callers
// only read the result.
//
// The read is shared only within one authorization class (sharedReadKey): what
// it returns depends on the credential as well as the user, so a session's
// read is never handed to an access token's call, nor the reverse.
func (s *Service) sharedRead(ctx context.Context, userID string) (*inboxReads, error) {
	return s.inboxReads.Do(ctx, sharedReadKey(ctx, userID), func() (*inboxReads, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inboxReadTimeout)
		defer cancel()
		return s.readInbox(rctx, userID)
	})
}

// readInbox runs ListInbox's reads concurrently, at most inboxReadConcurrency
// at once plus inboxHealthReadConcurrency of automation health. The pending
// rows and automation health are the inbox: either one failing fails the
// read. Storage and needs-setup are advisory and only warn.
func (s *Service) readInbox(ctx context.Context, userID string) (*inboxReads, error) {
	lim := make(readLimiter, inboxReadConcurrency)
	healthLim := make(readLimiter, inboxHealthReadConcurrency)
	var out inboxReads
	// One authz reader for the pending and recovery sections, so each chat
	// and project is read once, and its org kept for the org scope.
	reads := s.readableItem()
	var triggerOrgs map[string]*string
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
			out.pending, err = s.deps.Items.ListInboxPending(gctx, userID)
			out.pending = authz.Visible(gctx, userID, out.pending, reads.pending)
			return err
		})
	})
	g.Go(func() error {
		// Both automation sections start from the caller's triggers; read
		// them once.
		var stored []*core.Trigger
		if err := lim.do(gctx, func() (err error) {
			stored, err = s.deps.Notices.ListTriggersForUser(gctx, core.TriggerFilters{UserID: userID})
			// Automation health is the triggers the caller may read: none of
			// an org they have left.
			stored = authz.Visible(gctx, userID, stored, authz.CanReadTrigger)
			return err
		}); err != nil {
			return err
		}
		triggerOrgs = make(map[string]*string, len(stored))
		for _, t := range stored {
			triggerOrgs["trigger:"+t.ID] = t.OrgID
		}
		tg, tctx := errgroup.WithContext(gctx)
		tg.Go(func() (err error) {
			out.failing, err = s.failingAutomations(tctx, healthLim, reads, userID, stored)
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
	g.Go(func() error {
		err := lim.do(gctx, func() (err error) {
			out.orgKeys, err = s.orgKeyItems(gctx, userID)
			return err
		})
		// Advisory: an admin also sees a key's state in the org's settings.
		advisory("Failed to list the rejected keys of the caller's orgs for inbox", err)
		return nil
	})
	g.Go(func() error {
		err := lim.do(gctx, func() (err error) {
			out.automationNotices, err = s.automationNoticeItems(gctx, userID)
			return err
		})
		// Advisory: each automation shows its own pause and funding.
		advisory("Failed to list the org account notices of the caller's automations for inbox", err)
		return nil
	})
	// Recoveries are durable catalog entries, NOT machine storage observations.
	// A missing source row or disconnected/deleted daemon cannot hide them.
	g.Go(func() error {
		return lim.do(gctx, func() error {
			rows, err := s.deps.Recoveries.ListWorktreeRecoveries(gctx, userID, "")
			if err != nil {
				return err
			}
			rows = authz.Visible(gctx, userID, rows, reads.recovery)
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
	for k, v := range triggerOrgs {
		reads.orgs[k] = v
	}
	// Read the org of every item the sections built without an authz read
	// of their own (automation health, broken definitions), so ListInbox's
	// org scope decides each one by what it is about, never by a key nobody
	// read. Before the read is returned: it may be shared (sharedRead).
	// Storage carries the person's marked worktrees (markedItems), each about
	// a project: its org is read here too, so an org scope lists only its own.
	// A machine's own storage items name nothing and stay in every org.
	for _, section := range [][]*reliantv1.InboxItem{out.failing, out.declarations, out.recovery, out.storage} {
		for _, it := range section {
			reads.resolveOrg(ctx, userID, it)
		}
	}
	out.orgs = reads.orgs
	return &out, nil
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
// gone. A paused automation is the person's choice and not nagged about; one
// a project move paused is listed until it is set up again (movePausedItem).
//
// Resolving a declaration may read the workflow store, one workflow after
// another, so the whole loop holds one of lim's tokens.
//
// Resolution validates each workflow tree (launch memoizes the verdict), which
// is CPU, not queries: the span names that time, which a trace otherwise shows
// only as a gap between catalog reads.
func (s *Service) declarationAutomations(ctx context.Context, lim readLimiter, stored []*core.Trigger) ([]*reliantv1.InboxItem, error) {
	var out []*reliantv1.InboxItem
	err := lim.do(ctx, func() error {
		ctx, span := otel.Tracer("reliant.inbox").Start(ctx, "inbox.declarationItems",
			trace.WithAttributes(attribute.Int("inbox.triggers", len(stored))))
		defer span.End()
		out = s.declarationItems(ctx, stored)
		span.SetAttributes(attribute.Int("inbox.items", len(out)))
		return nil
	})
	return out, err
}

func (s *Service) declarationItems(ctx context.Context, stored []*core.Trigger) []*reliantv1.InboxItem {
	// Every activation resolves its workflow, its children and presets, mostly
	// against the same machine catalogs and account drafts: read each once
	// for the whole list (launch.NewRequestResolver), as ListTriggers does.
	// Prod ListInbox (2026-11-05, 22 activations): 13 catalog and 24 draft
	// reads a call, p50 516ms, the most-called slow rpc of the web.
	resolver := launch.NewRequestResolver(s.deps.Repo)
	// Only enabled activations are resolved below, so only their drafts are
	// worth reading together.
	triggers.PrefetchActivationDrafts(ctx, resolver, slices.DeleteFunc(slices.Clone(stored), func(t *core.Trigger) bool { return !t.Enabled }), "inbox")
	workflows := triggers.NewCachedWorkflows(triggers.LaunchWorkflows{Repo: resolver})
	var out []*reliantv1.InboxItem
	broken := &brokenDefinitions{byKey: map[string]*reliantv1.InboxItem{}}
	for _, t := range stored {
		if item := movePausedItem(t); item != nil {
			out = append(out, item)
			continue
		}
		if !t.Enabled {
			continue
		}
		// A broken version of a workflow file is ONE item however many
		// automations (declared or hand-made) it refuses, and it replaces
		// their own broken items: they all restate its one cause.
		if def, err := triggers.BrokenDefinition(ctx, workflows, t); err == nil && def != nil {
			broken.add(t, def)
			continue
		}
		if t.WorkflowTrigger == nil {
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
	return append(out, broken.items...)
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
		// A broken definition supersedes every automation it refuses.
		for _, a := range it.GetAutomationDefinitionBroken().GetAutomations() {
			broken[a.TriggerId] = true
		}
	}
	if len(broken) == 0 {
		return items
	}
	return slices.DeleteFunc(items, func(it *reliantv1.InboxItem) bool {
		// A broken declaration does not resolve an earlier job's incident.
		if it.GetAutomationFailing().GetIncidentKey() != "" {
			return false
		}
		if it.TriggerId == "" || !broken[it.TriggerId] || strings.HasPrefix(it.ItemId, inboxFailingPrefix+brokenItemInfix) {
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
	dismissed, err := s.deps.Items.ListDismissedInboxItemIDs(ctx, userID, candidates)
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

// storageItems returns one item per machine with a volume under disk pressure
// or archived worktrees it declined to remove. Storage items are not tied to a
// project, so they appear in every project scope.
// storageItems is every machine's storage items, and the user's marked
// worktrees (markedItems).
func (s *Service) storageItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
	out, err := s.machineStorageItems(ctx, userID)
	if err != nil {
		return nil, err
	}
	marked, err := s.markedItems(ctx, userID)
	if err != nil {
		return nil, err
	}
	return append(out, marked...), nil
}

func (s *Service) machineStorageItems(ctx context.Context, userID string) ([]*reliantv1.InboxItem, error) {
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
		if m.Processes != nil {
			out = append(out, s.processesItem(ctx, userID, m, names[m.DaemonID]))
		}
		if it := reclaimItem(m, names[m.DaemonID]); it != nil {
			out = append(out, it)
		}
		if !m.StorageItem() {
			continue
		}
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
				Removable: h.Removable, Cleaning: h.Cleaning, Forgettable: h.Forgettable,
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

// processesItem is a machine's "near its process limit" item: the daemon is
// refusing new background heavy commands, and the item names the largest
// process trees by the agent that started each, so the one at fault can be
// stopped. One item per refusal episode (storage:<daemon>:procs:<since ms>):
// dismissing it holds until the machine recovers and gets there again.
func (s *Service) processesItem(ctx context.Context, userID string, m *worktreesweep.MachineStorage, name string) *reliantv1.InboxItem {
	procs := proto.Clone(m.Processes).(*reliantv1.ProcessReport)
	for i, t := range procs.Top {
		if i == maxTitledProcTrees || t.ChatId == "" {
			continue
		}
		// Only the caller's own chats are named: the id came from a machine,
		// and a title is not the machine's to reveal.
		if c, err := s.deps.Chats.GetChat(ctx, t.ChatId); err == nil && authz.CanReadChat(ctx, userID, c) {
			t.ChatTitle = c.Title
		}
	}
	payload := &reliantv1.InboxStorage{
		DaemonId:   m.DaemonID,
		DaemonName: name,
		Online:     m.Online,
		Processes:  procs,
	}
	since := procs.RefusingSinceMs
	if m.Disk != nil {
		payload.ReportedAt = time.UnixMilli(m.Disk.ReportedAtMs).UTC().Format(time.RFC3339)
		if since == 0 {
			since = m.Disk.ReportedAtMs
		}
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_STORAGE,
		ItemId:       inboxStoragePrefix + m.DaemonID + storageProcsInfix + strconv.FormatInt(since, 10),
		WaitingSince: time.UnixMilli(since).UTC().Format(time.RFC3339Nano),
		Payload:      &reliantv1.InboxItem_Storage{Storage: payload},
	}
}

// reclaimItem is a machine's worktree-reclaim item: the proposals its daemon
// last reported that nobody has decided yet, with any already approved
// (shown as such until the machine reports them removed; one it refused to
// remove is undecided again, with why). Declined ones are not asked about again; a
// declined worktree that changes is a new proposal. nil when nothing is
// undecided.
func reclaimItem(m *worktreesweep.MachineStorage, name string) *reliantv1.InboxItem {
	wr := m.Reclaim
	if wr == nil || m.DaemonID == "" {
		return nil
	}
	payload := &reliantv1.InboxWorktreeReclaim{DaemonId: m.DaemonID, DaemonName: name, Online: m.Online, Mode: wr.Mode, Total: wr.Total}
	if m.Disk != nil {
		payload.ReportedAt = time.UnixMilli(m.Disk.ReportedAtMs).UTC().Format(time.RFC3339)
	}
	var undecided []*reliantv1.WorktreeReclaimProposal
	var since int64
	for _, p := range wr.Proposals {
		switch p.Decision {
		case "":
			undecided = append(undecided, p)
			if since == 0 || (p.ProposedAtMs > 0 && p.ProposedAtMs < since) {
				since = p.ProposedAtMs
			}
		case "approve":
		default:
			continue
		}
		payload.Proposals = append(payload.Proposals, proto.CloneOf(p))
	}
	if len(undecided) == 0 {
		return nil
	}
	if since == 0 {
		since = wr.PassFinishedAtMs
	}
	return &reliantv1.InboxItem{
		Kind:         reliantv1.InboxItemKind_INBOX_ITEM_KIND_WORKTREE_RECLAIM,
		ItemId:       reclaimItemID(m.DaemonID, undecided, reclaimEpisode(m)),
		WaitingSince: time.UnixMilli(since).UTC().Format(time.RFC3339Nano),
		Payload:      &reliantv1.InboxItem_WorktreeReclaim{WorktreeReclaim: payload},
	}
}

// reclaimItemID names a machine's worktree-reclaim item by the proposals it
// asks about, worktree_reclaim:<daemon>:<digest of their keys and
// fingerprints>. The same undecided set is one item however often the
// machine reports it, so a dismissal holds for it; a new proposal, or a
// worktree whose state changed, is a new set and a new item.
//
// While the machine's disk is under pressure, or its disk guard is acting,
// the episode's start is part of the id too: a dismissed set is asked about
// again once per episode in which the machine is running out of space, when
// what it holds is what would free it. Before, one dismissal hid the
// proposals for good while their state held still: on 2026-11-26 a build
// machine at 93% reported 127 proposals (45 GB) that no Inbox showed.
func reclaimItemID(daemonID string, undecided []*reliantv1.WorktreeReclaimProposal, episodeMs int64) string {
	keys := make([]string, 0, len(undecided))
	for _, p := range undecided {
		keys = append(keys, p.Key+"\x00"+p.Fingerprint)
	}
	sort.Strings(keys)
	joined := strings.Join(keys, "\n")
	if episodeMs > 0 {
		joined += "\nepisode:" + strconv.FormatInt(episodeMs, 10)
	}
	sum := sha256.Sum256([]byte(joined))
	return inboxWorktreeReclaimPrefix + daemonID + ":" + hex.EncodeToString(sum[:8])
}

// reclaimEpisode is the start of the machine's current low-disk episode: its
// pressure episode, else its disk guard's, else 0 (the disk is fine).
func reclaimEpisode(m *worktreesweep.MachineStorage) int64 {
	if since := m.Disk.GetPressureSinceMs(); since > 0 {
		return since
	}
	return m.Disk.GetGuardSinceMs()
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
	machines, err := worktreesweep.StorageView(ctx, s.deps.Storage, userID)
	if err != nil {
		return nil, err
	}
	s.storageCache.Store(userID, cachedStorageView{at: time.Now(), machines: machines})
	return machines, nil
}

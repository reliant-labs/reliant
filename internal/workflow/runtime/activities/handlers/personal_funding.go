// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/orgfunding"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// This file is §10 of control-plane docs/design/org-provider-accounts.md on
// the server: before a chat in an org moves from the org's accounts to the
// person's own, it asks, and with nobody to answer it waits. The decision is
// made here, at the activity edge, from server-side state only (the person's
// provider.org_funding setting, the chat's funding order, the chat's question
// row): never from a client, so nothing an older client sends can spend the
// person's money.
//
// The three cases:
//
//  1. limited: the org's accounts that serve the model are all rate-limited
//     and a person's own account could serve now (fundingOnLimit, from the
//     usage-limit path);
//  2. unusable: the thread was on an org-funded account that is no longer a
//     candidate (revoked, rejected, paused, a grant withdrawn);
//  3. unserved: the person has org-funded accounts in the org, but none serves
//     the model the turn resolved to.
//
// Cases 2 and 3 are found after routing, before any provider call
// (checkPersonalFunding). Under "ask" (the default) the chat's one question
// is recorded and the turn waits; under "wait" it waits without asking; under
// "use_personal" a funding lease is granted and the turn goes on, with a
// notice. Every wait is the usage-limit wait the workflow already has, so an
// older client renders it as a wait and its Resume keeps waiting.

// PersonalFundingStore is what the routing reads and writes of a chat's
// question (core.PersonalFunding). *db.Repo satisfies it.
type PersonalFundingStore interface {
	GetChatPersonalFunding(ctx context.Context, chatID string) (*core.PersonalFunding, error)
	AskPersonalFunding(ctx context.Context, q core.PersonalFunding, now time.Time) (bool, error)
	GrantPersonalFunding(ctx context.Context, q core.PersonalFunding, now time.Time) error
	ClearPersonalFunding(ctx context.Context, chatID string) (bool, error)
	MarkPersonalFundingAnnounced(ctx context.Context, chatID string) (bool, error)
	ProviderAccountOrgs(ctx context.Context, accountID string) (ownerOrg, attributedOrg string, found bool, err error)
	ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error)
	LatestServingAccount(ctx context.Context, threadID string) (string, error)
	// ThreadFunding is who paid for the thread's calls on its current
	// account (thread_funding), and the run's org: the provenance a replaced
	// or deleted account no longer carries, per thread.
	ThreadFunding(ctx context.Context, threadID string) (funding, orgID, accountID string, found bool, err error)
	// RecordThreadFunding records who pays for the thread's calls from now on.
	RecordThreadFunding(ctx context.Context, threadID, orgID, funding, accountID string) error
	// ChatFundingOrder is the order a person chose for the chat
	// (UpdateChatProviderPolicy, person-only); "" when it inherits.
	ChatFundingOrder(ctx context.Context, chatID string) (string, error)
	// ChatPersonPickDriver is the provider the chat's owner picked for its
	// root through the composer, signed in as a person, in orgID, while the
	// chat's selection still holds that pick; "" otherwise.
	ChatPersonPickDriver(ctx context.Context, chatID, orgID string) (string, error)
}

// fundingNow is the clock of the funding decisions; a variable for tests.
var fundingNow = time.Now

// fundingPlan is what one call of a chat in an org knows about who may pay:
// the person's choice for the org, the chat's question, and whether the org
// gave the person anything at all.
type fundingPlan struct {
	// active is false for a call that names no org or no chat: nothing here
	// applies, the call routes as before.
	active bool
	// unchecked is a call of a chat in an org whose consent cannot be read
	// or recorded (a worker built without the question store): it never
	// spends the person's own account, it waits.
	unchecked bool
	order     orgfunding.Order
	limit     orgfunding.OnOrgLimit
	// question is the chat's row, nil when it has none.
	question *core.PersonalFunding
	// orgGave reports that the scope holds at least one org-funded account
	// of the first tier: the org's key or the person's seat in it.
	orgGave bool
	// leased is true when a use_personal answer reorders this call to the
	// person's own accounts first.
	leased bool
	// personPick is the provider the person picked for the chat's root
	// (ChatPersonPickDriver), read only for an attended call of the root
	// thread; "" otherwise.
	personPick string
}

// orgFundedFirstTier reports whether a is paid for by the org and preferred.
func orgFundedFirstTier(a models.AccountConfig) bool {
	return a.Tier == models.TierFirst && models.OrgFunded(a.Funding)
}

// fundingPlanFor reads the plan of spec's call over avail.
func (r *AccountRouting) fundingPlanFor(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers) fundingPlan {
	if r == nil || spec.OrgID == "" || spec.ChatID == "" {
		return fundingPlan{}
	}
	plan := fundingPlan{active: true}
	for _, list := range avail.Accounts {
		for _, a := range list {
			if orgFundedFirstTier(a) {
				plan.orgGave = true
			}
		}
	}
	pref := orgfunding.Default()
	if r.Settings != nil {
		if v, ok := r.Settings.GetSettingValue(ctx, spec.UserID, orgfunding.SettingKey); ok {
			pref = orgfunding.Parse(v).For(spec.OrgID)
		}
	}
	plan.order, plan.limit = pref.Order, pref.OnOrgLimit
	if !spec.Attended && spec.UnattendedAnySource {
		// An automation that may use any source has nobody to ask: its
		// owner's stamp is the consent (org-provider-accounts §11.3).
		plan.limit = orgfunding.OnOrgLimitUsePersonal
	}
	if r.Funding == nil {
		// Consent can be neither read nor recorded: fail closed.
		plan.unchecked = true
		plan.order = orgfunding.OrderOrgOnly
		return plan
	}
	// The chat's own order, which only a person sets, outranks their per-org
	// setting. It is read here, on the server: the copy the run carries in
	// __provider_policy is one a workflow, a preset or a spawn can also write.
	if chatOrder, err := r.Funding.ChatFundingOrder(ctx, spec.ChatID); err != nil {
		// The chat may restrict spending (org_only): unreadable, it is taken
		// as restricting, so the call waits rather than spending (fail closed).
		logging.WarnContext(ctx, "Could not read the chat's funding order; using only the org's accounts", "chatID", spec.ChatID, "error", err)
		plan.order = orgfunding.OrderOrgOnly
		plan.limit = orgfunding.OnOrgLimitWait
	} else if o := orgfunding.ParseOrder(chatOrder); o != "" {
		plan.order = o
	}
	// The run's order (a preset's or workflow's providers:, a spawn's
	// policy) may only tighten what the person chose, never grant spending:
	// org_only always holds, org_first undoes a personal_first, and a
	// personal_first from a run is no consent.
	switch orgfunding.ParseOrder(spec.Failover.Funding) {
	case orgfunding.OrderOrgOnly:
		plan.order = orgfunding.OrderOrgOnly
	case orgfunding.OrderOrgFirst:
		if plan.order == orgfunding.OrderPersonalFirst {
			plan.order = orgfunding.OrderOrgFirst
		}
	}
	q, err := r.Funding.GetChatPersonalFunding(ctx, spec.ChatID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the chat's funding question; deciding without it", "chatID", spec.ChatID, "error", err)
	}
	if q != nil && (q.UserID != spec.UserID || q.OrgID != spec.OrgID) {
		// A question, or a lease, is one person's answer about one org's
		// accounts: it is never honoured for another person or in another
		// org (a chat whose project moved), and the next ask replaces it.
		q = nil
	}
	plan.question = q
	// The person's own pick of the root's provider is consent for the
	// root's attended calls only: never a sub-agent's, never a run with
	// nobody behind it. Unreadable, there is no pick, and the call asks.
	if spec.Attended && !spec.Descendant {
		pick, err := r.Funding.ChatPersonPickDriver(ctx, spec.ChatID, spec.OrgID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read the person's pick for the chat; asking", "chatID", spec.ChatID, "error", err)
		}
		plan.personPick = pick
	}
	// A limited or unusable answer reorders the call to the person's own
	// accounts while it lasts, between the tool steps of a turn. At a turn
	// start the org's accounts are tried first (§4.5): an org account usable
	// again (a key resumed or added, a grant, an earlier reset) takes the
	// thread back, with a notice, and ends the lease; while none is, the
	// lease holds (llmCallSpec.leaseHolds). An unserved answer needs no
	// reorder: no org account serves its model anyway. org_only honours no
	// lease: the person's own account never serves.
	recheckAtTurnStart := q != nil && spec.TurnStart && !spec.leaseHolds &&
		(q.Reason == core.PersonalFundingUnusable || q.Reason == core.PersonalFundingLimited)
	if plan.order != orgfunding.OrderOrgOnly && q != nil && q.Reason != core.PersonalFundingUnserved &&
		q.LeaseActive(fundingNow(), "") && !recheckAtTurnStart {
		plan.leased = true
	}
	return plan
}

// limitedLeaseHolds reports a turn start that tried the org's accounts first
// under the chat's live limited lease and found them all still limited: the
// lease holds, and the call re-resolves with the person's own accounts first
// (llmCallSpec.leaseHolds) rather than asking again.
func (p fundingPlan) limitedLeaseHolds(spec llmCallSpec, modelID string) bool {
	return p.active && !p.leased && spec.TurnStart && !spec.leaseHolds && p.order != orgfunding.OrderOrgOnly &&
		p.question != nil && p.question.Reason == core.PersonalFundingLimited && p.question.LeaseActive(fundingNow(), modelID)
}

// apply is avail as this plan routes it: the person's own accounts first under
// personal_first or a live lease; and, mid-turn, the thread kept on the
// provider it is already on (a better tier is taken up at the next turn start,
// never between the tool steps of one turn: §4.5, D6).
func (r *AccountRouting) applyFundingPlan(ctx context.Context, spec llmCallSpec, plan fundingPlan, avail models.AvailableDrivers) models.AvailableDrivers {
	if !plan.active {
		return avail
	}
	if plan.order == orgfunding.OrderOrgOnly || plan.unchecked {
		avail = withoutPersonalBesideOrg(avail)
	}
	if plan.order == orgfunding.OrderPersonalFirst || plan.leased {
		avail = drivers.PersonalFirst(avail)
	} else if plan.personPick != "" && plan.order != orgfunding.OrderOrgOnly && !plan.unchecked {
		avail = r.promotePickWhileOrgLimited(ctx, spec, plan.personPick, avail)
	}
	if avail.DriverRank == nil || spec.TurnStart || spec.ThreadID == "" || r.Funding == nil {
		return avail
	}
	servingID, err := r.Funding.LatestServingAccount(ctx, spec.ThreadID)
	if err != nil || servingID == "" {
		return avail
	}
	for driver, list := range avail.Accounts {
		for _, a := range list {
			if a.AccountID == servingID {
				avail.StickyDriver = driver
				return avail
			}
		}
	}
	return avail
}

// withoutPersonalBesideOrg drops the person's own accounts from every driver
// that also has an org-funded one, before any account is chosen: under
// org_only the router never places a thread on them. A driver with only the
// person's accounts keeps them, so model resolution still sees the provider and
// the call waits (checkPersonalFunding) rather than failing to resolve.
func withoutPersonalBesideOrg(avail models.AvailableDrivers) models.AvailableDrivers {
	out := avail
	out.Accounts = make(map[models.DriverID][]models.AccountConfig, len(avail.Accounts))
	out.Drivers = make(map[models.DriverID]models.DriverConfig, len(avail.Drivers))
	for d, cfg := range avail.Drivers {
		out.Drivers[d] = cfg
	}
	for d, list := range avail.Accounts {
		orgServes := false
		for _, a := range list {
			orgServes = orgServes || models.OrgFunded(a.Funding)
		}
		if !orgServes {
			out.Accounts[d] = list
			continue
		}
		kept := make([]models.AccountConfig, 0, len(list))
		for _, a := range list {
			if a.Funding != models.FundingPersonal {
				kept = append(kept, a)
			}
		}
		out.Accounts[d] = kept
		if len(kept) > 0 {
			out.Drivers[d] = kept[0].DriverConfig
		}
	}
	return out
}

// fundingHold is the refusal of a call that would move to the person's own
// account without their say: a usage-limit wait with no known reset, so the
// workflow parks the thread (probing now and then, waking on any capacity
// wake, which an answer sends) exactly as it parks on a limit.
func fundingHold(providerDriver, why string) error {
	return &types.UsageLimitFailure{
		Err:   &ratelimit.UsageLimitError{Provider: "your organization's AI accounts (" + why + ")", StatusCode: 429},
		Facts: types.UsageLimitFacts{Provider: providerDriver},
	}
}

// replacedOrgFunding reports whether the call replaces an org-funded account
// of the run's org that no longer serves (§10.1 case 2). It reads provenance
// that routing cannot have rewritten: the account this call's assignment left
// (captured before the router stored the new one), the thread's assignments on
// other providers, and who paid for THIS thread's calls on its last account
// (thread_funding), which outlives that account's deletion. Another thread of
// the chat says nothing about this one.
func (r *AccountRouting) replacedOrgFunding(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers, acct *routedAccount) bool {
	candidate := map[string]bool{}
	for _, list := range avail.Accounts {
		for _, a := range list {
			candidate[a.AccountID] = true
		}
	}
	ofOrg := func(accountID string) bool {
		owner, attributed, found, err := r.Funding.ProviderAccountOrgs(ctx, accountID)
		return err == nil && found && (owner == spec.OrgID || attributed == spec.OrgID)
	}
	if acct != nil && acct.Change != nil && acct.Change.Previous != nil {
		prev := acct.Change.Previous
		if models.OrgFunded(prev.Funding) || ofOrg(prev.AccountID) {
			return true
		}
	}
	if spec.ThreadID != "" {
		assignments, err := r.Funding.ListThreadAssignments(ctx, spec.ThreadID)
		if err != nil {
			logging.WarnContext(ctx, "Could not read the thread's accounts", "threadID", spec.ThreadID, "error", err)
		}
		for _, as := range assignments {
			if !candidate[as.AccountID] && ofOrg(as.AccountID) {
				return true
			}
		}
	}
	if spec.ThreadID == "" {
		return false
	}
	funding, orgID, accountID, found, err := r.Funding.ThreadFunding(ctx, spec.ThreadID)
	if err != nil {
		// Unknown provenance: treat it as the org's, so the call asks or
		// waits rather than spending without a say.
		logging.WarnContext(ctx, "Could not read who paid for the thread; treating it as the org's", "threadID", spec.ThreadID, "error", err)
		return true
	}
	return found && models.OrgFunded(funding) && orgID == spec.OrgID && !candidate[accountID]
}

// recordThreadFunding remembers who pays for the thread's calls from this one
// on (thread_funding): what replacedOrgFunding reads once that account is gone.
// It runs for a call that goes ahead, so a held call keeps the org's
// provenance and the next attempt asks again; a call held because the org's
// tier is limited records the org's account it waits for (recordOrgWait). It
// fails closed: an org-funded
// call whose provenance cannot be written is not made (a retryable error),
// since a later deletion of that account would then go unseen. A personal or
// local call's provenance only ever widens what asks, so its failed write is
// logged.
func (r *AccountRouting) recordThreadFunding(ctx context.Context, spec llmCallSpec, acct *routedAccount) error {
	if r.Funding == nil || spec.ThreadID == "" || acct == nil || acct.Config.AccountID == "" || acct.Config.Funding == "" {
		return nil
	}
	err := r.Funding.RecordThreadFunding(ctx, spec.ThreadID, spec.OrgID, acct.Config.Funding, acct.Config.AccountID)
	if err == nil {
		return nil
	}
	if models.OrgFunded(acct.Config.Funding) {
		return fmt.Errorf("record who pays for the thread before an org-funded call: %w", err)
	}
	logging.WarnContext(ctx, "Could not record who pays for the thread", "threadID", spec.ThreadID, "error", err)
	return nil
}

// checkPersonalFunding is the decision after routing (§10.1 cases 2 and 3):
// a call routed to the person's own account in an org whose accounts it was
// given goes on only with their say. It returns the refusal to wait on, or
// nil to proceed; notice is a transcript notice the call owes.
func (r *AccountRouting) checkPersonalFunding(ctx context.Context, spec llmCallSpec, plan fundingPlan, avail models.AvailableDrivers,
	acct *routedAccount, providerDriver, modelID string) (notice string, err error) {
	if !plan.active || acct == nil {
		return "", nil
	}
	notice, err = r.decideCallFunding(ctx, spec, plan, avail, acct, providerDriver, modelID)
	if err != nil {
		return "", err
	}
	if err := r.recordThreadFunding(ctx, spec, acct); err != nil {
		// Without the thread's provenance on record, a later deletion of the
		// org's account could move the thread to the person's own without
		// asking: make no call, and let the activity retry.
		return "", err
	}
	return notice, nil
}

// decideCallFunding is checkPersonalFunding's decision for a routed account.
func (r *AccountRouting) decideCallFunding(ctx context.Context, spec llmCallSpec, plan fundingPlan, avail models.AvailableDrivers,
	acct *routedAccount, providerDriver, modelID string) (notice string, err error) {
	funding := acct.Config.Funding
	if models.OrgFunded(funding) {
		if r.movedUpFromPersonal(ctx, spec, avail, acct) {
			notice = fmt.Sprintf("Using your organization's %s account **%s** for this chat from now on.",
				noticeProviderName(providerDriver), accountName(acct.Config))
		}
		// Served by the org again: the episode is over.
		if plan.question != nil {
			if _, cerr := r.Funding.ClearPersonalFunding(ctx, spec.ChatID); cerr != nil {
				logging.WarnContext(ctx, "Could not clear the chat's funding question", "chatID", spec.ChatID, "error", cerr)
			} else if plan.question.State == core.PersonalFundingUsePersonal {
				notice = orgAccountsBackNotice()
			}
		}
		return notice, nil
	}
	if funding != models.FundingPersonal {
		return "", nil
	}
	if plan.unchecked || plan.order == orgfunding.OrderOrgOnly {
		// org_only never uses the person's own account, whatever was
		// answered, picked or served before; nor does a call whose consent
		// cannot be checked. Both wait, without asking.
		return "", fundingHold(providerDriver, "only your organization's accounts may serve this chat")
	}
	now := fundingNow()
	if plan.question.LeaseActive(now, modelID) {
		if !plan.question.Announced {
			// The first call under the person's answer says whose account
			// serves, once for the chat.
			if marked, merr := r.Funding.MarkPersonalFundingAnnounced(ctx, spec.ChatID); merr == nil && marked {
				return personalFundingNotice(*plan.question, plan.question.Reason, plan.limit == orgfunding.OnOrgLimitUsePersonal), nil
			}
		}
		return "", nil
	}
	if !spec.TurnStart && spec.ThreadID != "" {
		// Between the tool steps of one turn the thread goes on with the
		// account it is on: the move, and any question, is a turn start's.
		if serving, serr := r.Funding.LatestServingAccount(ctx, spec.ThreadID); serr == nil && serving == acct.Config.AccountID {
			return "", nil
		}
	}
	reason := core.PersonalFundingUnserved
	switch {
	case r.replacedOrgFunding(ctx, spec, avail, acct):
		reason = core.PersonalFundingUnusable
	case !plan.orgGave && plan.question != nil:
		// The org gives the person nothing now, but this chat asked about
		// its accounts (or was answered, a lease since ended): it was
		// waiting for, or leasing in place of, the org's accounts, which
		// are now gone. That is case 2, never "the org gave nothing": a
		// Keep waiting, or a lease that ended, is no consent to go on.
		reason = core.PersonalFundingUnusable
	case !plan.orgGave:
		// The org gave the person no account: nothing of the org's is
		// being replaced, so their own accounts serve as before.
		return "", nil
	}
	// Only a person's own decision is consent: their order (the per-org
	// setting, or the chat's order, both person-only), or the provider they
	// picked for the chat's root through the composer (chat_person_picks),
	// for the root's attended calls on that provider. A provider named by a
	// workflow's "<model>@<driver>" selector, a run's, preset's or spawn's
	// providers, or a sub-agent's model is not: software can name one. Such
	// a call asks. org_only is absolute: no pick lifts it.
	if plan.order == orgfunding.OrderPersonalFirst {
		return "", nil
	}
	if plan.order != orgfunding.OrderOrgOnly && plan.personPick != "" && plan.personPick == providerDriver {
		return "", nil
	}
	q := core.PersonalFunding{
		ChatID: spec.ChatID, UserID: spec.UserID, OrgID: spec.OrgID, Reason: reason, Model: modelID,
		PersonalProvider: providerDriver, PersonalAccountLabel: accountName(acct.Config),
	}
	if reason == core.PersonalFundingUnserved {
		// The org's accounts do serve the model, and every one is limited:
		// the thread was kept on the person's account (a turn start after
		// its lease ended, the org still limited). That is case 1, with the
		// org's reset, not "the org's accounts do not serve this model".
		if reset, limited := orgTierLimitedFor(avail, modelID, now); limited {
			q.Reason, q.OrgResetAt = core.PersonalFundingLimited, &reset
		}
	}
	return r.decidePersonalFunding(ctx, plan, q, providerDriver, now, true)
}

// orgTierLimitedFor reports whether avail holds org-funded first-tier accounts
// that serve modelID (on any driver serving it) and every one of them is
// limited at now, with the earliest of their resets.
func orgTierLimitedFor(avail models.AvailableDrivers, modelID string, now time.Time) (reset time.Time, limited bool) {
	def, ok := models.MustGetRegistry().GetDefinition(modelID)
	if !ok {
		return time.Time{}, false
	}
	for _, p := range def.Providers {
		for _, a := range avail.Accounts[models.DriverID(p.Driver)] {
			if !orgFundedFirstTier(a) || (a.Serves != nil && !a.Serves(modelID)) {
				continue
			}
			if !a.LimitedUntil.After(now) {
				return time.Time{}, false
			}
			if reset.IsZero() || a.LimitedUntil.Before(reset) {
				reset = a.LimitedUntil
			}
		}
	}
	return reset, !reset.IsZero()
}

// decidePersonalFunding acts on the person's choice for a call that would use
// their own account in place of the org's: grant a lease (use_personal), ask
// (the default) or wait. org_only never uses their account, and never asks.
// announce is whether this call serves on the lease it grants and so says so;
// a grant made for a later retry leaves the notice to the call that serves.
func (r *AccountRouting) decidePersonalFunding(ctx context.Context, plan fundingPlan, q core.PersonalFunding, providerDriver string, now time.Time, announce bool) (string, error) {
	why := map[string]string{
		core.PersonalFundingLimited:  "rate-limited",
		core.PersonalFundingUnusable: "unavailable",
		core.PersonalFundingUnserved: "they do not serve " + q.Model,
	}[q.Reason]
	limit := plan.limit
	if plan.order == orgfunding.OrderOrgOnly {
		limit = orgfunding.OnOrgLimitWait
	}
	switch limit {
	case orgfunding.OnOrgLimitUsePersonal:
		q.Announced = announce
		if q.Reason == core.PersonalFundingLimited {
			until := now.Add(time.Hour)
			if q.OrgResetAt != nil && q.OrgResetAt.After(now) {
				until = *q.OrgResetAt
			}
			q.LeaseUntil = &until
		}
		if err := r.Funding.GrantPersonalFunding(ctx, q, now); err != nil {
			// Without the lease on record the next call would ask again
			// rather than spend: wait.
			logging.WarnContext(ctx, "Could not record the use of the person's own account; waiting", "chatID", q.ChatID, "error", err)
			return "", fundingHold(providerDriver, why)
		}
		return personalFundingNotice(q, q.Reason, true), nil
	case orgfunding.OnOrgLimitAsk:
		if _, err := r.Funding.AskPersonalFunding(ctx, q, now); err != nil {
			logging.WarnContext(ctx, "Could not record the chat's funding question; waiting", "chatID", q.ChatID, "error", err)
		}
	}
	return "", fundingHold(providerDriver, why)
}

// fundingOnLimit is §10.1 case 1, from the usage-limit path: the org-funded
// accounts serving the model are all limited (facts carries no alternative
// within the tier) and the person's own account could serve now. Under
// use_personal it grants a lease until the org's earliest reset and reports
// an alternative, so the workflow retries at once and the retry routes to the
// person's account; under ask it records the question; the wait itself is
// unchanged.
func (r *AccountRouting) fundingOnLimit(ctx context.Context, uc usageLimitContext, facts *types.UsageLimitFacts) {
	if r == nil || r.Funding == nil || !uc.Plan.active || uc.Plan.unchecked || facts.AlternativeAccount || !uc.OrgFundedLimited {
		return
	}
	personal := r.personalAlternative(ctx, uc)
	if personal == nil {
		return
	}
	reset := facts.ProviderEarliestReset
	if reset.IsZero() {
		reset = facts.ResetAt
	}
	// The person's own pick of this provider for the chat's root (read only
	// for the root's attended calls) is consent: the retry runs on it
	// (promotePickWhileOrgLimited), with no question and no lease, so no
	// other provider and no sub-agent gains anything. org_only still wins.
	if uc.Plan.personPick != "" && uc.Plan.personPick == string(personal.DriverID) && uc.Plan.order != orgfunding.OrderOrgOnly {
		facts.AlternativeAccount = true
		return
	}
	q := core.PersonalFunding{
		ChatID: uc.ChatID, UserID: uc.Scope.UserID, OrgID: uc.Scope.OrgID, Reason: core.PersonalFundingLimited, Model: uc.ModelID,
		PersonalProvider: string(personal.DriverID), PersonalAccountLabel: accountName(*personal),
	}
	if !reset.IsZero() {
		q.OrgResetAt = &reset
	}
	if _, err := r.decidePersonalFunding(ctx, uc.Plan, q, string(uc.Driver), fundingNow(), false); err == nil {
		// Granted: the retry runs on the person's own account, and says so.
		facts.AlternativeAccount = true
	}
}

// recordOrgWait records, for a thread refused before any call because the
// org's accounts serving its model are all limited, that it waits for one of
// them (thread_funding). A thread held so on its first turn has served on no
// account: without this it carries no provenance, and should the org's
// accounts then leave its candidates altogether (a grant withdrawn, access
// narrowed, the key paused or deleted) the next attempt, a capacity wake's
// retry, would read as "the org gave the person nothing" and spend their own
// account with no question and no notice. With it, that attempt is §10.1 case
// 2 (replacedOrgFunding), whatever was answered meanwhile. accounts are the
// provider's accounts the call was routed over.
//
// The write is best effort: the call is held whether or not it lands, and the
// next hold writes it again.
func (r *AccountRouting) recordOrgWait(ctx context.Context, uc usageLimitContext, accounts []models.AccountConfig) {
	if r == nil || r.Funding == nil || !uc.Plan.active || uc.Plan.unchecked || uc.ThreadID == "" || !uc.OrgFundedLimited {
		return
	}
	for _, a := range accounts {
		if a.Serves != nil && !a.Serves(uc.ModelID) {
			continue
		}
		if !orgFundedFirstTier(a) || a.AccountID == "" {
			continue
		}
		if err := r.Funding.RecordThreadFunding(ctx, uc.ThreadID, uc.Scope.OrgID, a.Funding, a.AccountID); err != nil {
			logging.WarnContext(ctx, "Could not record that the thread waits for the org's account", "threadID", uc.ThreadID, "accountID", a.AccountID, "error", err)
		}
		return
	}
}

// promotePickWhileOrgLimited is avail with the person's own accounts of
// driver, the provider they picked for the chat's root (plan.personPick, read
// only for the root's attended calls), moved ahead of the org's accounts of
// driver while none of those can serve now (all rate-limited). It is no
// lease: it is decided per call, for that one provider, and the org's
// accounts serve again as soon as one can. Any other provider, and a
// question it cannot answer (no router, a failed read), is left as it is.
func (r *AccountRouting) promotePickWhileOrgLimited(ctx context.Context, spec llmCallSpec, driver string, avail models.AvailableDrivers) models.AvailableDrivers {
	d := models.DriverID(driver)
	var org []models.AccountConfig
	personal := false
	for _, a := range avail.Accounts[d] {
		switch {
		case orgFundedFirstTier(a):
			org = append(org, a)
		case a.Funding == models.FundingPersonal:
			personal = true
		}
	}
	if len(org) == 0 || !personal || r.Router == nil {
		return avail
	}
	modelID, _, _ := strings.Cut(spec.Selector.ID, "@")
	usable, _, err := r.Router.Alternatives(ctx, accountrouter.Request{UserID: spec.UserID, Driver: d, ModelID: modelID, Accounts: org}, "")
	if err != nil || usable {
		return avail
	}
	out := avail
	out.Accounts = make(map[models.DriverID][]models.AccountConfig, len(avail.Accounts))
	for id, list := range avail.Accounts {
		if id != d {
			out.Accounts[id] = list
			continue
		}
		next := make([]models.AccountConfig, len(list))
		for i, a := range list {
			switch {
			case a.Funding == models.FundingPersonal:
				a.Tier = models.TierFirst
			case a.Tier == models.TierFirst:
				a.Tier = models.TierSecond
			}
			next[i] = a
		}
		out.Accounts[id] = next
	}
	return out
}

// checkDefaultAccountFunding is checkPersonalFunding for a call routing chose
// no account for (the router failed, or the chosen account was refused or its
// driver could not be built): drivers.GetDriver then serves the provider's
// default account of the scope, which answers to the same consent. A default
// whose funding cannot be told waits.
func (r *AccountRouting) checkDefaultAccountFunding(ctx context.Context, spec llmCallSpec, plan fundingPlan, scope, avail models.AvailableDrivers,
	providerDriver, modelID string) (string, error) {
	if !plan.active {
		return "", nil
	}
	d := models.DriverID(providerDriver)
	def, ok := scope.Drivers[d]
	if !ok {
		// GetDriver has no account of this provider either and fails on its
		// own: nothing is spent.
		return "", nil
	}
	for _, a := range scope.Accounts[d] {
		if a.AccountID == def.AccountID {
			return r.checkPersonalFunding(ctx, spec, plan, avail, &routedAccount{Config: a}, providerDriver, modelID)
		}
	}
	return "", fundingHold(providerDriver, "who pays for this call is unknown")
}

// limitedTierIsOrgFunded reports whether a refusal of driver's accounts for
// modelID hit the org's: the best tier among the accounts serving the model
// is the org-funded first tier.
func limitedTierIsOrgFunded(accounts []models.AccountConfig, modelID string) bool {
	for _, a := range accounts {
		if a.Serves != nil && !a.Serves(modelID) {
			continue
		}
		if orgFundedFirstTier(a) {
			return true
		}
	}
	return false
}

// personalAlternative is a person's own account that could serve uc's model
// now, on any driver: nil when none could.
func (r *AccountRouting) personalAlternative(ctx context.Context, uc usageLimitContext) *models.AccountConfig {
	avail, err := lookupAvailableDrivers(ctx, uc.Scope)
	if err != nil {
		return nil
	}
	reg := models.MustGetRegistry()
	def, ok := reg.GetDefinition(uc.ModelID)
	if !ok {
		return nil
	}
	now := fundingNow()
	for _, p := range def.Providers {
		for _, a := range avail.Accounts[models.DriverID(p.Driver)] {
			if a.Funding != models.FundingPersonal || a.LimitedUntil.After(now) {
				continue
			}
			if a.Serves != nil && !a.Serves(uc.ModelID) {
				continue
			}
			if r.Router != nil {
				ok, _, aerr := r.Router.Alternatives(ctx, accountrouter.Request{
					UserID: uc.Scope.UserID, Driver: a.DriverID, ModelID: uc.ModelID, Accounts: []models.AccountConfig{a},
				}, "")
				if aerr != nil || !ok {
					continue
				}
			}
			a := a
			return &a
		}
	}
	return nil
}

// personalFundingNotice is the transcript notice of a call served by the
// person's own account in place of the org's. remembered says the person's
// setting chose it, which is where to change it.
func personalFundingNotice(q core.PersonalFunding, reason string, remembered bool) string {
	who := "your own account"
	if q.PersonalAccountLabel != "" {
		who = fmt.Sprintf("your %s account **%s**", noticeProviderName(q.PersonalProvider), q.PersonalAccountLabel)
	}
	var why string
	switch reason {
	case core.PersonalFundingLimited:
		why = "while your organization's AI accounts are rate-limited"
	case core.PersonalFundingUnusable:
		why = "while your organization's AI accounts are unavailable"
	default:
		why = "because your organization's AI accounts do not serve this model"
	}
	msg := fmt.Sprintf("Using %s %s.", who, why)
	if remembered {
		msg += " You chose this in Settings → Providers, where you can change it."
	}
	return msg
}

// movedUpFromPersonal reports a thread that was last served by the person's
// own account and is now newly placed on an org-funded account of another
// provider (the turn-start upgrade across providers, §4.5). A move within one
// provider is announced by the router's own notice (ReasonOrgAvailable).
func (r *AccountRouting) movedUpFromPersonal(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers, acct *routedAccount) bool {
	if r.Funding == nil || acct.Change == nil || !acct.Change.Changed || acct.Change.Previous != nil || spec.ThreadID == "" {
		return false
	}
	servingID, err := r.Funding.LatestServingAccount(ctx, spec.ThreadID)
	if err != nil || servingID == "" || servingID == acct.Config.AccountID {
		return false
	}
	for _, list := range avail.Accounts {
		for _, a := range list {
			if a.AccountID == servingID {
				return a.Funding == models.FundingPersonal
			}
		}
	}
	return false
}

func orgAccountsBackNotice() string {
	return "Using your organization's AI accounts again."
}

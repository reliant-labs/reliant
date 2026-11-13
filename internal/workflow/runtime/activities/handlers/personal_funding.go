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
}

// fundingNow is the clock of the funding decisions; a variable for tests.
var fundingNow = time.Now

// fundingPlan is what one call of a chat in an org knows about who may pay:
// the person's choice for the org, the chat's question, and whether the org
// gave the person anything at all.
type fundingPlan struct {
	// active is false for a call that names no org, or a chat with no
	// question store: nothing here applies, the call routes as before.
	active bool
	order  orgfunding.Order
	limit  orgfunding.OnOrgLimit
	// question is the chat's row, nil when it has none.
	question *core.PersonalFunding
	// orgGave reports that the scope holds at least one org-funded account
	// of the first tier: the org's key or the person's seat in it.
	orgGave bool
	// leased is true when a use_personal answer reorders this call to the
	// person's own accounts first.
	leased bool
}

// orgFundedFirstTier reports whether a is paid for by the org and preferred.
func orgFundedFirstTier(a models.AccountConfig) bool {
	return a.Tier == models.TierFirst && models.OrgFunded(a.Funding)
}

// fundingPlanFor reads the plan of spec's call over avail.
func (r *AccountRouting) fundingPlanFor(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers) fundingPlan {
	if r == nil || r.Funding == nil || spec.OrgID == "" || spec.ChatID == "" {
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
	if o := orgfunding.ParseOrder(spec.Failover.Funding); o != "" {
		// The chat's own order outranks the person's per-org setting.
		plan.order = o
	}
	if !spec.Attended && spec.UnattendedAnySource {
		// An automation that may use any source has nobody to ask: its
		// owner's stamp is the consent (org-provider-accounts §11.3).
		plan.limit = orgfunding.OnOrgLimitUsePersonal
	}
	q, err := r.Funding.GetChatPersonalFunding(ctx, spec.ChatID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the chat's funding question; deciding without it", "chatID", spec.ChatID, "error", err)
	}
	plan.question = q
	// A limited or unusable answer reorders the call to the person's own
	// accounts while it lasts; an unusable one only until a turn start finds
	// the org's accounts usable again (checked after routing). An unserved
	// answer needs no reorder: no org account serves its model anyway.
	recheckAtTurnStart := q != nil && q.Reason == core.PersonalFundingUnusable && spec.TurnStart
	if q != nil && q.Reason != core.PersonalFundingUnserved && q.LeaseActive(fundingNow(), "") && !recheckAtTurnStart {
		plan.leased = true
	}
	return plan
}

// apply is avail as this plan routes it: the person's own accounts first under
// personal_first or a live lease; and, mid-turn, the thread kept on the
// provider it is already on (a better tier is taken up at the next turn start,
// never between the tool steps of one turn: §4.5, D6).
func (r *AccountRouting) applyFundingPlan(ctx context.Context, spec llmCallSpec, plan fundingPlan, avail models.AvailableDrivers) models.AvailableDrivers {
	if !plan.active {
		return avail
	}
	if plan.order == orgfunding.OrderPersonalFirst || plan.leased {
		avail = drivers.PersonalFirst(avail)
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

// explicitPersonalPick reports that the call names its provider (a
// "<model>@<driver>" pick, the chat's provider switch, the run's providers)
// and that provider has no org-funded account: the person chose to pay, and
// the composer labelled the choice (§4.4, §10.1).
func explicitPersonalPick(spec llmCallSpec, avail models.AvailableDrivers, providerDriver string) bool {
	if !strings.Contains(spec.Selector.ID, "@") && spec.ChatProvider == "" && len(spec.RunProviders) == 0 && len(spec.Selector.Providers) == 0 {
		return false
	}
	for _, a := range avail.Accounts[models.DriverID(providerDriver)] {
		if models.OrgFunded(a.Funding) {
			return false
		}
	}
	return true
}

// threadWasOrgFunded reports whether the thread was on an org-funded account
// of the run's org that is no longer a candidate (§10.1 case 2): its sticky
// assignment names an account owned by, or attributed to, the org.
func (r *AccountRouting) threadWasOrgFunded(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers) bool {
	if spec.ThreadID == "" {
		return false
	}
	assignments, err := r.Funding.ListThreadAssignments(ctx, spec.ThreadID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read the thread's accounts; treating it as new", "threadID", spec.ThreadID, "error", err)
		return false
	}
	candidate := map[string]bool{}
	for _, list := range avail.Accounts {
		for _, a := range list {
			candidate[a.AccountID] = true
		}
	}
	for _, as := range assignments {
		if candidate[as.AccountID] {
			continue
		}
		owner, attributed, found, err := r.Funding.ProviderAccountOrgs(ctx, as.AccountID)
		if err != nil || !found {
			continue
		}
		if owner == spec.OrgID || attributed == spec.OrgID {
			return true
		}
	}
	return false
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
	now := fundingNow()
	if plan.question.LeaseActive(now, modelID) {
		if !plan.question.Announced {
			// The first call under the person's answer says whose account
			// serves, once for the chat.
			if marked, merr := r.Funding.MarkPersonalFundingAnnounced(ctx, spec.ChatID); merr == nil && marked {
				return personalFundingNotice(*plan.question, plan.question.Reason, false), nil
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
	case r.threadWasOrgFunded(ctx, spec, avail):
		reason = core.PersonalFundingUnusable
	case !plan.orgGave:
		// The org gave the person no account: nothing of the org's is
		// being replaced, so their own accounts serve as before.
		return "", nil
	}
	consented := plan.order == orgfunding.OrderPersonalFirst || explicitPersonalPick(spec, avail, providerDriver)
	if consented && plan.order != orgfunding.OrderOrgOnly {
		return "", nil
	}
	q := core.PersonalFunding{
		ChatID: spec.ChatID, UserID: spec.UserID, OrgID: spec.OrgID, Reason: reason, Model: modelID,
		PersonalProvider: providerDriver, PersonalAccountLabel: accountName(acct.Config),
	}
	return r.decidePersonalFunding(ctx, plan, q, providerDriver, now)
}

// decidePersonalFunding acts on the person's choice for a call that would use
// their own account in place of the org's: grant a lease (use_personal), ask
// (the default) or wait. org_only never uses their account, and never asks.
func (r *AccountRouting) decidePersonalFunding(ctx context.Context, plan fundingPlan, q core.PersonalFunding, providerDriver string, now time.Time) (string, error) {
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
		q.Announced = true // this call says so
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
	if r == nil || r.Funding == nil || !uc.Plan.active || facts.AlternativeAccount || !uc.OrgFundedLimited {
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
	q := core.PersonalFunding{
		ChatID: uc.ChatID, UserID: uc.Scope.UserID, OrgID: uc.Scope.OrgID, Reason: core.PersonalFundingLimited, Model: uc.ModelID,
		PersonalProvider: string(personal.DriverID), PersonalAccountLabel: accountName(*personal),
	}
	if !reset.IsZero() {
		q.OrgResetAt = &reset
	}
	if _, err := r.decidePersonalFunding(ctx, uc.Plan, q, string(uc.Driver), fundingNow()); err == nil {
		// Granted: the retry runs on the person's own account, and says so.
		facts.AlternativeAccount = true
	}
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
	if acct.Change == nil || !acct.Change.Changed || acct.Change.Previous != nil || spec.ThreadID == "" {
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

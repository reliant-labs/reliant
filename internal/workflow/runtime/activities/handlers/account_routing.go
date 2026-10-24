// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/chatmarkers"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/accountrouter"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// This file is where a request learns WHICH account of its provider serves it,
// and what to tell the workflow when that account is refused.
//
// The registry still answers "which model on which driver"; the account router
// (internal/llm/accountrouter) answers "which account of that driver, for this
// thread"; and a usage-limit refusal is turned into facts (types.UsageLimitFacts)
// for the workflow's one decision function. Every path here degrades to the
// plain behaviour a user without accounts has: a failure of the router, the
// store or the recorder never fails a turn, it just routes (or waits) as it did
// before accounts existed.

// lookupAvailableDrivers is drivers.LookupAvailableDrivers; a variable so the routing
// tests supply accounts without a database. Its error (wrapping
// drivers.ErrDriverLookupFailed) is a failed READ of the user's providers, which
// is not the same as a user who has none.
var lookupAvailableDrivers = drivers.LookupAvailableDrivers

// getAvailableDrivers is lookupAvailableDrivers for best-effort callers: a failed
// read is logged and reads as no providers.
func getAvailableDrivers(ctx context.Context, userID string) models.AvailableDrivers {
	avail, err := lookupAvailableDrivers(ctx, userID)
	if err != nil {
		logging.ErrorContext(ctx, "Failed to build available drivers from settings", "error", err, "userID", userID)
		return models.AvailableDrivers{Drivers: make(map[models.DriverID]models.DriverConfig)}
	}
	return avail
}

// DriverForAccountFunc builds a driver that talks as one specific account
// (drivers.GetDriverForAccount).
type DriverForAccountFunc func(ctx context.Context, userID string, cfg models.DriverConfig, model models.Model, opts ...llm.DriverOption) (llm.Driver, error)

// AccountLimitRecorder is what the routing needs of the account-limits
// recorder: an observer to install on a request served by an account, and the
// bench that takes an account out of rotation until its limit resets.
type AccountLimitRecorder interface {
	// For returns the observer to install on a request served by accountID.
	For(userID, accountID string) ratelimit.Observer
	// Bench records accountID refused until `until` (zero = unknown reset).
	Bench(ctx context.Context, userID, accountID string, until time.Time, window string) (time.Time, error)
}

// routingSettings reads the per-provider routing mode setting.
type routingSettings interface {
	GetSettingValue(ctx context.Context, userID, key string) (string, bool)
}

// AccountRouting wires multi-account routing into the LLM request path. The
// zero value, and a nil *AccountRouting, route nothing: requests use the
// driver's default account exactly as they did before accounts.
type AccountRouting struct {
	Router accountrouter.Service
	// ClearOtherAssignments removes old-provider pins at the next safe model
	// call after a chat-wide switch. A failure refuses that call rather than
	// letting stale routing survive unnoticed.
	ClearOtherAssignments func(ctx context.Context, userID, threadID, provider string) error
	// Store supplies the limit rows the account-switch notice quotes (the
	// window, the reset). Optional.
	Store accountrouter.Store
	// Recorder installs the rate-limit observer and benches refused accounts.
	// Optional: without it limits are not recorded and a refusal is waited out.
	Recorder AccountLimitRecorder
	// DriverFor builds the per-account driver. Routing is off without it: an
	// account is never assigned that cannot then be used.
	DriverFor DriverForAccountFunc
	// Settings supplies the routing mode (provider.routing.<driver>).
	Settings routingSettings
	// Credentials takes an account whose API key the provider rejected out of
	// rotation. Optional: without it a rejected key fails the turn as before.
	Credentials CredentialMarker
}

func (r *AccountRouting) active() bool {
	return r != nil && r.Router != nil && r.DriverFor != nil
}

// routingMode is the provider's routing mode: Balance unless the user chose
// "priority" (In order). Any trouble reading it is Balance.
func (r *AccountRouting) routingMode(ctx context.Context, userID string, driver models.DriverID) accountrouter.RoutingMode {
	if r == nil || r.Settings == nil {
		return accountrouter.RoutingBalance
	}
	v, _ := r.Settings.GetSettingValue(ctx, userID, "provider.routing."+string(driver))
	return accountrouter.ParseRoutingMode(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(v), " ", "")))
}

// observe installs the account's rate-limit observer on ctx.
func (r *AccountRouting) observe(ctx context.Context, userID string, acct *routedAccount) context.Context {
	if r == nil || r.Recorder == nil || acct == nil || acct.Config.AccountID == "" {
		return ctx
	}
	o := r.Recorder.For(userID, acct.Config.AccountID)
	if o == nil {
		return ctx
	}
	return ratelimit.WithObserver(ctx, o)
}

// routedAccount is the account that serves a call.
type routedAccount struct {
	Config models.AccountConfig
	// Accounts are all the driver's usable accounts, for Alternatives.
	Accounts []models.AccountConfig
	// Change is set when this call moved the thread (a switch, or its first
	// assignment).
	Change *accountrouter.Result
	// ModelID is the registry definition id the account was chosen for.
	ModelID string
}

// allAccountsLimited is the refusal the router makes without a network call
// when every account that could serve the model is limited. It IS a
// *ratelimit.UsageLimitError, so the workflow's wait path takes it exactly as
// it would the provider's own refusal.
type allAccountsLimited struct {
	*ratelimit.UsageLimitError
	Driver  models.DriverID
	ModelID string
	// Earliest is when the accounts next change state, and Probe says it is
	// only a probe bench's end. The embedded error's ResetAt is left zero for
	// a probe: no reset has been given, so none is announced.
	Earliest time.Time
	Probe    bool
}

func (e *allAccountsLimited) Error() string { return e.UsageLimitError.Error() }
func (e *allAccountsLimited) Unwrap() error { return e.UsageLimitError }

// routeAccount picks the account of providerDriver that serves this thread.
// It returns nil, nil when routing does not apply (no router, no per-account
// driver builder, a provider with no accounts, an injected resolver).
func routeAccount(ctx context.Context, spec llmCallSpec, avail models.AvailableDrivers, providerDriver, modelID string) (*routedAccount, error) {
	if !spec.Routing.active() {
		return nil, nil
	}
	driver := models.DriverID(providerDriver)
	if spec.ClearProviderPins && spec.ChatProvider != "" && spec.ThreadID != "" && spec.Routing.ClearOtherAssignments != nil {
		if err := spec.Routing.ClearOtherAssignments(ctx, spec.UserID, spec.ThreadID, providerDriver); err != nil {
			return nil, err
		}
	}
	accounts := avail.Accounts[driver]
	if len(accounts) == 0 {
		return nil, nil
	}
	res, err := spec.Routing.Router.Assign(ctx, accountrouter.Request{
		UserID:    spec.UserID,
		ThreadID:  spec.ThreadID,
		Driver:    driver,
		ModelID:   modelID,
		Accounts:  accounts,
		Preferred: spec.Failover.PreferredAccount[providerDriver],
		Mode:      spec.Routing.routingMode(ctx, spec.UserID, driver),
	})
	var all *accountrouter.ErrAllLimited
	if errors.As(err, &all) {
		resetAt := all.EarliestReset
		if all.ResetIsProbe {
			resetAt = time.Time{}
		}
		return nil, &allAccountsLimited{
			UsageLimitError: &ratelimit.UsageLimitError{
				Provider:   models.ProviderDisplayName(providerDriver),
				StatusCode: 429,
				ResetAt:    resetAt,
				Window:     all.Window,
			},
			Driver:   driver,
			ModelID:  modelID,
			Earliest: all.EarliestReset,
			Probe:    all.ResetIsProbe,
		}
	}
	if err != nil {
		// The router failing (or finding nothing it can assign) is not a
		// reason to fail the turn: the provider's default account serves, as it
		// did before accounts, and resolution's own error says what is wrong
		// if nothing can.
		logging.WarnContext(ctx, "[AccountRouter] could not assign an account; using the provider's default account",
			"userID", spec.UserID, "provider", providerDriver, "threadID", spec.ThreadID, "error", err)
		return nil, nil
	}
	change := res
	return &routedAccount{Config: res.Account, Accounts: accounts, Change: &change, ModelID: modelID}, nil
}

// announceAccountMove saves the transcript notice for a thread that moved to
// another account of its provider. A thread's first assignment is not a move.
func (r *AccountRouting) announceAccountMove(ctx context.Context, post func(text string), userID string, acct *routedAccount) {
	if r == nil || acct == nil || acct.Change == nil || !acct.Change.Changed || acct.Change.Previous == nil {
		return
	}
	var window string
	var resetAt time.Time
	if r.Store != nil && acct.Change.Previous.AccountID != "" {
		if limits, err := r.Store.ListAccountLimits(ctx, userID); err == nil {
			if ls, ok := limits[acct.Change.Previous.AccountID]; ok {
				window = ls.LimitedWindow
				// A probe bench's end is when to look again, not a reset.
				if ls.LimitedUntil != nil && ls.LimitedProbes == 0 {
					resetAt = *ls.LimitedUntil
				}
			}
		}
	}
	post(accountMoveNotice(*acct.Change, window, resetAt))
}

// usageLimitContext says who was refused, for usageLimitFailure.
type usageLimitContext struct {
	UserID   string
	ThreadID string
	// Driver is the provider that refused; empty when it cannot be told (the
	// error then carries no facts and the workflow simply waits).
	Driver  models.DriverID
	ModelID string
	// Account is the account that served the refused request, nil when the
	// request was not routed.
	Account *routedAccount
	Policy  types.FailoverPolicy
}

// usageLimitFailure turns a usage-limit refusal into a *types.UsageLimitFailure
// carrying the facts the workflow decides on: it benches the refused account,
// asks whether another account of the provider can serve now, and checks the
// chat's fallback. Any other error comes back unchanged.
//
// Every step is best-effort. A bench that could not be recorded reports no
// alternative (retrying would meet the same account again), so the failure is
// waited out as before accounts.
func (r *AccountRouting) usageLimitFailure(ctx context.Context, err error, uc usageLimitContext) error {
	if err == nil {
		return nil
	}
	var already *types.UsageLimitFailure
	if errors.As(err, &already) {
		return err
	}
	var all *allAccountsLimited
	synthetic := errors.As(err, &all)
	if synthetic {
		uc.Driver, uc.ModelID = all.Driver, all.ModelID
	}
	resetAt, window, ok := usageLimitOf(err)
	if !ok {
		return err
	}

	facts := types.UsageLimitFacts{Provider: string(uc.Driver), ResetAt: resetAt, Window: window}
	if uc.Account != nil {
		facts.AccountID = uc.Account.Config.AccountID
		facts.AccountLabel = accountName(uc.Account.Config)
	}

	switch {
	case synthetic:
		// Every account is already benched; nothing was refused by a provider.
		facts.ProviderEarliestReset, facts.ProviderResetIsProbe = all.Earliest, all.Probe
	case r.active() && uc.Account != nil && uc.Account.Config.AccountID != "":
		benched := true
		if r.Recorder != nil {
			if _, berr := r.Recorder.Bench(ctx, uc.UserID, uc.Account.Config.AccountID, resetAt, window); berr != nil {
				benched = false
				logging.WarnContext(ctx, "[Failover] Could not bench the refused account; waiting instead of switching",
					"userID", uc.UserID, "provider", string(uc.Driver), "accountID", uc.Account.Config.AccountID, "error", berr)
			}
		} else {
			benched = false
		}
		available, earliest, aerr := r.Router.Alternatives(ctx, accountrouter.Request{
			UserID: uc.UserID, ThreadID: uc.ThreadID, Driver: uc.Driver,
			ModelID: uc.ModelID, Accounts: uc.Account.Accounts,
		}, uc.Account.Config.AccountID)
		switch {
		case aerr != nil:
			logging.WarnContext(ctx, "[Failover] Could not ask for another account; waiting",
				"userID", uc.UserID, "provider", string(uc.Driver), "error", aerr)
		case available && benched:
			facts.AlternativeAccount = true
		case !available:
			facts.ProviderEarliestReset, facts.ProviderResetIsProbe = earliest.At, earliest.Probe
		}
	}
	if !facts.AlternativeAccount {
		facts.FallbackModel = r.fallbackModel(ctx, uc)
	}
	return &types.UsageLimitFailure{Err: err, Facts: facts}
}

// providerFailure is what every activity that streams from a routed account
// does with the provider's refusal, so a refused account is judged the same
// way whoever met it: a rejected API key leaves rotation (credentialRejection),
// a usage limit benches the account (usageLimitFailure), and anything else
// comes back unchanged. The next request, from any activity, then routes
// around the account instead of meeting it first again.
func (r *AccountRouting) providerFailure(ctx context.Context, err error, uc usageLimitContext) error {
	if rejected := r.credentialRejection(ctx, err, uc); rejected != nil {
		return rejected
	}
	return r.usageLimitFailure(ctx, err, uc)
}

// usageLimitOf reads a usage-limit refusal's reset and window, from the typed
// error when it survived the wraps, else from the marker every wrap keeps.
func usageLimitOf(err error) (resetAt time.Time, window string, ok bool) {
	var ule *ratelimit.UsageLimitError
	if errors.As(err, &ule) {
		return ule.ResetAt, ule.Window, true
	}
	kind, payload, found := chatmarkers.Extract(err.Error())
	if !found || kind != chatmarkers.KindProviderUsageLimit {
		return time.Time{}, "", false
	}
	if t, perr := time.Parse(time.RFC3339, strings.TrimSpace(payload)); perr == nil {
		resetAt = t.UTC()
	}
	return resetAt, "", true
}

// fallbackModel is the chat's cross-provider fallback when it is allowed and
// servable now: another provider's model the user has connected, whose own
// accounts are not all limited. Cross-provider fallback is opt-in, so no
// policy means no fallback.
func (r *AccountRouting) fallbackModel(ctx context.Context, uc usageLimitContext) string {
	want := strings.TrimSpace(uc.Policy.FallbackModel)
	if want == "" {
		return ""
	}
	modelID, driverID := drivers.ParseModelIDWithDriver(want)
	if driverID == "" || models.DriverID(driverID) == uc.Driver {
		return ""
	}
	fd := models.DriverID(driverID)
	avail, err := lookupAvailableDrivers(ctx, uc.UserID)
	if err != nil {
		logging.WarnContext(ctx, "[Failover] Could not read the user's providers; offering no fallback",
			"userID", uc.UserID, "error", err)
		return ""
	}
	cfg, ok := avail.Drivers[fd]
	if !ok || !cfg.IsConfigured() || !models.CanDriverUseModel(models.Family(driverID), models.ModelID(modelID)) {
		return ""
	}
	if avail.Availability != nil && avail.Availability(driverID, modelID).Disabled {
		return ""
	}
	if accounts := avail.Accounts[fd]; r.active() && len(accounts) > 0 {
		usable, _, err := r.Router.Alternatives(ctx, accountrouter.Request{
			UserID: uc.UserID, Driver: fd, ModelID: modelID, Accounts: accounts,
		}, "")
		if err == nil && !usable {
			return ""
		}
	}
	return want
}

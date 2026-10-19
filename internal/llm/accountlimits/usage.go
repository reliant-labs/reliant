// Copyright (c) 2025 Reliant Labs

package accountlimits

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
	"github.com/reliant-labs/reliant/internal/logging"
)

// TokenSource hands out a usable access token for one account, refreshing it
// first when it has expired. The refresh MUST go through the same per-account
// coordinator the LLM drivers use, so a poll never consumes a refresh token a
// live request is also about to consume.
type TokenSource interface {
	AccessToken(ctx context.Context, userID, accountID string) (string, error)
}

// usageStore is the slice of the account store a usage poll reads and writes.
type usageStore interface {
	GetAccountLimits(ctx context.Context, accountID string) (*core.LimitState, error)
	GetProviderAccountWithSecrets(ctx context.Context, userID, accountID string) (*core.AccountWithSecrets, error)
	ClaimUsageFetch(ctx context.Context, accountID string, now time.Time, floor time.Duration) (bool, error)
	ScheduleUsagePoll(ctx context.Context, accountID string, next time.Time, failures int, errText string) error
	UpsertAccountLimitSnapshot(ctx context.Context, s core.LimitState) error
	BenchAccount(ctx context.Context, userID, accountID string, until time.Time, window string, probe bool) error
}

// FetchUsage reads the usage windows of one subscription credential from its
// provider's usage endpoint. externalAccountID is the ChatGPT account id Codex
// needs; Claude ignores it. A provider without a usage endpoint is
// ErrNoUsageAPI.
func FetchUsage(ctx context.Context, d providerusage.Doer, provider, accessToken, externalAccountID string) (ratelimit.Snapshot, error) {
	switch provider {
	case providerusage.ProviderClaude:
		return providerusage.ClaudeUsage(ctx, d, accessToken)
	case providerusage.ProviderCodex:
		return providerusage.CodexUsage(ctx, d, accessToken, externalAccountID)
	}
	return ratelimit.Snapshot{}, ErrNoUsageAPI
}

// ErrNoUsageAPI means the provider has no usage endpoint (an API key, Copilot, …).
var ErrNoUsageAPI = errors.New("accountlimits: provider has no usage API")

// Fetcher reads usage from the providers' usage endpoints and records the
// result. It backs both the background Poller and the on-demand Refresher.
type Fetcher struct {
	Store  usageStore
	Tokens TokenSource
	// Doer makes the HTTP calls; nil means providerusage's 15 s client.
	Doer providerusage.Doer
	Now  func() time.Time
	// Enabled reports whether a provider's usage API may be called. Nil means
	// providerusage.UsageAPIEnabled, the switch for an endpoint that turned out
	// not to take our credential.
	Enabled func(provider string) bool
}

func (f *Fetcher) now() time.Time {
	if f.Now != nil {
		return f.Now().UTC()
	}
	return time.Now().UTC()
}

func (f *Fetcher) enabled(provider string) bool {
	if f.Enabled != nil {
		return f.Enabled(provider)
	}
	return providerusage.UsageAPIEnabled(provider)
}

// Outcome is what one fetch did.
type Outcome int

const (
	// OutcomeUpdated: the usage API answered and the windows were stored.
	OutcomeUpdated Outcome = iota
	// OutcomeDeferred: the account is not due (recent traffic, still benched, idle); rescheduled.
	OutcomeDeferred
	// OutcomeThrottled: the usage-API call floor was hit; nothing was called.
	OutcomeThrottled
	// OutcomeFailed: the call failed; the failure is on the row and the poll backs off.
	OutcomeFailed
	// OutcomeSkipped: nothing to poll (account gone, paused, needs sign-in, provider has no usage API).
	OutcomeSkipped
)

func (o Outcome) String() string {
	return [...]string{"updated", "deferred", "throttled", "failed", "skipped"}[o]
}

// Result is the outcome of one fetch, with the account state it left behind.
type Result struct {
	Outcome Outcome
	Account core.ProviderAccount
	// Limits is the account's limit state after the fetch; nil when it has none.
	Limits *core.LimitState
	// Err is why an OutcomeFailed fetch failed. It never carries a credential.
	Err error
}

// PollLeased handles an account the poller leased: it applies the cadence
// (design §4.3) before calling the provider, so an account in active use, a
// benched one, or an idle one costs no call.
func (f *Fetcher) PollLeased(ctx context.Context, accountID string) Result {
	return f.poll(ctx, "", accountID, true)
}

// Refresh fetches now for a user-requested refresh, ignoring the cadence but not
// the call floor. userID scopes the lookup: another user's account is
// core.ErrProviderAccountNotFound.
func (f *Fetcher) Refresh(ctx context.Context, userID, accountID string) (Result, error) {
	res := f.poll(ctx, userID, accountID, false)
	if res.Outcome == OutcomeSkipped && res.Err != nil {
		return res, res.Err
	}
	return res, nil
}

func (f *Fetcher) poll(ctx context.Context, userID, accountID string, leased bool) Result {
	now := f.now()
	limits, err := f.Store.GetAccountLimits(ctx, accountID)
	if err != nil {
		return Result{Outcome: OutcomeSkipped, Err: err}
	}
	if userID == "" {
		if limits == nil {
			// Deleted since it was leased (the limits row cascades), or never had one.
			return Result{Outcome: OutcomeSkipped}
		}
		userID = limits.UserID
	}
	acct, err := f.Store.GetProviderAccountWithSecrets(ctx, userID, accountID)
	if err != nil {
		// Only a lease proves the account is gone: its user came from the limits row. A user's
		// refresh of an id that is not theirs says nothing about the account, so it must not
		// touch the owner's poll schedule.
		if leased && errors.Is(err, core.ErrProviderAccountNotFound) {
			f.unschedule(ctx, accountID)
		}
		return Result{Outcome: OutcomeSkipped, Err: err}
	}
	res := Result{Account: acct.ProviderAccount, Limits: limits}

	if !f.enabled(acct.Provider) {
		if leased {
			f.unschedule(ctx, accountID)
		}
		res.Outcome = OutcomeSkipped
		return res
	}
	if leased {
		if acct.NeedsReauthAt != nil || acct.PausedAt != nil {
			f.unschedule(ctx, accountID)
			res.Outcome = OutcomeSkipped
			return res
		}
		if next, due := nextForLease(limits, now); !due {
			f.schedule(ctx, accountID, next, failuresOf(limits), errOf(limits))
			res.Outcome = OutcomeDeferred
			return res
		}
	}

	claimed, err := f.Store.ClaimUsageFetch(ctx, accountID, now, UsageFloor)
	if err != nil {
		res.Outcome, res.Err = OutcomeFailed, err
		return res
	}
	if !claimed {
		if leased {
			f.schedule(ctx, accountID, now.Add(UsageFloor), failuresOf(limits), errOf(limits))
		}
		res.Outcome = OutcomeThrottled
		return res
	}

	snap, fetchErr := f.fetch(ctx, acct)
	if fetchErr != nil {
		return f.recordFailure(ctx, res, limits, fetchErr, now)
	}
	return f.recordSuccess(ctx, res, limits, snap, now)
}

func (f *Fetcher) fetch(ctx context.Context, acct *core.AccountWithSecrets) (ratelimit.Snapshot, error) {
	token, err := f.Tokens.AccessToken(ctx, acct.UserID, acct.ID)
	if err != nil {
		return ratelimit.Snapshot{}, &tokenError{err: err}
	}
	return FetchUsage(ctx, f.Doer, acct.Provider, token, acct.ExternalAccountID)
}

// tokenError marks a failure to obtain an access token, as opposed to the usage
// endpoint refusing it.
type tokenError struct{ err error }

func (e *tokenError) Error() string { return "no usable access token: " + e.err.Error() }
func (e *tokenError) Unwrap() error { return e.err }

func (f *Fetcher) recordSuccess(ctx context.Context, res Result, cur *core.LimitState, snap ratelimit.Snapshot, now time.Time) Result {
	st := LimitStateFromSnapshot(res.Account.UserID, res.Account.ID, SourceUsageAPI, snap, now)
	st.ObservedAt = now
	if err := f.Store.UpsertAccountLimitSnapshot(ctx, st); err != nil {
		res.Outcome, res.Err = OutcomeFailed, err
		return res
	}

	until := f.reconcileBench(ctx, res.Account, cur, st, now)

	next := nextAfterPoll(cur, until, now)
	f.schedule(ctx, res.Account.ID, next, 0, "")
	res.Outcome = OutcomeUpdated
	res.Limits = f.reload(ctx, res.Account.ID, res.Limits)
	return res
}

// reconcileBench makes the bench agree with a usage reading and returns the
// bench in force (zero when none). An exhausted window benches the account. A
// reading that says "allowed" does not lift a bench the provider announced with
// a reset time (the store lifts it for any newer allowed snapshot, which is right
// for a response and wrong for a poll that cannot see a model-scoped refusal).
func (f *Fetcher) reconcileBench(ctx context.Context, acct core.ProviderAccount, cur *core.LimitState, st core.LimitState, now time.Time) time.Time {
	if ex := exhausted(st.Windows, st.RepresentativeWindow, st.Status, now); ex.Exhausted {
		eff, err := bench(ctx, f.Store, now, acct.UserID, acct.ID, ex.Until, ex.Window)
		if err != nil {
			logging.Warn("benching account from its usage reading failed", "account_id", acct.ID, "error", err)
			return time.Time{}
		}
		return eff
	}
	if cur != nil && cur.LimitedUntil != nil && cur.LimitedUntil.After(now) && cur.LimitedProbes == 0 {
		if err := f.Store.BenchAccount(ctx, acct.UserID, acct.ID, *cur.LimitedUntil, cur.LimitedWindow, false); err != nil {
			logging.Warn("restoring account bench after usage reading failed", "account_id", acct.ID, "error", err)
			return time.Time{}
		}
		return *cur.LimitedUntil
	}
	return time.Time{}
}

func (f *Fetcher) recordFailure(ctx context.Context, res Result, cur *core.LimitState, fetchErr error, now time.Time) Result {
	failures := failuresOf(cur) + 1
	text, delay, longTerm := classifyFailure(fetchErr, failures)
	next := now.Add(delay)
	if failures >= idleGiveUpAfter && (cur == nil || cur.LastTrafficAt.IsZero() || now.Sub(cur.LastTrafficAt) >= idleAfter) {
		// An idle account whose reads keep failing is not polled in the background any
		// more; the failure stays on the row, and Settings can still refresh it on demand.
		// Traffic schedules it again.
		next = time.Time{}
	}
	f.schedule(ctx, res.Account.ID, next, failures, text)

	// One warning when an account's usage reads start failing, then quiet: the
	// failure is on the row, and a dead endpoint must not fill the log.
	if failures == 1 || errOf(cur) != text {
		logging.Warn("usage API read failed; account runs on response headers",
			"account_id", res.Account.ID, "provider", res.Account.Provider, "reason", text, "retry_in", delay.Round(time.Second).String())
	} else {
		logging.Debug("usage API read still failing", "account_id", res.Account.ID, "provider", res.Account.Provider,
			"failures", failures, "long_term", longTerm)
	}
	res.Outcome, res.Err = OutcomeFailed, fetchErr
	res.Limits = f.reload(ctx, res.Account.ID, res.Limits)
	return res
}

func (f *Fetcher) reload(ctx context.Context, accountID string, fallback *core.LimitState) *core.LimitState {
	if l, err := f.Store.GetAccountLimits(ctx, accountID); err == nil && l != nil {
		return l
	}
	return fallback
}

func (f *Fetcher) schedule(ctx context.Context, accountID string, next time.Time, failures int, errText string) {
	if err := f.Store.ScheduleUsagePoll(ctx, accountID, next, failures, errText); err != nil && !errors.Is(err, core.ErrProviderAccountNotFound) {
		logging.Warn("scheduling usage poll failed", "account_id", accountID, "error", err)
	}
}

func (f *Fetcher) unschedule(ctx context.Context, accountID string) {
	f.schedule(ctx, accountID, time.Time{}, 0, "")
}

func failuresOf(l *core.LimitState) int {
	if l == nil {
		return 0
	}
	return l.UsageAPIFailures
}

func errOf(l *core.LimitState) string {
	if l == nil {
		return ""
	}
	return l.UsageAPIError
}

// nextForLease decides, for an account whose poll came due, whether to call the
// provider now (due) or when to look again.
func nextForLease(l *core.LimitState, now time.Time) (next time.Time, due bool) {
	if l == nil {
		return time.Time{}, true
	}
	// Benched: nothing to learn before the bench ends; confirm the reset just after.
	if l.LimitedUntil != nil && l.LimitedUntil.After(now) {
		if at := l.LimitedUntil.Add(confirmAfterReset); at.After(now) {
			return at, false
		}
	}
	// Headers are fresher and free while the account is in use.
	if !l.LastTrafficAt.IsZero() && now.Sub(l.LastTrafficAt) < headerFreshFor {
		return l.LastTrafficAt.Add(headerFreshFor), false
	}
	return time.Time{}, true
}

// nextAfterPoll is when to poll again after a successful reading: just after a
// bench ends, every ten minutes while the account has been used in the last day
// and no headers are arriving, and never for an idle account (the Recorder
// schedules it again when traffic returns; Settings refreshes it on demand).
func nextAfterPoll(cur *core.LimitState, benchUntil, now time.Time) time.Time {
	if benchUntil.After(now) {
		return benchUntil.Add(confirmAfterReset)
	}
	if cur != nil && !cur.LastTrafficAt.IsZero() && now.Sub(cur.LastTrafficAt) < idleAfter {
		return now.Add(headerFreshFor)
	}
	return time.Time{}
}

// Backoff after a failed read.
const (
	// idleGiveUpAfter is how many consecutive failed reads an idle account gets.
	idleGiveUpAfter = 3

	transientBackoffMin = time.Minute
	transientBackoffMax = time.Hour
	// A usage endpoint that refuses the credential or answers in a shape we do
	// not know is not coming back soon: the account runs on headers, and we ask
	// again rarely, in case the provider changes its mind or the token was merely
	// expired.
	unavailableBackoffMin = time.Hour
	unavailableBackoffMax = 12 * time.Hour
)

// classifyFailure turns a failed read into the text stored on the row (shown to
// the user; never carries a credential), how long to wait before asking again,
// and whether the endpoint looks unavailable rather than transiently failing.
func classifyFailure(err error, failures int) (text string, delay time.Duration, unavailable bool) {
	var (
		throttled *providerusage.ThrottledError
		status    *providerusage.StatusError
		tokenErr  *tokenError
	)
	switch {
	case errors.As(err, &throttled):
		delay = backoff(failures, transientBackoffMin, transientBackoffMax)
		if throttled.RetryAfter > delay {
			delay = throttled.RetryAfter
		}
		return "usage endpoint throttled (HTTP 429); showing rate-limit headers only", delay, false
	case errors.As(err, &status):
		return fmt.Sprintf("usage endpoint unavailable (HTTP %d); showing rate-limit headers only", status.StatusCode),
			backoff(failures, unavailableBackoffMin, unavailableBackoffMax), isUnavailableStatus(status.StatusCode)
	case errors.Is(err, providerusage.ErrUnexpectedResponse):
		return "usage endpoint answered in an unknown format; showing rate-limit headers only",
			backoff(failures, unavailableBackoffMin, unavailableBackoffMax), true
	case errors.As(err, &tokenErr):
		return "usage not read: " + sanitize(tokenErr.Error()), backoff(failures, transientBackoffMin, transientBackoffMax), false
	}
	return "usage not read: " + sanitize(err.Error()), backoff(failures, transientBackoffMin, transientBackoffMax), false
}

func isUnavailableStatus(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound
}

// backoff doubles from min per consecutive failure, capped at max.
func backoff(failures int, min, max time.Duration) time.Duration {
	d := min
	for i := 1; i < failures && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// sanitize keeps an error message short and single-line for storage and display.
func sanitize(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

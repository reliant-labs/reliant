// Copyright (c) 2025 Reliant Labs

// Package accountrouter chooses WHICH of a user's accounts for one provider
// serves a thread's next LLM call.
//
// The model registry still answers "which model on which driver"; this package
// answers "which account of that driver, for this thread", below the registry,
// so pins stay "<model>@<driver>" and never encode an account.
//
// A thread keeps the account it was first given (prompt cache and reasoning
// envelopes are per account) and moves only when that account stops being
// usable: limited, removed (which includes paused and needing sign-in, since
// those accounts are not offered to the router), or unable to serve the model. A thread that has no account yet is placed by the provider's routing
// mode: Balance (the default) takes the least-used account, choosing at random
// within a near-equal band and discounting accounts that just received many
// threads; In order takes the account with the lowest priority number.
//
// The package is pure decision logic over a thin, consumer-declared Store.
// Persisting limits and benching accounts is the account-limits recorder's job
// (a later package), not this one's.
//
//forge:not-a-component: built by reliant's hand-wired server and worker once the router is wired into call_llm (research/provider-load-balancing/DESIGN.md P3.4); no composition root builds it yet
package accountrouter

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// Service picks accounts.
type Service interface {
	// Assign returns the account that serves req's thread. A thread with a
	// usable assignment gets it back unchanged; otherwise the thread is
	// (re)assigned and the new assignment is stored.
	//
	// It returns *ErrAllLimited when the driver has accounts that could serve
	// the model but every one is limited, and *models.ProviderUnavailableError
	// when it has none that could.
	Assign(ctx context.Context, req Request) (Result, error)

	// Alternatives reports, as if exclude had just been benched, whether any
	// other account could serve req now. When none can, earliestReset is the
	// soonest any of the driver's accounts stops being limited (zero when no
	// reset is known).
	Alternatives(ctx context.Context, req Request, exclude string) (available bool, earliestReset time.Time, err error)
}

// Store is the storage the router reads and writes. It is declared here, where
// it is consumed; the account repository satisfies it.
type Store interface {
	GetThreadAssignment(ctx context.Context, threadID, provider string) (*Assignment, error)
	// SetThreadAssignment stores a. A non-empty expectedAccountID makes the
	// write a compare-and-swap on the thread's current account; applied is
	// false when another writer got there first.
	SetThreadAssignment(ctx context.Context, a Assignment, expectedAccountID string) (applied bool, err error)
	ListAccountLimits(ctx context.Context, userID string) (map[string]LimitState, error)
	RecentAssignmentCounts(ctx context.Context, accountIDs []string, since time.Time) (map[string]int, error)
}

// Deps are the router's collaborators.
type Deps struct {
	Store Store
	// Now is the clock. Defaults to time.Now.
	Now func() time.Time
	// Rand returns a uniform integer in [0, n) for n > 0. Defaults to
	// math/rand/v2. The router serialises calls, so it need not be safe for
	// concurrent use.
	Rand func(n int) int
}

// RoutingMode is how a provider's accounts share new threads.
type RoutingMode string

const (
	// RoutingBalance spreads new threads by usage. It is the default.
	RoutingBalance RoutingMode = "balance"
	// RoutingPriority fills accounts in priority order: the lowest priority
	// number takes every new thread until it is limited.
	RoutingPriority RoutingMode = "priority"
)

// ParseRoutingMode reads a stored routing mode. Anything unrecognised,
// including "", is RoutingBalance, so a bad setting never stops a conversation.
func ParseRoutingMode(s string) RoutingMode {
	if RoutingMode(s) == RoutingPriority {
		return RoutingPriority
	}
	return RoutingBalance
}

// Request asks for the account of one driver that should serve a thread.
type Request struct {
	UserID string
	// ThreadID names the thread whose sticky assignment applies. Empty picks
	// an account without reading or writing any assignment.
	ThreadID string
	Driver   models.DriverID
	// ModelID filters accounts by AccountConfig.Serves and selects which
	// model-scoped usage windows count.
	ModelID string
	// Accounts is AvailableDrivers.Accounts[Driver].
	Accounts []models.AccountConfig
	// Preferred names an account the caller wants: a models.ProviderRef
	// ("claude:alice@example.com") or its bare instance ("alice@example.com"),
	// matched against the account id, email or label, case-insensitively.
	// It wins whenever that account is usable, and is ignored otherwise. A
	// ref for another driver is ignored.
	Preferred string
	Mode      RoutingMode
}

// Why a thread got the account it did (Result.Reason).
const (
	ReasonInitial  = "initial"  // the thread had no account yet
	ReasonLimited  = "limited"  // its account is limited
	ReasonRemoved  = "removed"  // its account is gone, or unusable for a reason the caller did not report
	ReasonUnserved = "unserved" // its account's catalog does not include the model
	ReasonPinned   = "pinned"   // the caller's preferred account
)

// Result is the account chosen for a thread.
type Result struct {
	Account models.AccountConfig
	// Previous is the account the thread left, set only when this call moved
	// it. When that account is no longer in Request.Accounts it carries just
	// its id.
	Previous *models.AccountConfig
	// Reason is why the thread was (re)assigned; empty when Changed is false.
	Reason string
	// Changed is true when this call wrote a new assignment, including the
	// thread's first. A caller that lost the write race to another activity on
	// the same thread adopts the winner's account with Changed false: the
	// winner announces the move.
	Changed bool
}

// ErrAllLimited means every account that could serve the model is limited.
type ErrAllLimited struct {
	Driver models.DriverID
	// EarliestReset is the soonest any of the driver's accounts stops being
	// limited.
	EarliestReset time.Time
	// Window names the limit that account hit; empty when unknown.
	Window string
}

// Error implements error. Use errors.As with a *ErrAllLimited.
func (e *ErrAllLimited) Error() string {
	name := models.ProviderDisplayName(string(e.Driver))
	if e.EarliestReset.IsZero() {
		return name + " is rate-limited on all accounts; no reset time is known"
	}
	return fmt.Sprintf("%s is rate-limited on all accounts until %s", name, e.EarliestReset.UTC().Format(time.RFC3339))
}

// LimitWindow is one usage window of an account.
type LimitWindow struct {
	Name         string
	UsedFraction *float64
	Limit        *int64
	Remaining    *int64
	ResetsAt     time.Time
	LengthS      int64
}

// LimitState is what is known of an account's usage and whether it is benched.
type LimitState struct {
	AccountID, UserID    string
	Status               string
	Windows              []LimitWindow
	RepresentativeWindow string
	Source               string
	ObservedAt           time.Time
	LimitedUntil         *time.Time
	LimitedWindow        string
	LimitedProbes        int
	UsageAPIError        string
}

// Assignment is a thread's account for one provider.
type Assignment struct {
	ThreadID, Provider, AccountID string
	AssignedAt                    time.Time
	Reason                        string
}

// Copyright (c) 2025 Reliant Labs

package accountrouter

import (
	"context"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/logging"
)

const (
	// herdWindow and herdPenalty make threads that start in the same moment
	// spread out: every assignment an account received in the last herdWindow
	// counts as herdPenalty of used capacity, because the usage windows have
	// not caught up with it yet.
	herdWindow  = 5 * time.Minute
	herdPenalty = 0.03

	// bandsPerUnit buckets scores into 5-point bands, so accounts within a few
	// points of each other are treated as equal and chosen between at random.
	bandsPerUnit = 20

	// maxAssignAttempts bounds how often a lost assignment race is retried.
	maxAssignAttempts = 3
)

type router struct {
	store Store
	now   func() time.Time

	mu  sync.Mutex // serialises rnd, which the caller may not have made concurrency-safe
	rnd func(int) int
}

var _ Service = (*router)(nil)

// New returns the account router. A nil Deps.Store disables stickiness and
// usage awareness: every call then picks by priority among the accounts it is
// given.
func New(deps Deps) Service {
	r := &router{store: deps.Store, now: deps.Now, rnd: deps.Rand}
	if r.store == nil {
		r.store = noStore{}
	}
	if r.now == nil {
		r.now = time.Now
	}
	if r.rnd == nil {
		r.rnd = rand.IntN
	}
	return r
}

func (r *router) intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rnd(n)
}

// accountState is whether, and why not, an account can take a request now.
type accountState int

const (
	stateOK accountState = iota
	stateLimited
	statePaused
	stateNeedsSignIn
	stateUnserved
)

type accountView struct {
	cfg           models.AccountConfig
	state         accountState
	limitedUntil  time.Time
	limitedWindow string
	limits        *LimitState
}

// classify states each of req.Accounts at now. exclude, when set, is treated
// as limited regardless of what is stored.
func classify(req Request, limits map[string]LimitState, now time.Time, exclude string) []accountView {
	views := make([]accountView, 0, len(req.Accounts))
	for _, cfg := range req.Accounts {
		v := accountView{cfg: cfg}
		if ls, ok := limits[cfg.AccountID]; ok {
			ls := ls
			v.limits = &ls
		}
		switch {
		case cfg.Status == models.AccountPaused:
			v.state = statePaused
		case cfg.Status == models.AccountNeedsSignIn:
			v.state = stateNeedsSignIn
		case cfg.Serves != nil && !cfg.Serves(req.ModelID):
			v.state = stateUnserved
		default:
			if v.limits != nil && v.limits.LimitedUntil != nil && v.limits.LimitedUntil.After(now) {
				v.limitedUntil = *v.limits.LimitedUntil
				v.limitedWindow = v.limits.LimitedWindow
			}
			if cfg.LimitedUntil.After(v.limitedUntil) {
				v.limitedUntil = cfg.LimitedUntil
			}
			if v.limitedUntil.After(now) || (exclude != "" && cfg.AccountID == exclude) {
				v.state = stateLimited
			}
		}
		views = append(views, v)
	}
	return views
}

func (r *router) Assign(ctx context.Context, req Request) (Result, error) {
	now := r.now()
	limits := r.loadLimits(ctx, req.UserID)
	views := classify(req, limits, now, "")
	provider := string(req.Driver)

	persist := req.ThreadID != ""
	var assigned *Assignment
	if persist {
		a, err := r.store.GetThreadAssignment(ctx, req.ThreadID, provider)
		if err != nil {
			// A thread whose assignment cannot be read still gets an account;
			// it just is not remembered, and must not overwrite what is stored.
			logging.Warn("[AccountRouter] could not read the thread's account; routing without stickiness",
				"error", err, "thread_id", req.ThreadID, "provider", provider)
			persist = false
		} else {
			assigned = a
		}
	}

	var res Result
	for range maxAssignAttempts {
		prevID := ""
		var prev *accountView
		if assigned != nil && assigned.AccountID != "" {
			prevID = assigned.AccountID
			prev = findView(views, prevID)
			if prev != nil && prev.state == stateOK {
				return Result{Account: prev.cfg}, nil
			}
		}

		pick, byPreference, err := r.choose(ctx, req, views, now)
		if err != nil {
			return Result{}, err
		}
		res = Result{Account: pick.cfg, Reason: ReasonInitial}
		switch {
		case prevID != "":
			res.Reason = leaveReason(prev)
			res.Previous = previousConfig(req.Driver, prevID, prev)
		case byPreference:
			res.Reason = ReasonPinned
		}
		if !persist {
			return res, nil
		}

		applied, err := r.store.SetThreadAssignment(ctx, Assignment{
			ThreadID: req.ThreadID, Provider: provider, AccountID: pick.cfg.AccountID,
			AssignedAt: now, Reason: res.Reason,
		}, prevID)
		if err != nil {
			logging.Warn("[AccountRouter] could not store the thread's account; continuing unrecorded",
				"error", err, "thread_id", req.ThreadID, "provider", provider, "account_id", pick.cfg.AccountID)
			res.Changed = true
			return res, nil
		}
		if applied {
			res.Changed = true
			return res, nil
		}

		// Another activity on this thread assigned first: adopt its choice if
		// it is usable, else pick again against what it wrote.
		cur, err := r.store.GetThreadAssignment(ctx, req.ThreadID, provider)
		if err != nil {
			logging.Warn("[AccountRouter] could not re-read the thread's account after a lost race",
				"error", err, "thread_id", req.ThreadID, "provider", provider)
			return res, nil
		}
		assigned = cur
	}
	return res, nil
}

func (r *router) Alternatives(ctx context.Context, req Request, exclude string) (bool, time.Time, error) {
	now := r.now()
	views := classify(req, r.loadLimits(ctx, req.UserID), now, exclude)
	var earliest time.Time
	for _, v := range views {
		switch v.state {
		case stateOK:
			return true, time.Time{}, nil
		case stateLimited:
			if !v.limitedUntil.IsZero() && (earliest.IsZero() || v.limitedUntil.Before(earliest)) {
				earliest = v.limitedUntil
			}
		}
	}
	return false, earliest, nil
}

// choose picks an account for a thread that has none usable. byPreference is
// true when the caller's preferred account decided it.
func (r *router) choose(ctx context.Context, req Request, views []accountView, now time.Time) (pick accountView, byPreference bool, err error) {
	var usable []accountView
	var limited []accountView
	for _, v := range views {
		switch v.state {
		case stateOK:
			usable = append(usable, v)
		case stateLimited:
			limited = append(limited, v)
		}
	}
	if len(usable) == 0 {
		if len(limited) == 0 {
			return accountView{}, false, &models.ProviderUnavailableError{
				ModelID: req.ModelID, Providers: []string{string(req.Driver)},
			}
		}
		all := &ErrAllLimited{Driver: req.Driver}
		for _, v := range limited {
			if all.EarliestReset.IsZero() || v.limitedUntil.Before(all.EarliestReset) {
				all.EarliestReset = v.limitedUntil
				all.Window = v.limitedWindow
			}
		}
		return accountView{}, false, all
	}

	if want := preferredInstance(req); want != "" {
		for _, v := range usable {
			if matchesInstance(v.cfg, want) {
				return v, true, nil
			}
		}
	}
	if len(usable) == 1 {
		return usable[0], false, nil
	}
	if req.Mode == RoutingPriority {
		return lowestPriority(usable), false, nil
	}
	return r.leastUsed(ctx, req, usable, now), false, nil
}

// leastUsed returns a uniformly random account from the lowest usage band.
func (r *router) leastUsed(ctx context.Context, req Request, usable []accountView, now time.Time) accountView {
	ids := make([]string, len(usable))
	for i, v := range usable {
		ids[i] = v.cfg.AccountID
	}
	recent, err := r.store.RecentAssignmentCounts(ctx, ids, now.Add(-herdWindow))
	if err != nil {
		logging.Warn("[AccountRouter] could not read recent assignments; skipping the herd penalty",
			"error", err, "provider", string(req.Driver))
	}

	best := -1
	var band []accountView
	for _, v := range usable {
		score := 0.0
		if v.limits != nil {
			score, _ = Score(*v.limits, req.ModelID, now)
		}
		b := bandOf(score + herdPenalty*float64(recent[v.cfg.AccountID]))
		switch {
		case best < 0 || b < best:
			best, band = b, []accountView{v}
		case b == best:
			band = append(band, v)
		}
	}
	if len(band) == 1 {
		return band[0]
	}
	return band[r.intn(len(band))]
}

func bandOf(score float64) int {
	// The epsilon keeps 0.15*20 = 3.0000000000000004 style products on the
	// side of the boundary a human reading "15 points" expects.
	return int(score*bandsPerUnit + 1e-9)
}

func lowestPriority(usable []accountView) accountView {
	best := usable[0]
	for _, v := range usable[1:] {
		if v.cfg.Priority < best.cfg.Priority {
			best = v
		}
	}
	return best
}

func (r *router) loadLimits(ctx context.Context, userID string) map[string]LimitState {
	limits, err := r.store.ListAccountLimits(ctx, userID)
	if err != nil {
		// Unknown usage looks like an unused account, and an account that is
		// in fact limited is benched by its own refusal, so keep going.
		logging.Warn("[AccountRouter] could not read account limits; routing without them", "error", err)
		return nil
	}
	return limits
}

func findView(views []accountView, accountID string) *accountView {
	for i := range views {
		if views[i].cfg.AccountID == accountID {
			return &views[i]
		}
	}
	return nil
}

// leaveReason is why a thread cannot stay on the account it had; prev is nil
// when that account is no longer among the driver's accounts.
func leaveReason(prev *accountView) string {
	if prev == nil {
		return ReasonRemoved
	}
	switch prev.state {
	case stateLimited:
		return ReasonLimited
	case statePaused:
		return ReasonPaused
	case stateNeedsSignIn:
		return ReasonNeedsReauth
	case stateUnserved:
		return ReasonUnserved
	}
	return ReasonRemoved
}

func previousConfig(driver models.DriverID, accountID string, prev *accountView) *models.AccountConfig {
	if prev != nil {
		cfg := prev.cfg
		return &cfg
	}
	return &models.AccountConfig{DriverConfig: models.DriverConfig{DriverID: driver, AccountID: accountID}}
}

// preferredInstance is the instance part of req.Preferred when it names this
// request's driver (or is a bare account id / email / label), else "".
func preferredInstance(req Request) string {
	p := strings.TrimSpace(req.Preferred)
	if p == "" {
		return ""
	}
	ref, err := models.ParseProviderRef(p)
	if err != nil {
		return p
	}
	if ref.Driver != req.Driver {
		return ""
	}
	return ref.Instance
}

func matchesInstance(cfg models.AccountConfig, instance string) bool {
	return strings.EqualFold(cfg.AccountID, instance) ||
		strings.EqualFold(cfg.AccountEmail, instance) ||
		strings.EqualFold(cfg.AccountLabel, instance)
}

// noStore is the Store of a router built without one.
type noStore struct{}

func (noStore) GetThreadAssignment(context.Context, string, string) (*Assignment, error) {
	return nil, nil
}

func (noStore) SetThreadAssignment(context.Context, Assignment, string) (bool, error) {
	return true, nil
}

func (noStore) ListAccountLimits(context.Context, string) (map[string]LimitState, error) {
	return nil, nil
}

func (noStore) RecentAssignmentCounts(context.Context, []string, time.Time) (map[string]int, error) {
	return nil, nil
}

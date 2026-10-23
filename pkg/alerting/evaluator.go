// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Limits bound what one rule and one owner may cost.
type Limits struct {
	// MinInterval is the floor on a rule's interval.
	MinInterval time.Duration
	// MaxExecutionTime is the per-query timeout, applied server-side and to the
	// call's context.
	MaxExecutionTime time.Duration
	// MaxInstancesPerRule: a result with more rows than this is an error, and the
	// rule's existing instances are left untouched for that evaluation.
	MaxInstancesPerRule int
	// MaxRulesPerOwner: rules past this count (in name order) are not evaluated.
	MaxRulesPerOwner int
}

// DefaultLimits are conservative.
func DefaultLimits() Limits {
	return Limits{MinInterval: time.Minute, MaxExecutionTime: 20 * time.Second, MaxInstancesPerRule: 200, MaxRulesPerOwner: 100}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MinInterval <= 0 {
		l.MinInterval = d.MinInterval
	}
	if l.MaxExecutionTime <= 0 {
		l.MaxExecutionTime = d.MaxExecutionTime
	}
	if l.MaxInstancesPerRule <= 0 {
		l.MaxInstancesPerRule = d.MaxInstancesPerRule
	}
	if l.MaxRulesPerOwner <= 0 {
		l.MaxRulesPerOwner = d.MaxRulesPerOwner
	}
	return l
}

// Evaluator runs rules one step at a time. It holds no timers; Scheduler drives
// it. Safe for concurrent use as long as one rule is never evaluated by two
// callers at once, which the Scheduler's lease guarantees.
type Evaluator struct {
	querier  Querier
	notifier Notifier
	store    StateStore
	limits   Limits
	logger   *slog.Logger
	now      func() time.Time
}

// EvaluatorDeps are the evaluator's collaborators.
type EvaluatorDeps struct {
	Querier  Querier
	Notifier Notifier
	Store    StateStore
	Limits   Limits
	Logger   *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// NewEvaluator validates deps and builds an Evaluator.
func NewEvaluator(d EvaluatorDeps) (*Evaluator, error) {
	switch {
	case d.Querier == nil:
		return nil, errors.New("alerting: EvaluatorDeps.Querier is required")
	case d.Notifier == nil:
		return nil, errors.New("alerting: EvaluatorDeps.Notifier is required")
	case d.Store == nil:
		return nil, errors.New("alerting: EvaluatorDeps.Store is required")
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Evaluator{querier: d.Querier, notifier: d.Notifier, store: d.Store, limits: d.Limits.withDefaults(), logger: d.Logger, now: d.Now}, nil
}

// EffectiveInterval is the interval a rule is evaluated at.
func (e *Evaluator) EffectiveInterval(r Rule) time.Duration {
	if r.Interval < e.limits.MinInterval {
		return e.limits.MinInterval
	}
	return r.Interval
}

// Select applies the per-owner rule cap and orders rules by key, so which rules
// the cap drops is deterministic.
func (e *Evaluator) Select(rules []Rule) (kept []Rule, dropped int) {
	sort.Slice(rules, func(i, j int) bool { return rules[i].String() < rules[j].String() })
	if len(rules) > e.limits.MaxRulesPerOwner {
		return rules[:e.limits.MaxRulesPerOwner], len(rules) - e.limits.MaxRulesPerOwner
	}
	return rules, 0
}

// EvaluateRule runs one rule once. prev is the stored instances of this rule.
//
// A rule that cannot be evaluated (a refused expr, a query error, an oversized
// or malformed result) changes nothing: its instances keep their state and any
// firing alert keeps being re-sent until it lapses on its own `endsAt`. Treating
// an error as "nothing returned" would resolve real incidents whenever the
// database hiccuped.
func (e *Evaluator) EvaluateRule(ctx context.Context, rule Rule, prev []Instance) ([]Instance, error) {
	now := e.now()
	interval := e.EffectiveInterval(rule)

	ss, err := e.run(ctx, rule)
	if err != nil {
		// keep re-asserting what is already firing
		e.post(ctx, rule, prev, interval, now, false)
		return prev, err
	}
	after, ch := advance(rule, prev, ss, now)
	return e.commit(ctx, rule, after, ch, interval, now)
}

// Retire resolves the live instances of a rule that was removed.
func (e *Evaluator) Retire(ctx context.Context, key RuleKey, prev []Instance, interval time.Duration) ([]Instance, error) {
	now := e.now()
	after, ch := retire(prev, now)
	return e.commit(ctx, Rule{RuleKey: key}, after, ch, interval, now)
}

func (e *Evaluator) run(ctx context.Context, rule Rule) ([]series, error) {
	if err := ValidateExpr(rule.Expr); err != nil {
		return nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, e.limits.MaxExecutionTime+5*time.Second)
	defer cancel()
	rows, err := e.querier.Query(qctx, rule.Owner, rule.Expr, QueryOptions{
		MaxExecutionTime: e.limits.MaxExecutionTime,
		MaxResultRows:    e.limits.MaxInstancesPerRule + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("alerting: query for %s: %w", rule.RuleKey, err)
	}
	if len(rows) > e.limits.MaxInstancesPerRule {
		return nil, fmt.Errorf("%w: %s returned more than %d rows; tighten the HAVING clause", ErrBadResult, rule.RuleKey, e.limits.MaxInstancesPerRule)
	}
	return buildSeries(rule, rows)
}

// commit persists first and notifies second. If notification fails after the
// state is stored, a firing instance is re-sent on the next evaluation, and a
// resolved one is retried because ResolveSent is still false.
func (e *Evaluator) commit(ctx context.Context, rule Rule, after []Instance, ch Changes, interval time.Duration, now time.Time) ([]Instance, error) {
	if !ch.empty() {
		if err := e.store.Apply(ctx, ch); err != nil {
			return nil, &StoreError{Rule: rule.RuleKey, Err: err}
		}
	}
	return e.post(ctx, rule, after, interval, now, true), nil
}

func (e *Evaluator) post(ctx context.Context, rule Rule, after []Instance, interval time.Duration, now time.Time, markDelivered bool) []Instance {
	alerts, delivered := alertsFor(after, interval, now)
	if len(alerts) == 0 {
		return after
	}
	if err := e.notifier.Notify(ctx, alerts); err != nil {
		e.logger.WarnContext(ctx, "alertmanager rejected a notification; retrying next evaluation", "rule", rule.String(), "error", err)
		return after
	}
	if !markDelivered || len(delivered) == 0 {
		return after
	}
	sent := map[string]bool{}
	var mark Changes
	for i := range after {
		for _, d := range delivered {
			if after[i].Fingerprint == d.Fingerprint {
				after[i].ResolveSent = true
				sent[d.Fingerprint] = true
				mark.Upsert = append(mark.Upsert, after[i])
			}
		}
	}
	if err := e.store.Apply(ctx, mark); err != nil {
		e.logger.WarnContext(ctx, "recording a delivered resolution failed; it will be sent again", "rule", rule.String(), "error", err)
	}
	return after
}

// StoreError is a failed write to the StateStore. The caller's in-memory copy of
// the rule's state may be ahead of what was stored and must be reloaded.
type StoreError struct {
	Rule RuleKey
	Err  error
}

func (e *StoreError) Error() string {
	return fmt.Sprintf("alerting: storing state for %s: %v", e.Rule, e.Err)
}
func (e *StoreError) Unwrap() error { return e.Err }

// ownerState caches an owner's stored instances between ticks.
type ownerState struct {
	mu        sync.Mutex
	loaded    bool
	instances map[RuleKey][]Instance
	// due is when each rule next runs; per owner, so owners evaluate in
	// parallel without sharing a map.
	due map[RuleKey]time.Time
}

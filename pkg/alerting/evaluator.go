// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits bound what one rule and one env may cost.
type Limits struct {
	// MinInterval is the floor on a rule's interval.
	MinInterval time.Duration
	// MaxExecutionTime is the per-query timeout, applied server-side and to the
	// call's context.
	MaxExecutionTime time.Duration
	// MaxInstancesPerRule: a result with more rows than this is an error, and the
	// rule's existing instances are left untouched for that evaluation.
	MaxInstancesPerRule int
	// MaxRulesPerEnv: rules past this count in ONE env are not evaluated (see
	// Select for which). The unit is the env, not the owner: an owner's envs
	// are independent deployments, and a cap over their sum let one env's
	// rules push another env's alerts out of evaluation.
	MaxRulesPerEnv int
}

// DefaultMaxRulesPerEnv is sized from measured evaluation cost. An owner's rules
// run one after another in a single lane per scheduler tick. Measured in prod
// (ClickHouse system.query_log, 6h, tenant zero's 100 evaluated rules; the
// Postgres-answered rules are not in it): 16.4s of ClickHouse time per minute,
// 164ms a rule averaged over all 100, of which two heavy queries are 12.9s and
// a typical one is 68ms (p95 116ms). At the 164ms average a lane saturates near
// 360 rules at a 1m interval; at the typical 68ms, past 800. The largest env
// there declares 79 rules, so 200 leaves every env 2.5x room, and one env at the
// cap costs about 33s of a 60s lane at the average rule. The cap bounds ONE env;
// the lane is shared by an owner's envs, which is why reaching the cap is never
// silent: see Overflow and OverCapAlertName.
const DefaultMaxRulesPerEnv = 200

// DefaultLimits are conservative.
func DefaultLimits() Limits {
	return Limits{MinInterval: time.Minute, MaxExecutionTime: 20 * time.Second, MaxInstancesPerRule: 200, MaxRulesPerEnv: DefaultMaxRulesPerEnv}
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
	if l.MaxRulesPerEnv <= 0 {
		l.MaxRulesPerEnv = d.MaxRulesPerEnv
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

// Overflow is one env whose declared rules exceed Limits.MaxRulesPerEnv: the
// rules in Dropped are NOT evaluated, so no alert can fire for them.
type Overflow struct {
	Env string
	// Configured is how many rules the env declares; Cap the limit it is over.
	Configured, Cap int
	// Dropped are the rules not evaluated, in the order they were dropped.
	Dropped []Rule
}

// Select applies the per-env rule cap to rules (which should all belong to one
// owner). Within an overflowing env the rules that stay are the most severe
// first (critical, error, warning, anything else), then by name, so the drop
// is deterministic and never takes a critical rule while a warning stays.
// Other envs are untouched. kept is ordered by rule key; every dropped rule is
// reported in over, and the caller must make that loud.
func (e *Evaluator) Select(rules []Rule) (kept []Rule, over []Overflow) {
	byEnv := map[string][]Rule{}
	for _, r := range rules {
		byEnv[r.Env] = append(byEnv[r.Env], r)
	}
	envs := make([]string, 0, len(byEnv))
	for env := range byEnv {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		rs := byEnv[env]
		if len(rs) <= e.limits.MaxRulesPerEnv {
			kept = append(kept, rs...)
			continue
		}
		sort.SliceStable(rs, func(i, j int) bool {
			if ri, rj := severityRank(rs[i]), severityRank(rs[j]); ri != rj {
				return ri < rj
			}
			return rs[i].Name < rs[j].Name
		})
		kept = append(kept, rs[:e.limits.MaxRulesPerEnv]...)
		over = append(over, Overflow{Env: env, Configured: len(rs), Cap: e.limits.MaxRulesPerEnv, Dropped: rs[e.limits.MaxRulesPerEnv:]})
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].String() < kept[j].String() })
	return kept, over
}

// severityRank orders a rule for survival of the cap: lower stays.
func severityRank(r Rule) int {
	switch strings.ToLower(r.Labels["severity"]) {
	case "critical", "page":
		return 0
	case "error":
		return 1
	case "warning":
		return 2
	}
	return 3
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
	ss, err := buildSeries(rule, rows)
	if err != nil {
		return nil, err
	}
	// One line per evaluation, not per instance: a broken template fails the
	// same way on every row.
	for _, s := range ss {
		if s.annotationErr != nil {
			e.logger.WarnContext(ctx, "alert annotation template failed; sending its raw text", "rule", rule.String(), "error", s.annotationErr)
			break
		}
	}
	return ss, nil
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
	// overCap is, per env, what was last logged and posted about its overflow.
	overCap map[string]*overCapState
}

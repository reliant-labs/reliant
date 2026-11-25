// Copyright (c) 2025 Reliant Labs

// Package alerting is a SQL alert-rule evaluator in the Prometheus shape.
//
// A Rule carries an `expr` that is a single SELECT. Every row it returns is one
// firing instance: the non-value columns become the instance's labels and the
// `value` column its sample, e.g.
//
//	SELECT ServiceName AS service, quantile(0.95)(Duration)/1e6 AS value
//	FROM otel_traces GROUP BY service HAVING value > 800
//
// The package owns what is the same for every deployment: the SQL guard, the
// label rules, the inactive → pending → firing → resolved state machine with
// flap damping, a jittered scheduler sharded over forge/pkg/lease, and an
// Alertmanager v2 client. It owns nothing about WHERE rules come from, WHICH
// database an owner's query runs against, or where state is kept; those are the
// four interfaces below, declared here at the consumer.
//
// An owner is an opaque string. The package never interprets it: it is handed
// to the Querier and recorded beside every instance, nothing more. Identity
// labels (who an alert belongs to) are supplied by the RuleSource on the Rule
// and are stamped LAST, so no query column or user label can override them.
//
// A self-hoster runs it with chquery (a single-owner ClickHouse Querier), the
// pgstore Postgres StateStore (or NewMemoryStore) and any RuleSource.
package alerting

import (
	"context"
	"encoding/json"
	"time"
)

// RuleKey identifies one rule. Owner and Env are opaque to this package.
type RuleKey struct {
	Owner string
	Env   string
	Name  string
}

// String is the key as a log field.
func (k RuleKey) String() string { return k.Owner + "/" + k.Env + "/" + k.Name }

// Rule is one alert rule, as declared.
type Rule struct {
	RuleKey

	// Expr is a single ClickHouse SELECT; see ValidateExpr.
	Expr string
	// For is how long a series must keep returning before it fires. Zero fires
	// on the first evaluation that returns it.
	For time.Duration
	// Interval is the evaluation period, raised to the scheduler's MinInterval.
	Interval time.Duration
	// KeepFiringFor holds a firing alert open this long after its series stops
	// returning (Prometheus's keep_firing_for). The hold is never shorter than
	// one evaluation, which is the flap damping every rule gets.
	KeepFiringFor time.Duration

	// Source is where the rule was declared.
	Source Source

	// Labels and Annotations are the rule author's. Labels whose name starts
	// with "reliant_" are dropped, and `alertname` is always the rule's name.
	Labels      map[string]string
	Annotations map[string]string

	// Identity is set by the RuleSource, never by the rule author, and is
	// applied after every other label: it is how a consumer routes an alert to
	// the owner it belongs to.
	Identity map[string]string
}

// Row is one row a Querier returned: column name to value.
type Row map[string]any

// QueryOptions bound one evaluation. The Querier must apply them server-side.
type QueryOptions struct {
	MaxExecutionTime time.Duration
	// MaxResultRows is one more than the instance cap, so exceeding the cap
	// surfaces as an error instead of a silently truncated result.
	MaxResultRows int
}

// Querier runs one guarded SELECT for an owner.
type Querier interface {
	Query(ctx context.Context, owner, query string, opts QueryOptions) ([]Row, error)
}

// RuleSource lists what to evaluate.
type RuleSource interface {
	// Owners lists every owner that has rules.
	Owners(ctx context.Context) ([]string, error)
	// Rules lists an owner's rules across all of its envs.
	Rules(ctx context.Context, owner string) ([]Rule, error)
}

// Alert is one alert as posted to a Notifier.
type Alert struct {
	Labels      map[string]string
	Annotations map[string]string
	StartsAt    time.Time
	// EndsAt is when the receiver should consider the alert resolved if it is
	// not refreshed: now+3×interval while firing, now once resolved.
	EndsAt time.Time
	// Why is the instance's why-fired payload (see Why): structured evidence
	// that rides with the alert but is not part of its identity. The
	// Alertmanager notifier posts it as the WhyAnnotation annotation. Nil when
	// the rule's rows carried no WhyColumn.
	Why json.RawMessage
}

// Notifier delivers alerts.
type Notifier interface {
	Notify(ctx context.Context, alerts []Alert) error
}

// StateStore keeps instance state and the transition history.
type StateStore interface {
	// LoadOwner returns every stored instance of an owner, resolved included.
	LoadOwner(ctx context.Context, owner string) ([]Instance, error)
	// Owners lists owners that have stored instances.
	Owners(ctx context.Context) ([]string, error)
	// Apply persists one rule's changes atomically.
	Apply(ctx context.Context, ch Changes) error
	// Events reads the transition history, newest first.
	Events(ctx context.Context, q EventQuery) ([]Event, error)
}

// EventQuery selects history rows. Owner is required; the store never returns
// another owner's rows.
type EventQuery struct {
	Owner string
	// Env narrows to one env; empty means all of the owner's envs.
	Env string
	// Since is the oldest event wanted.
	Since time.Time
	// Limit defaults to 200 and is capped at 1000.
	Limit int
}

// Locker runs fn while holding a named, cluster-wide lease. forge/pkg/lease's
// *Locker satisfies it.
type Locker interface {
	Run(ctx context.Context, name string, fn func(ctx context.Context) error) error
}

// State is an instance's position in the state machine. An instance that is not
// stored is inactive.
type State string

const (
	StatePending  State = "pending"
	StateFiring   State = "firing"
	StateResolved State = "resolved"
)

// Instance is one label set of one rule, with its state.
type Instance struct {
	RuleKey
	Fingerprint string
	// Labels are the final labels, identity included.
	Labels      map[string]string
	Annotations map[string]string
	State       State
	// ActiveAt is when the series was first seen in its current run.
	ActiveAt time.Time
	// FiringAt is when it became firing; zero while pending.
	FiringAt time.Time
	// ClearedAt is when a firing instance first stopped returning, while flap
	// damping holds it open; zero when it is returning.
	ClearedAt  time.Time
	ResolvedAt time.Time
	// ResolveSent records that the resolved notification was delivered.
	ResolveSent bool
	LastEvalAt  time.Time
	LastValue   float64
	// Why is the latest why-fired payload the rule's row carried (WhyColumn),
	// refreshed on every evaluation. It is not a label, so it never changes
	// the instance's fingerprint.
	Why json.RawMessage
}

// InstanceRef addresses one stored instance.
type InstanceRef struct {
	RuleKey
	Fingerprint string
}

// Transition names a state change in the history.
type Transition string

const (
	TransitionPending  Transition = "pending"
	TransitionFiring   Transition = "firing"
	TransitionResolved Transition = "resolved"
	// TransitionInactive: a pending series stopped returning before `for` held.
	TransitionInactive Transition = "inactive"
	// TransitionRemoved: the rule was deleted while the instance was live.
	TransitionRemoved Transition = "removed"
)

// Event is one history row.
type Event struct {
	RuleKey
	Fingerprint string
	Labels      map[string]string
	Transition  Transition
	Value       float64
	At          time.Time
	// Why is the payload the instance carried at this transition.
	Why json.RawMessage
}

// Changes is what one evaluation step writes.
type Changes struct {
	Upsert []Instance
	Delete []InstanceRef
	Events []Event
}

func (c Changes) empty() bool {
	return len(c.Upsert) == 0 && len(c.Delete) == 0 && len(c.Events) == 0
}

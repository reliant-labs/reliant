package obsquery

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Experiment readout: how the arms of one Forge Changes experiment run
// converted, read from the per-org product_events table. The control plane's
// change readout and a local reader compile the same statement, so they cannot
// disagree. The verdict (intervals, lift, SRM) is pkg/experimentstats's; this
// file only counts.
//
// The assignment record is the evaluation itself: the SDK records
// flag_evaluated {flag, variant, provider=forge, run, unit, reason, revision}
// the first time it enrolls a unit in a run, before the treatment renders, so
// a broken treatment cannot hide from its own denominator. The readout is
// intent-to-treat:
//
//   - ASSIGNED: the distinct units with a flag_evaluated row where
//     provider = 'forge', flag = the change key, run = the run id,
//     unit = the unit kind and reason = 'experiment', in [Scope.Start,
//     Scope.End). Each unit's assignment is its first such row: its time
//     (first_at) and its variant. Preview, everyone and rollout evaluations
//     carry other reasons and never count, and a unit that was never enrolled
//     emits nothing, so it is never "control".
//   - CONFLICTED: an assigned unit whose rows in the run name more than one
//     variant, at any time in [Scope.Start, Scope.End) and in any order (an
//     id shared across devices that hashed before a fix, a tampered client).
//     A run's assignment is a pure function of the run and the unit, so a
//     second variant is never a correction: it means the record cannot be
//     trusted, two rows at one timestamp included. Such a unit is counted in
//     no arm and reported on its own: a non-zero count is a bug to find, not
//     a unit to guess about.
//   - CONVERTED: an assigned unit with at least one outcome event (Outcome,
//     optionally narrowed by OutcomeFilter) by the same unit, in the same
//     environment, with Timestamp in [first_at, first_at + WindowHours],
//     both ends inclusive. An outcome before the assignment never counts.
//   - WINDOW COMPLETE: an assigned unit whose whole window has elapsed by the
//     readout's horizon (first_at + WindowHours <= Scope.End). Only these
//     units can be compared fairly: a unit assigned an hour ago has had an
//     hour to convert, not WindowHours. The verdict reads them alone; the
//     others are pending, never non-converters.
//
// The unit column, the same on both sides of the join:
//
//   - user: UserId (observe's user.id: the auth subject, which is what the SDK
//     hashes as the OpenFeature targetingKey);
//   - visitor: VisitorId (the consented, stable visitor.id; never DayVisitor,
//     which rotates daily and cannot hold a cohort together);
//   - account: OrgId (observe's org.id: the claims' org, the browser's
//     setTelemetryOrg; tenant schema v29), else Props['account'] on an event
//     that names no org. The SDK hashes the evaluation context's `account`
//     attribute and stamps it as the `account` prop of the flag_evaluated
//     assignment, so an app that measures by account either sets that
//     attribute to the org id its events carry, or, when its accounts are
//     not orgs, tags only its OUTCOME events with an `account` prop.
//
// Every caller value (the change key, run, unit kind, outcome event and its
// filter, the window and the times) is a bound parameter: none can become SQL
// text.

// Experiment-readout limits.
const (
	// MaxRangeExperiment is the longest run a readout reads: product_events'
	// default retention. A longer run's early assignments have expired.
	MaxRangeExperiment = MaxRangeFunnel
	// MaxExperimentWindowHours is the longest attribution window (90 days).
	MaxExperimentWindowHours = 2160
	// MaxExperimentVariants bounds ExperimentReadoutQuery.Variants.
	MaxExperimentVariants = 20
)

// EventFlagEvaluated is the product event a flag evaluation records
// (web-runtime's exposure.ts, and Forge Changes' assignment record).
const EventFlagEvaluated = "flag_evaluated"

// ExperimentUnit is what stays together in an experiment: the id a unit is
// hashed, assigned and counted by.
type ExperimentUnit string

const (
	// ExperimentUnitUser counts signed-in users (UserId).
	ExperimentUnitUser ExperimentUnit = "user"
	// ExperimentUnitAccount counts accounts (OrgId, else Props['account']).
	ExperimentUnitAccount ExperimentUnit = "account"
	// ExperimentUnitVisitor counts consented anonymous visitors (VisitorId).
	ExperimentUnitVisitor ExperimentUnit = "visitor"
)

// unitExpr is the product_events expression that names a row's unit.
func (u ExperimentUnit) unitExpr() (string, bool) {
	switch u {
	case ExperimentUnitUser:
		return "UserId", true
	case ExperimentUnitVisitor:
		return "VisitorId", true
	case ExperimentUnitAccount:
		return "if(OrgId != '', OrgId, Props['account'])", true
	}
	return "", false
}

var (
	// changeKeyRE is a Forge Changes key (forge.changes/v1).
	changeKeyRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	// changeRunRE is a run id: "r_" and random base32 characters.
	changeRunRE = regexp.MustCompile(`^r_[A-Za-z0-9]{4,64}$`)
	// propKeyRE is a product event property key (Props, the product. prefix
	// stripped; a slog group flattens to "group.key").
	propKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]{0,63}$`)
)

// maxExperimentPropValueLen is observe.MaxProductPropValueLen: a longer value
// was truncated when it was recorded, so it can never match.
const maxExperimentPropValueLen = 256

// ExperimentPropFilter narrows the outcome to events whose Props[Key] equals
// Value ("plan" = "paid"). Zero = no filter.
type ExperimentPropFilter struct {
	Key, Value string
}

// ExperimentReadoutQuery reads one experiment run.
type ExperimentReadoutQuery struct {
	// Scope bounds the read: the org's database and the change's environment
	// (EnvIDs), and the run's time. Start is the run's start. End is the
	// readout's horizon: the run's end for a stopped run, else now — pass
	// min(run end, now). Assignments are read in [Start, End), outcomes in
	// [Start, End], and a unit's window is complete when it closed by End.
	// At most MaxRangeExperiment. Filters do not apply to product events and
	// are refused. Services, when set, narrows assignments AND outcomes:
	// leave it empty when a frontend records the assignment and a backend
	// the outcome.
	Scope Scope
	// ChangeKey is the change (flag) key.
	ChangeKey string
	// RunID is the run whose assignments are counted.
	RunID string
	// Unit is the run's unit kind.
	Unit ExperimentUnit
	// Variants are the run's arms in split order (optional). Each is in the
	// answer, zero-filled, in this order; a variant found that is not listed
	// follows, sorted, and is named in ExperimentReadout.Undeclared.
	Variants []string
	// Outcome is the success metric's product event name.
	Outcome string
	// OutcomeFilter optionally narrows Outcome by one property.
	OutcomeFilter ExperimentPropFilter
	// WindowHours is the attribution window after each unit's assignment,
	// 1..MaxExperimentWindowHours.
	WindowHours uint32
}

// ExperimentArm is one variant's counts. A unit counts in an arm by its
// assignment, whether or not it saw the treatment (intent-to-treat).
type ExperimentArm struct {
	Variant string
	// Assigned are the units assigned to the arm; Converted those of them
	// with an outcome in their window so far.
	Assigned, Converted int64
	// WindowComplete are the assigned units whose whole window had elapsed by
	// Scope.End; WindowCompleteConverted those of them that converted. The
	// verdict reads these two.
	WindowComplete, WindowCompleteConverted int64
	// FirstAssignedAt and LastAssignedAt bound the arm's assignments; zero
	// when it has none.
	FirstAssignedAt, LastAssignedAt time.Time
}

// ExperimentReadout is one run's counts.
type ExperimentReadout struct {
	// Arms are the query's Variants in order, then any other variant found,
	// sorted.
	Arms []ExperimentArm
	// Undeclared are the variants found that the query did not list (empty
	// when Variants was not given): assignments to an arm the run does not
	// have.
	Undeclared []string
	// Totals over Arms. Conflicted units are in none of them.
	Assigned, Converted, WindowComplete, WindowCompleteConverted int64
	// Conflicted are the units assigned to more than one variant in the run,
	// counted in no arm.
	Conflicted int64
	// FirstAssignedAt and LastAssignedAt bound every assignment read,
	// conflicted units included; zero when there is none.
	FirstAssignedAt, LastAssignedAt time.Time
}

// Pending is the assigned units whose window is still open: counted, never as
// non-converters.
func (r ExperimentReadout) Pending() int64 { return r.Assigned - r.WindowComplete }

func (q ExperimentReadoutQuery) validate() error {
	if err := q.Scope.validate(MaxRangeExperiment); err != nil {
		return err
	}
	if len(q.Scope.Filters) > 0 {
		return invalid("scope filters narrow telemetry attributes and do not apply to an experiment readout")
	}
	if !changeKeyRE.MatchString(q.ChangeKey) {
		return invalid("change key %q is not a change key (^[a-z][a-z0-9_.-]{0,63}$)", q.ChangeKey)
	}
	if !changeRunRE.MatchString(q.RunID) {
		return invalid("run id %q is not a run id (r_ and 4-64 letters or digits)", q.RunID)
	}
	if _, ok := q.Unit.unitExpr(); !ok {
		return invalid("unit %q must be user, account or visitor", q.Unit)
	}
	if len(q.Variants) > MaxExperimentVariants {
		return invalid("at most %d variants", MaxExperimentVariants)
	}
	seen := map[string]bool{}
	for _, v := range q.Variants {
		if v == "" || len(v) > 128 || !utf8.ValidString(v) {
			return invalid("variant %q must be 1-128 bytes of UTF-8", v)
		}
		if seen[v] {
			return invalid("variant %q is listed twice", v)
		}
		seen[v] = true
	}
	if !activityEventRE.MatchString(q.Outcome) {
		return invalid("outcome %q is not an event name", q.Outcome)
	}
	if f := q.OutcomeFilter; f != (ExperimentPropFilter{}) {
		if !propKeyRE.MatchString(f.Key) {
			return invalid("outcome filter key %q is not a property key", f.Key)
		}
		if f.Value == "" || utf8.RuneCountInString(f.Value) > maxExperimentPropValueLen || !utf8.ValidString(f.Value) {
			return invalid("outcome filter value must be 1-%d characters of UTF-8", maxExperimentPropValueLen)
		}
	}
	if q.WindowHours < 1 || q.WindowHours > MaxExperimentWindowHours {
		return invalid("window_hours must be 1-%d", MaxExperimentWindowHours)
	}
	return nil
}

// Compile builds the statement. Its rows are
//
//	kind, variant, assigned, converted, window_complete, window_complete_converted, first_at, last_at
//
// with kind arm (one row per variant found) and conflicted (one row: the
// conflicted units in assigned, and every assignment's first_at and last_at).
func (q ExperimentReadoutQuery) Compile() (Stmt, error) {
	if err := q.validate(); err != nil {
		return Stmt{}, err
	}
	s := q.Scope
	b := newBuilder(s)
	unit, _ := q.Unit.unitExpr()
	table := b.table(s, tblProduct)

	var scope []string
	if e := b.browserEnvCond("EnvId", s); e != "" {
		scope = append(scope, e)
	}
	if sv := b.serviceIn(s.Services); sv != "" {
		scope = append(scope, sv)
	}
	if e := b.browserInstanceCond(s); e != "" {
		scope = append(scope, e)
	}
	scopeSQL := ""
	if len(scope) > 0 {
		scopeSQL = " AND " + strings.Join(scope, " AND ")
	}
	from, to := b.nanoTime(s.Start), b.nanoTime(s.End)
	window := "toIntervalHour(" + b.u32(q.WindowHours) + ")"

	assigned := "EventName = '" + EventFlagEvaluated + "' AND Props['provider'] = 'forge' AND Props['reason'] = 'experiment'" +
		" AND Props['flag'] = " + b.str(q.ChangeKey) + " AND Props['run'] = " + b.str(q.RunID) +
		" AND Props['unit'] = " + b.str(string(q.Unit))
	outcome := "EventName = " + b.str(q.Outcome)
	if f := q.OutcomeFilter; f != (ExperimentPropFilter{}) {
		outcome += " AND Props[" + b.str(f.Key) + "] = " + b.str(f.Value)
	}

	sql := "WITH\n" +
		// One row per (environment, unit): its first assignment's time, how
		// many distinct variants its rows in the run name, and its variant
		// when that is exactly one. A unit with two is conflicted whenever
		// the second appears (CONFLICTED above), so its variant is never
		// picked: no tie between rows at one timestamp can choose an arm.
		"asg AS (\n" +
		"  SELECT EnvId, " + unit + " AS unit, min(Timestamp) AS first_at,\n" +
		"         uniqExact(Props['variant']) AS variants, if(variants = 1, any(Props['variant']), '') AS variant\n" +
		"  FROM " + table + "\n" +
		"  WHERE " + assigned + "\n" +
		"    AND " + unit + " != '' AND Timestamp >= " + from + " AND Timestamp < " + to + scopeSQL + "\n" +
		"  GROUP BY EnvId, unit),\n" +
		"outcome AS (\n" +
		"  SELECT EnvId, " + unit + " AS unit, Timestamp\n" +
		"  FROM " + table + "\n" +
		"  WHERE " + outcome + "\n" +
		"    AND " + unit + " != '' AND Timestamp >= " + from + " AND Timestamp <= " + to + scopeSQL + "),\n" +
		// One row per assigned unit: its arm, whether it converted in its
		// window and whether that window is complete. countIf is false for an
		// unmatched row under either join_use_nulls: the default timestamp
		// precedes the assignment, and NULL is not true. The columns are
		// named apart from the output's so no alias shadows an aggregate's
		// argument.
		"units AS (\n" +
		"  SELECT any(a.variant) AS arm, any(a.variants) AS n_variants, any(a.first_at) AS assigned_at,\n" +
		"         toUInt8(countIf(o.Timestamp >= a.first_at AND o.Timestamp <= a.first_at + " + window + ") > 0) AS conv,\n" +
		"         toUInt8(any(a.first_at) + " + window + " <= " + to + ") AS done\n" +
		"  FROM asg AS a LEFT JOIN outcome AS o ON o.EnvId = a.EnvId AND o.unit = a.unit\n" +
		"  GROUP BY a.EnvId, a.unit)\n" +
		"SELECT 'arm' AS kind, arm AS variant, count() AS assigned, countIf(conv = 1) AS converted,\n" +
		"       countIf(done = 1) AS window_complete, countIf(done = 1 AND conv = 1) AS window_complete_converted,\n" +
		"       min(assigned_at) AS first_at, max(assigned_at) AS last_at\n" +
		"FROM units WHERE n_variants = 1 GROUP BY arm\n" +
		"UNION ALL\n" +
		"SELECT 'conflicted' AS kind, '' AS variant, countIf(n_variants > 1) AS assigned, toUInt64(0) AS converted,\n" +
		"       toUInt64(0) AS window_complete, toUInt64(0) AS window_complete_converted,\n" +
		"       min(assigned_at) AS first_at, max(assigned_at) AS last_at\n" +
		"FROM units"
	return b.stmt(sql), nil
}

// Parse reads the rows of Compile's statement. The caller closes rows.
func (q ExperimentReadoutQuery) Parse(rows Rows) (ExperimentReadout, error) {
	byVariant := map[string]*ExperimentArm{}
	var out ExperimentReadout
	for _, v := range q.Variants {
		byVariant[v] = &ExperimentArm{Variant: v}
	}
	var extra []string
	for rows.Next() {
		var (
			kind, variant                                    string
			assigned, converted, complete, completeConverted uint64
			firstAt, lastAt                                  time.Time
		)
		if err := rows.Scan(&kind, &variant, &assigned, &converted, &complete, &completeConverted, &firstAt, &lastAt); err != nil {
			return ExperimentReadout{}, err
		}
		// An aggregate over no rows answers the epoch: no assignment.
		firstAt, lastAt = decidedFrom(firstAt), decidedFrom(lastAt)
		switch kind {
		case "arm":
			a, ok := byVariant[variant]
			if !ok {
				a = &ExperimentArm{Variant: variant}
				byVariant[variant] = a
				extra = append(extra, variant)
			}
			a.Assigned, a.Converted = int64(assigned), int64(converted)
			a.WindowComplete, a.WindowCompleteConverted = int64(complete), int64(completeConverted)
			a.FirstAssignedAt, a.LastAssignedAt = firstAt, lastAt
		case "conflicted":
			out.Conflicted = int64(assigned)
			out.FirstAssignedAt, out.LastAssignedAt = firstAt, lastAt
		}
	}
	if err := rows.Err(); err != nil {
		return ExperimentReadout{}, err
	}
	sort.Strings(extra)
	order := append(append([]string{}, q.Variants...), extra...)
	out.Arms = make([]ExperimentArm, 0, len(order))
	for _, v := range order {
		a := *byVariant[v]
		out.Arms = append(out.Arms, a)
		out.Assigned += a.Assigned
		out.Converted += a.Converted
		out.WindowComplete += a.WindowComplete
		out.WindowCompleteConverted += a.WindowCompleteConverted
	}
	out.Undeclared = []string{}
	if len(q.Variants) > 0 {
		out.Undeclared = append(out.Undeclared, extra...)
	}
	return out, nil
}

// ReadExperimentReadout compiles q, runs it on run and parses the answer.
func ReadExperimentReadout(ctx context.Context, run Runner, q ExperimentReadoutQuery) (ExperimentReadout, error) {
	st, err := q.Compile()
	if err != nil {
		return ExperimentReadout{}, err
	}
	rows, err := run(ctx, st)
	if err != nil {
		return ExperimentReadout{}, err
	}
	defer rows.Close()
	return q.Parse(rows)
}

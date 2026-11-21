package obsquery

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

func experimentQuery() ExperimentReadoutQuery {
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return ExperimentReadoutQuery{
		Scope:     Scope{Database: "t_org", EnvRestricted: true, EnvIDs: []string{"env-prod"}, Start: start, End: start.Add(14 * 24 * time.Hour)},
		ChangeKey: "new-checkout", RunID: "r_8f2cq7xa", Unit: ExperimentUnitUser,
		Variants: []string{"control", "treatment"},
		Outcome:  "order_placed", OutcomeFilter: ExperimentPropFilter{Key: "plan", Value: "paid"},
		WindowHours: 72,
	}
}

func TestExperimentReadoutStatementIsGolden(t *testing.T) {
	st, err := experimentQuery().Compile()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "experiment_readout.golden.sql", st.SQL+"\n")
}

// Every caller value is a bound parameter: none appears in the SQL text, and
// each placeholder the statement names is bound.
func TestExperimentReadoutBindsEveryValue(t *testing.T) {
	q := experimentQuery()
	// Values chosen to be distinctive in SQL text.
	q.ChangeKey, q.RunID = "zz-key.v2", "r_qqqq7777"
	q.Outcome = "zz_outcome_evt"
	q.OutcomeFilter = ExperimentPropFilter{Key: "zz_prop", Value: "x' OR 1=1 --"}
	q.Scope.EnvIDs = []string{"zz-env-1", "zz-env-2"}
	q.Scope.Services = []string{"zz-svc"}
	q.WindowHours = 1999
	for _, unit := range []ExperimentUnit{ExperimentUnitUser, ExperimentUnitAccount, ExperimentUnitVisitor} {
		q.Unit = unit
		st, err := q.Compile()
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{q.ChangeKey, q.RunID, q.Outcome, q.OutcomeFilter.Key, q.OutcomeFilter.Value, "zz-env-1", "zz-svc", "1999"} {
			if strings.Contains(st.SQL, v) {
				t.Errorf("%s: the value %q is in the SQL text", unit, v)
			}
		}
		if !strings.Contains(st.SQL, "Props['unit'] = {p") {
			t.Errorf("%s: the unit kind is not compared to a parameter", unit)
		}
		bound := map[string]bool{}
		for _, v := range st.Params {
			bound[v] = true
		}
		for _, v := range []string{q.ChangeKey, q.RunID, q.Outcome, q.OutcomeFilter.Key, q.OutcomeFilter.Value, string(unit), "1999"} {
			if !bound[v] {
				t.Errorf("%s: %q is not a bound parameter: %v", unit, v, st.Params)
			}
		}
		for _, m := range regexp.MustCompile(`\{(p\d+):`).FindAllStringSubmatch(st.SQL, -1) {
			if _, ok := st.Params[m[1]]; !ok {
				t.Errorf("%s: placeholder %s is not bound", unit, m[1])
			}
		}
		if len(st.Params) != len(regexp.MustCompile(`\{p\d+:`).FindAllString(dedupe(st.SQL), -1)) {
			t.Errorf("%s: a bound parameter is unused: %v", unit, st.Params)
		}
	}
}

// dedupe keeps the first occurrence of each placeholder in sql.
func dedupe(sql string) string {
	seen := map[string]bool{}
	var out []string
	for _, m := range regexp.MustCompile(`\{p\d+:[A-Za-z0-9()]+\}`).FindAllString(sql, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return strings.Join(out, " ")
}

func TestExperimentReadoutUnitColumn(t *testing.T) {
	for unit, want := range map[ExperimentUnit]string{
		ExperimentUnitUser:    "SELECT EnvId, UserId AS unit",
		ExperimentUnitVisitor: "SELECT EnvId, VisitorId AS unit",
		ExperimentUnitAccount: "SELECT EnvId, if(OrgId != '', OrgId, Props['account']) AS unit",
	} {
		q := experimentQuery()
		q.Unit = unit
		st, err := q.Compile()
		if err != nil {
			t.Fatal(err)
		}
		// The same column on both sides of the join.
		if n := strings.Count(st.SQL, want); n != 2 {
			t.Errorf("%s: %q appears %d times, want 2 (assignments and outcomes)", unit, want, n)
		}
	}
	q := experimentQuery()
	q.OutcomeFilter = ExperimentPropFilter{}
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, "Props[{") {
		t.Error("no outcome filter compiled a property predicate")
	}
}

func TestExperimentReadoutRefusesWhatItCannotBound(t *testing.T) {
	for name, mutate := range map[string]func(*ExperimentReadoutQuery){
		"window zero":           func(q *ExperimentReadoutQuery) { q.WindowHours = 0 },
		"window past 90 days":   func(q *ExperimentReadoutQuery) { q.WindowHours = MaxExperimentWindowHours + 1 },
		"unit":                  func(q *ExperimentReadoutQuery) { q.Unit = "session" },
		"change key":            func(q *ExperimentReadoutQuery) { q.ChangeKey = "New|Checkout" },
		"run id":                func(q *ExperimentReadoutQuery) { q.RunID = "8f2c' --" },
		"outcome":               func(q *ExperimentReadoutQuery) { q.Outcome = "order placed" },
		"filter key":            func(q *ExperimentReadoutQuery) { q.OutcomeFilter.Key = "plan']" },
		"filter key, no value":  func(q *ExperimentReadoutQuery) { q.OutcomeFilter.Value = "" },
		"filter value too long": func(q *ExperimentReadoutQuery) { q.OutcomeFilter.Value = strings.Repeat("x", 257) },
		"duplicate variant":     func(q *ExperimentReadoutQuery) { q.Variants = []string{"a", "a"} },
		"empty variant":         func(q *ExperimentReadoutQuery) { q.Variants = []string{""} },
		"range past retention":  func(q *ExperimentReadoutQuery) { q.Scope.End = q.Scope.Start.Add(MaxRangeExperiment + time.Hour) },
		"filters":               func(q *ExperimentReadoutQuery) { q.Scope.Filters = []AttrFilter{{Key: "service.name", Value: "x"}} },
		"default database":      func(q *ExperimentReadoutQuery) { q.Scope.Database = "default" },
	} {
		q := experimentQuery()
		mutate(&q)
		if _, err := q.Compile(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	q := experimentQuery()
	q.WindowHours = MaxExperimentWindowHours
	q.Variants = nil
	if _, err := q.Compile(); err != nil {
		t.Errorf("a 90-day window with no variants listed: %v", err)
	}
}

func TestExperimentReadoutParse(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 1, h, 0, 0, 0, time.UTC) }
	rows := &fakeRows{rows: [][]any{
		{"arm", "treatment", uint64(40), uint64(9), uint64(30), uint64(8), at(2), at(9)},
		{"arm", "stale", uint64(1), uint64(0), uint64(1), uint64(0), at(3), at(3)},
		{"conflicted", "", uint64(2), uint64(0), uint64(0), uint64(0), at(1), at(10)},
	}}
	got, err := experimentQuery().Parse(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Arms) != 3 || got.Arms[0].Variant != "control" || got.Arms[1].Variant != "treatment" || got.Arms[2].Variant != "stale" {
		t.Fatalf("arms %+v, want control (zero-filled), treatment, stale", got.Arms)
	}
	if got.Arms[0].Assigned != 0 || !got.Arms[0].FirstAssignedAt.IsZero() {
		t.Errorf("control = %+v, want zero", got.Arms[0])
	}
	if a := got.Arms[1]; a.Assigned != 40 || a.Converted != 9 || a.WindowComplete != 30 || a.WindowCompleteConverted != 8 || !a.FirstAssignedAt.Equal(at(2)) {
		t.Errorf("treatment = %+v", a)
	}
	if got.Assigned != 41 || got.Converted != 9 || got.WindowComplete != 31 || got.WindowCompleteConverted != 8 || got.Pending() != 10 {
		t.Errorf("totals %+v, pending %d", got, got.Pending())
	}
	if got.Conflicted != 2 || !got.FirstAssignedAt.Equal(at(1)) || !got.LastAssignedAt.Equal(at(10)) {
		t.Errorf("conflicted %d, assignments [%v, %v]", got.Conflicted, got.FirstAssignedAt, got.LastAssignedAt)
	}
	if len(got.Undeclared) != 1 || got.Undeclared[0] != "stale" {
		t.Errorf("undeclared %v, want [stale]", got.Undeclared)
	}
	// An empty run: the conflicted row's aggregates over nothing are the epoch.
	empty, err := experimentQuery().Parse(&fakeRows{rows: [][]any{{"conflicted", "", uint64(0), uint64(0), uint64(0), uint64(0), time.Unix(0, 0), time.Unix(0, 0)}}})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Assigned != 0 || !empty.FirstAssignedAt.IsZero() || len(empty.Arms) != 2 || len(empty.Undeclared) != 0 {
		t.Errorf("empty readout %+v", empty)
	}
}

package dbanalyze_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/dbanalyze"
	"github.com/reliant-labs/forge/pkg/querycapture"
)

// Query and Finding were comparable in v1.7.0 and are public v1 API: a
// caller may use them as map keys or compare them with ==. Their slices live
// behind the embedded *QueryDetail and *FindingQuery.
func TestDocumentTypesStayComparable(t *testing.T) {
	for _, v := range []any{dbanalyze.Query{}, dbanalyze.Finding{}} {
		if typ := reflect.TypeOf(v); !typ.Comparable() {
			t.Errorf("%s is not comparable", typ)
		}
	}
}

// NonConcurrently is deprecated but keeps its v1.7.0 behaviour.
func TestNonConcurrently(t *testing.T) {
	st := dbanalyze.SplitStatements("-- x\ncreate unique index  concurrently if not exists i ON t (a);")[0]
	got, ok := st.NonConcurrently()
	if !ok || got != "create unique index  if not exists i ON t (a)" {
		t.Errorf("NonConcurrently = %q, %v", got, ok)
	}
	if _, ok := dbanalyze.SplitStatements("CREATE INDEX i ON t (a)")[0].NonConcurrently(); ok {
		t.Error("a plain CREATE INDEX was rewritten")
	}
}

// The forge.db-analyze/v1 document is what reviewers (control-plane's
// pr-review, scripts/pr-pipeline/db-analyze.sh) read with jq: the embedded
// detail structs must flatten into the query's and finding's own objects —
// queries[].tables/callers/access, findings[].query/callers — and a
// migration finding carries neither key. Decoding round-trips the shape.
func TestDocumentJSONShape(t *testing.T) {
	callers := []querycapture.CallSite{{Caller: querycapture.Frame{File: "internal/db/a.go", Line: 4, Function: "db.A"}, Tests: []string{"TestA"}, Count: 1}}
	doc := dbanalyze.NewDocument()
	doc.Queries = []dbanalyze.Query{{
		Fingerprint: "fp", SQL: "SELECT id FROM accounts WHERE email = $1", Operation: "SELECT",
		QueryDetail: &dbanalyze.QueryDetail{
			Tables: []string{"accounts"}, Callers: callers,
			Access: []dbanalyze.TableAccess{{Table: "accounts", Verdict: dbanalyze.AccessUnserved, How: dbanalyze.UnservedFilter, Scan: "Seq Scan", SeqScan: true}},
		},
		Plan:          &dbanalyze.PlanNode{NodeType: "Seq Scan", Relation: "accounts"},
		PlanNoSeqScan: &dbanalyze.PlanNode{NodeType: "Seq Scan", Relation: "accounts", Disabled: true},
	}}
	doc.Findings = []dbanalyze.Finding{
		{Rule: dbanalyze.RuleQueryNoUsableIndex, Severity: "warning", File: "internal/db/a.go", Line: 4, Relation: "accounts", Message: "m",
			FindingQuery: &dbanalyze.FindingQuery{Query: "fp", Callers: callers}},
		{Rule: dbanalyze.RuleTableRewrite, Severity: "error", File: "db/migrations/2_x.up.sql", Line: 1, Relation: "orders", Message: "m"},
	}
	doc.Finalize()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Queries  []map[string]json.RawMessage `json:"queries"`
		Findings []map[string]json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	keys := func(o map[string]json.RawMessage) string {
		var ks []string
		for k := range o {
			ks = append(ks, k)
		}
		slices.Sort(ks)
		return strings.Join(ks, ",")
	}
	if got := keys(m.Queries[0]); got != "access,callers,fingerprint,operation,plan,plan_no_seqscan,sql,tables" {
		t.Errorf("queries[0] keys = %s", got)
	}
	if got := keys(m.Findings[0]); got != "callers,file,line,message,query,relation,rule,severity" {
		t.Errorf("a query finding's keys = %s", got)
	}
	if got := keys(m.Findings[1]); got != "file,line,message,relation,rule,severity" {
		t.Errorf("a migration finding's keys = %s", got)
	}
	if got := string(m.Findings[0]["callers"]); !strings.Contains(got, `"caller":{"file":"internal/db/a.go","line":4`) {
		t.Errorf("findings[0].callers = %s", got)
	}
	if got := string(m.Queries[0]["access"]); !strings.Contains(got, `"verdict":"unserved"`) {
		t.Errorf("queries[0].access = %s", got)
	}

	var back dbanalyze.Document
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	q, f := back.Queries[0], back.Findings
	if q.QueryDetail == nil || len(q.Callers) != 1 || q.Access[0].Verdict != dbanalyze.AccessUnserved || q.Tables[0] != "accounts" {
		t.Errorf("decoded query = %+v", q)
	}
	if f[0].FindingQuery == nil || f[0].Query != "fp" || len(f[0].Callers) != 1 || f[1].FindingQuery != nil {
		t.Errorf("decoded findings = %+v / %+v", f[0], f[1])
	}
}

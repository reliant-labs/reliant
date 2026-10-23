package obsbackend

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)

func sp(id, parent, svc, name string, startMs, durMs int, status string) Span {
	return Span{SpanID: id, ParentID: parent, Service: svc, Name: name, Start: t0.Add(time.Duration(startMs) * time.Millisecond),
		DurationNs: int64(durMs) * int64(time.Millisecond), StatusCode: status}
}

func find(n []*TreeNode, name string) *TreeNode {
	for _, x := range n {
		if x.Span.Name == name {
			return x
		}
	}
	return nil
}

func TestTreeCollapsesRunsOfIdenticalSiblings(t *testing.T) {
	spans := []Span{sp("r", "", "api", "GET /jobs", 0, 100, "unset")}
	for i := 0; i < 37; i++ {
		spans = append(spans, sp(fmt.Sprintf("q%d", i), "r", "api", "SELECT jobs", i, 2+i%8, "unset"))
	}
	spans = append(spans, sp("x", "r", "api", "render", 60, 30, "unset"))
	tr := BuildTree(spans, false)
	root := tr.Roots[0]
	run := find(root.Children, "SELECT jobs")
	if run == nil || run.Count != 37 {
		t.Fatalf("want one ×37 node, children = %+v", root.Children)
	}
	if run.Min != 2*time.Millisecond || run.Max != 9*time.Millisecond {
		t.Errorf("min/max = %s/%s", run.Min, run.Max)
	}
	if run.P50 < run.Min || run.P95 > run.Max || run.P95 < run.P50 {
		t.Errorf("percentiles out of order: p50 %s p95 %s", run.P50, run.P95)
	}
	if len(root.Children) != 2 {
		t.Errorf("a collapsed run and render: %d children", len(root.Children))
	}
	if root.Hidden != 0 {
		t.Errorf("a ×N run accounts for its members; nothing is hidden when they have no children: %d", root.Hidden)
	}
	if tr.Spans != 39 || tr.Shown != 3 {
		t.Errorf("spans %d shown %d, want 39 and 3", tr.Spans, tr.Shown)
	}
}

func TestTreeCountsWhatHangsUnderACollapsedRun(t *testing.T) {
	spans := []Span{sp("r", "", "api", "root", 0, 100, "unset")}
	for i := 0; i < 5; i++ {
		spans = append(spans, sp(fmt.Sprintf("q%d", i), "r", "api", "SELECT", i, 3, "unset"))
		spans = append(spans, sp(fmt.Sprintf("n%d", i), fmt.Sprintf("q%d", i), "db", "net", i, 1, "unset"))
	}
	// The sibling that ends last is the critical path and stays on its own.
	spans = append(spans, sp("tail", "r", "api", "render", 50, 40, "unset"))
	tr := BuildTree(spans, false)
	root := tr.Roots[0]
	if len(root.Children) != 2 || root.Children[0].Count != 5 {
		t.Fatalf("children = %+v", root.Children)
	}
	if root.Hidden != 5 || !tr.Capped {
		t.Errorf("the 5 spans under the run's members are not rendered and must be counted: hidden %d capped %v", root.Hidden, tr.Capped)
	}
}

func TestTreeDoesNotCollapseDifferentSiblings(t *testing.T) {
	spans := []Span{
		sp("r", "", "api", "GET", 0, 50, "unset"),
		sp("a", "r", "api", "SELECT jobs", 1, 5, "unset"),
		sp("b", "r", "api", "SELECT crews", 7, 5, "unset"),
		sp("c", "r", "worker", "SELECT jobs", 13, 5, "unset"),
	}
	if got := len(BuildTree(spans, false).Roots[0].Children); got != 3 {
		t.Errorf("different name or service must stay separate, got %d children", got)
	}
}

func TestTreeKeepsErrorSpansExpandedInsideARun(t *testing.T) {
	spans := []Span{sp("r", "", "api", "GET", 0, 100, "unset")}
	for i := 0; i < 20; i++ {
		st := "unset"
		if i == 13 {
			st = "error"
		}
		spans = append(spans, sp(fmt.Sprintf("q%d", i), "r", "api", "SELECT", i, 3, st))
	}
	root := BuildTree(spans, false).Roots[0]
	var errNode *TreeNode
	runs := 0
	for _, c := range root.Children {
		if c.Error {
			errNode = c
		}
		if c.Count > 1 {
			runs++
		}
	}
	if errNode == nil || errNode.Span.SpanID != "q13" || errNode.Count != 1 {
		t.Fatalf("the failing span keeps its own node: %+v", root.Children)
	}
	if runs != 1 {
		t.Errorf("the 19 healthy siblings still collapse into one run, got %d runs", runs)
	}
}

func TestTreeDepthCapHidesBelowButCountsIt(t *testing.T) {
	var spans []Span
	parent := ""
	for i := 1; i <= 12; i++ {
		id := fmt.Sprintf("s%d", i)
		spans = append(spans, sp(id, parent, "api", fmt.Sprintf("level%d", i), i, 100-i, "unset"))
		parent = id
	}
	tr := BuildTree(spans, false)
	depth, n := 0, tr.Roots[0]
	for ; len(n.Children) > 0; n = n.Children[0] {
		depth++
	}
	if depth+1 != MaxTreeDepth {
		t.Errorf("rendered depth %d, want %d", depth+1, MaxTreeDepth)
	}
	if n.Hidden != 12-MaxTreeDepth || !tr.Capped {
		t.Errorf("hidden %d capped %v, want %d and true", n.Hidden, tr.Capped, 12-MaxTreeDepth)
	}
	full := BuildTree(spans, true)
	if full.Capped || full.Shown != 12 {
		t.Errorf("--full lifts the cap: capped %v shown %d", full.Capped, full.Shown)
	}
}

func TestTreeSpanCapKeepsErrorsAndTheCriticalPath(t *testing.T) {
	spans := []Span{sp("r", "", "api", "root", 0, 1000, "unset")}
	// 300 distinct children (distinct names: nothing collapses).
	for i := 0; i < 300; i++ {
		spans = append(spans, sp(fmt.Sprintf("c%d", i), "r", "api", fmt.Sprintf("op%03d", i), i, 1, "unset"))
	}
	spans = append(spans, sp("late", "r", "api", "slowest", 400, 500, "unset")) // ends last: critical
	spans = append(spans, sp("bad", "r", "db", "failing", 150, 1, "error"))
	tr := BuildTree(spans, false)
	if tr.Shown > MaxTreeSpans+2 {
		t.Errorf("shown %d spans, the cap is %d (plus the always-expanded ones)", tr.Shown, MaxTreeSpans)
	}
	root := tr.Roots[0]
	if find(root.Children, "failing") == nil {
		t.Error("an error span is always expanded")
	}
	late := find(root.Children, "slowest")
	if late == nil || !late.Critical {
		t.Errorf("the critical path is always expanded and marked: %+v", late)
	}
	if root.Hidden == 0 || !tr.Capped {
		t.Errorf("the cut must be counted: hidden %d capped %v", root.Hidden, tr.Capped)
	}
	if tr := BuildTree(spans, true); tr.Shown != len(spans) || tr.Capped {
		t.Errorf("--full shows all %d: shown %d capped %v", len(spans), tr.Shown, tr.Capped)
	}
}

func TestTreeCriticalLeafFoldsIntoItsRunAndMarksIt(t *testing.T) {
	spans := []Span{sp("r", "", "api", "root", 0, 100, "unset")}
	for i := 0; i < 12; i++ {
		spans = append(spans, sp(fmt.Sprintf("q%d", i), "r", "api", "SELECT", i*5, 2, "unset"))
	}
	root := BuildTree(spans, false).Roots[0]
	if len(root.Children) != 1 || root.Children[0].Count != 12 || !root.Children[0].Critical {
		t.Fatalf("the last select is the critical leaf; it folds into the ×12 run and marks it: %+v", root.Children)
	}
}

func TestTreeAncestorsOfAnErrorStayExpanded(t *testing.T) {
	spans := []Span{sp("r", "", "api", "root", 0, 100, "unset")}
	for i := 0; i < 150; i++ {
		spans = append(spans, sp(fmt.Sprintf("c%d", i), "r", "api", fmt.Sprintf("op%03d", i), i, 1, "unset"))
	}
	spans = append(spans,
		sp("mid", "r", "api", "middle", 500, 2, "unset"),
		sp("leaf", "mid", "db", "boom", 501, 1, "error"))
	root := BuildTree(spans, false).Roots[0]
	mid := find(root.Children, "middle")
	if mid == nil || find(mid.Children, "boom") == nil {
		t.Fatalf("the path to an error span must stay expanded: %+v", mid)
	}
}

func TestTreeOrphansAndEmpty(t *testing.T) {
	spans := []Span{sp("r", "", "api", "root", 0, 10, "unset"), sp("o", "missing", "worker", "orphan", 5, 2, "unset")}
	tr := BuildTree(spans, false)
	if len(tr.Roots) != 2 || tr.Orphans != 1 {
		t.Errorf("a span whose parent was never exported is its own root: %+v", tr)
	}
	if empty := BuildTree(nil, false); len(empty.Roots) != 0 || empty.Spans != 0 {
		t.Errorf("empty: %+v", empty)
	}
}

func TestErrorSpansIncludeExceptionEvents(t *testing.T) {
	a := sp("a", "r", "api", "ok", 0, 1, "unset")
	b := sp("b", "r", "api", "throws", 5, 1, "unset")
	b.Events = []SpanEvent{{Name: "exception"}}
	c := sp("c", "r", "api", "failed", 2, 1, "error")
	got := ErrorSpans([]Span{a, b, c})
	if len(got) != 2 || got[0].SpanID != "c" || got[1].SpanID != "b" {
		t.Errorf("want c then b: %+v", got)
	}
}

func TestSQLShapeRemovesLiteralsKeepsStructure(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT count() FROM otel_logs WHERE ServiceName = 'api' AND SeverityNumber >= 17 -- why\n LIMIT 10",
			"select count() from otel_logs where ServiceName = ? and SeverityNumber >= ? limit ?"},
		{"select  a,\n  b  FROM t  WHERE x IN ('a','b') /* c */ AND y = {p1:String}",
			"select a, b from t where x in (?,?) and y = {p1:String}"},
		{"SELECT 1.5e3, 'it''s', 'a\\'b' FROM t", "select ?, ?, ? from t"},
		{"SELECT * FROM otel_traces WHERE TraceId = '0af7651916cd43dd8448eb211c80319c' AND Duration > 1000000",
			"select * from otel_traces where TraceId = ? and Duration > ?"},
		{"SELECT quantile(0.95)(Duration) FROM otel_traces", "select quantile(?)(Duration) from otel_traces"},
		{"SELECT map['http.route'] FROM otel_logs", "select map[?] from otel_logs"},
	}
	for _, c := range cases {
		if got := SQLShape(c.in); got != c.want {
			t.Errorf("SQLShape(%q)\n got  %q\n want %q", c.in, got, c.want)
		}
	}
}

func TestSQLShapeNeverCarriesALiteral(t *testing.T) {
	for _, secret := range []string{"0af7651916cd43dd8448eb211c80319c", "alice@example.com", "hunter2", "8675309"} {
		got := SQLShape("SELECT * FROM t WHERE a = '" + secret + "' AND b IN ('x', '" + secret + "') AND TraceId = '" + secret + "'")
		if strings.Contains(got, secret) {
			t.Errorf("a string literal leaked into the shape: %s", got)
		}
	}
	for _, secret := range []string{"8675309", "0x1F2E3D", "4e5", "123abc"} {
		if got := SQLShape("SELECT * FROM t WHERE id = " + secret + " AND n > " + secret); strings.Contains(got, secret) || strings.Contains(got, "abc") {
			t.Errorf("a number leaked into the shape: %s", got)
		}
	}
}

func TestCmdQuoting(t *testing.T) {
	if got := Cmd("forge", "obs", "logs", "dev", "service:api AND x"); got != `forge obs logs dev 'service:api AND x'` {
		t.Errorf("got %s", got)
	}
}

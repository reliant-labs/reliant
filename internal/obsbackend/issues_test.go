package obsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const testFP = "0123456789abcdef"

func jnum(s string) json.Number { return json.Number(s) }

func issueRow(fp string) []any {
	return []any{fp, "api", "*pgconn.PgError: duplicate key <str>", "internal/store/db.go in store.(*DB).Exec", "*pgconn.PgError",
		jnum("7"), jnum("3"), "2026-10-22 10:00:00.000000000", "2026-10-22 11:30:00.000000000", "0af7651916cd43dd8448eb211c80319c", "v1.4.0", "v1.3.0", "", "users"}
}

func eventRow(ts, trace string) []any {
	return []any{ts, trace, "b7ad6b7169203331", "api", "v1.4.0", "insert failed", map[string]any{
		"code.function.name": "store.(*DB).Exec", "code.file.path": "internal/store/db.go", "code.line.number": "88",
		"exception.stacktrace": strings.Repeat("x", 2000),
	}}
}

func localIssues(answers ...fakeAnswer) (Local, *fakeRunner) {
	fr := &fakeRunner{answers: answers}
	return Local{Env: "dev", Runner: fr}, fr
}

func TestLocalListIssuesIsStatelessAndKeyedBySfp1(t *testing.T) {
	l, fr := localIssues(fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{issueRow(testFP), issueRow("aaaaaaaaaaaaaaaa")}}})
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || !res.Truncated {
		t.Fatalf("limit 1 of 2 rows: issues=%d truncated=%v", len(res.Issues), res.Truncated)
	}
	i := res.Issues[0]
	if i.ID != "sfp1-"+testFP || i.Status != IssueUnresolved || i.Platform != PlatformServer || i.Events != 7 || i.Affected != 3 ||
		i.LatestTraceID != "0af7651916cd43dd8448eb211c80319c" || i.LastVersion != "v1.4.0" || i.FirstVersion != "v1.3.0" || i.Version != 0 {
		t.Errorf("issue = %+v", i)
	}
	sql := fr.ran[0].SQL
	if !strings.Contains(sql, obsquery.ServerFingerprintSQL) || !strings.Contains(sql, "LIMIT 2") {
		t.Errorf("statement:\n%s", sql)
	}
}

func TestLocalListIssuesOfASource(t *testing.T) {
	row := issueRow(testFP)
	row[12] = "llm_bug_report"
	l, fr := localIssues(fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{row}}})
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5, Source: "llm_bug_report"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || res.Issues[0].Source != "llm_bug_report" {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if sql := fr.ran[0].SQL; strings.Contains(sql, obsquery.ServerErrorFilterSQL) || !strings.Contains(sql, obsquery.IssueSourceSQL+" = ") {
		t.Errorf("a source's issues are its records at any level, not errors:\n%s", sql)
	}
}

func TestCloudListIssuesSendsTheSourceAndRefusesAControlPlaneThatIgnoresIt(t *testing.T) {
	sourced := strings.Replace(wireIssueJSON, `"platform"`, `"source":"llm_bug_report","platform"`, 1)
	f := &fakeCaller{replies: map[string]string{procListIssues: `{"issues":[` + sourced + `]}`}}
	res, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{
		Narrow: Narrow{Range: Range{From: t0c, To: t0c.Add(time.Hour)}}, Source: "llm_bug_report", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.reqs[procListIssues]["source"]; got != "llm_bug_report" {
		t.Errorf("source = %v", got)
	}
	if len(res.Issues) != 1 || res.Issues[0].Source != "llm_bug_report" {
		t.Fatalf("issues = %+v", res.Issues)
	}

	// An older control plane drops the unknown filter and answers errors.
	f = &fakeCaller{replies: map[string]string{procListIssues: `{"issues":[` + wireIssueJSON + `]}`}}
	_, err = cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{
		Narrow: Narrow{Range: Range{From: t0c, To: t0c.Add(time.Hour)}}, Source: "llm_bug_report", Limit: 10,
	})
	if err == nil || !strings.Contains(err.Error(), "does not filter by issue source") {
		t.Errorf("err = %v, want a refusal to pass errors off as the source's issues", err)
	}
}

// The unit the statement counted travels with the count, so an issue whose events
// name no user is reported as "N chats" and not as a bare number.
func TestLocalListIssuesCarriesTheUnitAffectedWasCountedOver(t *testing.T) {
	row := issueRow(testFP)
	row[13] = "chats"
	l, _ := localIssues(fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{row}}})
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Issues[0]; got.Affected != 3 || got.AffectedUnit != "chats" {
		t.Errorf("affected = %d %q, want 3 chats", got.Affected, got.AffectedUnit)
	}
}

func TestLocalListIssuesAnswersFiltersThatCannotMatchWithoutQuerying(t *testing.T) {
	l, fr := localIssues()
	for _, req := range []ListIssuesRequest{
		{Narrow: narrowC(), Limit: 5, Statuses: []string{IssueResolved}},
		{Narrow: narrowC(), Limit: 5, Platform: PlatformBrowser},
	} {
		res, err := l.ListIssues(context.Background(), req)
		if err != nil || len(res.Issues) != 0 {
			t.Errorf("%+v: %v %v", req, res.Issues, err)
		}
	}
	if len(fr.ran) != 0 {
		t.Errorf("a filter no local issue can satisfy ran %d statements", len(fr.ran))
	}
	for _, req := range []ListIssuesRequest{
		{Narrow: narrowC(), Limit: 5, SinceRelease: "v1"},
		{Narrow: narrowC(), Limit: 5, SincePromotion: "p1"},
	} {
		if _, err := l.ListIssues(context.Background(), req); exitOf(err) != ExitUndetermined {
			t.Errorf("%+v: exit %d, want 2: a release filter has no ledger to answer it locally", req, exitOf(err))
		}
	}
}

func TestLocalGetIssueAssemblesTheDocument(t *testing.T) {
	l, _ := localIssues(
		fakeAnswer{"GROUP BY fingerprint, service", Result{Rows: [][]any{issueRow(testFP)}}},
		fakeAnswer{"ORDER BY Timestamp DESC", Result{Rows: [][]any{eventRow("2026-10-22 11:30:00.000000000", "0af7651916cd43dd8448eb211c80319c")}}},
		fakeAnswer{"GROUP BY version", Result{Rows: [][]any{{"v1.4.0", jnum("5"), jnum("2"), "2026-10-22 10:30:00", "2026-10-22 11:30:00", "users"}, {"v1.3.0", jnum("2"), jnum("1"), "2026-10-22 10:00:00", "2026-10-22 10:10:00", "chats"}}}},
		fakeAnswer{"uniqExact(TraceId) AS shared", Result{Rows: [][]any{{"bbbbbbbbbbbbbbbb", jnum("2")}}}},
	)
	res, err := l.GetIssue(context.Background(), GetIssueRequest{Narrow: narrowC(), ID: "sfp1-" + testFP})
	if err != nil {
		t.Fatal(err)
	}
	if res.Issue.ID != "sfp1-"+testFP || res.LatestEvent == nil || res.LatestEvent.TraceID != "0af7651916cd43dd8448eb211c80319c" {
		t.Fatalf("issue %+v event %+v", res.Issue, res.LatestEvent)
	}
	if len(res.Frames) != 1 || res.Frames[0].File != "internal/store/db.go" || res.Frames[0].Line != 88 || res.Frames[0].Function != "store.(*DB).Exec" {
		t.Errorf("frames = %+v", res.Frames)
	}
	if got := len(res.LatestEvent.Attributes["exception.stacktrace"]); got != maxEventAttrBytes {
		t.Errorf("a long attribute is cut to %d bytes, got %d", maxEventAttrBytes, got)
	}
	if len(res.ByVersion) != 2 || res.ByVersion[0].Version != "v1.4.0" || res.ByVersion[0].Events != 5 {
		t.Errorf("by version = %+v", res.ByVersion)
	}
	if len(res.CoOccurring) != 1 || res.CoOccurring[0] != "sfp1-bbbbbbbbbbbbbbbb" {
		t.Errorf("co-occurring = %v", res.CoOccurring)
	}
	if res.FirstRelease != nil {
		t.Error("a local issue knows no release ledger")
	}
}

func TestLocalGetIssueRefusesBadIdsAndReportsMissing(t *testing.T) {
	l, _ := localIssues()
	for _, id := range []string{"ISS-7f3a", "sfp1-xyz", ""} {
		if _, err := l.GetIssue(context.Background(), GetIssueRequest{Narrow: narrowC(), ID: id}); exitOf(err) != ExitWrong {
			t.Errorf("id %q: exit %d, want 1", id, exitOf(err))
		}
	}
	if _, err := l.GetIssue(context.Background(), GetIssueRequest{Narrow: narrowC(), ID: "sfp1-" + testFP}); exitOf(err) != ExitWrong {
		t.Errorf("an issue with no events in the range: exit %d, want 1", exitOf(err))
	}
}

func TestLocalTriageIsRefusedWithExit2(t *testing.T) {
	l, fr := localIssues()
	for _, st := range []string{IssueResolved, IssueIgnored, IssueUnresolved} {
		_, err := l.UpdateIssueStatus(context.Background(), UpdateIssueStatusRequest{ID: "sfp1-" + testFP, Status: st})
		if exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "triage on a hosted env") {
			t.Errorf("%s: %v (exit %d)", st, err, exitOf(err))
		}
	}
	if len(fr.ran) != 0 {
		t.Error("a refused write queried ClickHouse")
	}
}

func TestLocalListIssueEvents(t *testing.T) {
	l, _ := localIssues(fakeAnswer{"ORDER BY Timestamp DESC", Result{Rows: [][]any{
		eventRow("2026-10-22 11:30:00", "t1"), eventRow("2026-10-22 11:20:00", "t2"),
	}}})
	res, err := l.ListIssueEvents(context.Background(), ListIssueEventsRequest{Narrow: narrowC(), ID: "sfp1-" + testFP, Limit: 1})
	if err != nil || len(res.Events) != 1 || !res.Truncated || res.Events[0].TraceID != "t1" {
		t.Errorf("%+v %v", res, err)
	}
}

// ── cloud ───────────────────────────────────────────────────────────────

const wireIssueJSON = `{"id":"ISS-7f3a","platform":"ISSUE_PLATFORM_SERVER","envId":"env-1","service":"api","fingerprint":"` + testFP + `",
 "title":"boom","culprit":"internal/store/db.go in store.Exec","status":"ISSUE_STATUS_REGRESSED","firstSeenAt":"2026-10-20T10:00:00Z",
 "firstVersion":"v1.3.0","lastSeenAt":"2026-10-22T11:00:00Z","lastVersion":"v1.4.0","events":"42","affected":"9","version":4,
 "regressedAt":"2026-10-22T09:00:00Z","resolvedAt":"1970-01-01T00:00:00Z"}`

func TestCloudListIssuesRequestShapeAndDecoding(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListIssues: `{"issues":[` + wireIssueJSON + `],"nextPageToken":"tok"}`}}
	res, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{
		Narrow:   Narrow{Range: Range{From: t0c, To: t0c.Add(time.Hour)}, Services: []string{"api"}},
		Statuses: []string{IssueRegressed, IssueNew}, Platform: PlatformServer, SinceRelease: "v1.4.0", SincePromotion: "promo-1",
		Sort: "events", Limit: 10, PageToken: "prev",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := f.reqs[procListIssues]
	scope := req["scope"].(map[string]any)
	if scope["orgId"] != "org-1" || scope["envIds"].([]any)[0] != "env-1" || scope["services"].([]any)[0] != "api" {
		t.Errorf("scope = %v", scope)
	}
	if _, has := scope["filters"]; has {
		t.Error("IssueScope reserves filters; it must not be sent")
	}
	for k, want := range map[string]any{"platform": "ISSUE_PLATFORM_SERVER", "firstSeenRelease": "v1.4.0", "sincePromotionId": "promo-1",
		"sort": "ISSUE_SORT_EVENTS_DESC", "pageSize": float64(10), "pageToken": "prev"} {
		if req[k] != want {
			t.Errorf("%s = %v, want %v", k, req[k], want)
		}
	}
	if got := req["statuses"].([]any); got[0] != "ISSUE_STATUS_REGRESSED" || got[1] != "ISSUE_STATUS_NEW" {
		t.Errorf("statuses = %v", got)
	}
	if res.NextPageToken != "tok" || len(res.Issues) != 1 {
		t.Fatalf("%+v", res)
	}
	i := res.Issues[0]
	if i.Status != IssueRegressed || i.Platform != PlatformServer || i.Events != 42 || i.Affected != 9 || i.Version != 4 || i.EnvID != "" {
		t.Errorf("issue = %+v", i)
	}
	if !i.RegressedAt.Equal(time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("regressed_at = %v", i.RegressedAt)
	}
}

func TestCloudRefusesFiltersOnIssuesAndExplain(t *testing.T) {
	f := &fakeCaller{}
	c := cloudWith(f)
	if _, err := c.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5}); exitOf(err) != ExitWrong {
		t.Errorf("--filter on issues: exit %d, want 1", exitOf(err))
	}
	n := narrowC()
	n.Filters = nil
	n.Explain = true
	if _, err := c.ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Limit: 5}); exitOf(err) != ExitUndetermined {
		t.Errorf("--explain: exit %d, want 2", exitOf(err))
	}
	if len(f.reqs) != 0 {
		t.Error("a refused request reached the control plane")
	}
}

func TestCloudErrorMapping(t *testing.T) {
	for code, want := range map[string]int{
		"unavailable": ExitUndetermined, "invalid_argument": ExitWrong, "aborted": ExitWrong, "not_found": ExitWrong,
		"unimplemented": ExitUndetermined, "permission_denied": ExitUndetermined, "failed_precondition": ExitUndetermined,
	} {
		n := narrowC()
		n.Filters = nil
		f := &fakeCaller{errs: map[string]error{procListIssues: &cloud.Error{Code: code}}}
		_, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Limit: 5})
		if got := exitOf(err); got != want {
			t.Errorf("%s: exit %d, want %d (%v)", code, got, want, err)
		}
	}
	n := narrowC()
	n.Filters = nil
	f := &fakeCaller{errs: map[string]error{procListIssues: &cloud.Error{Code: "unimplemented"}}}
	_, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Limit: 5})
	if err == nil || !strings.Contains(err.Error(), "does not serve issues yet") {
		t.Errorf("unimplemented must say so, got %v", err)
	}
}

func TestCloudGetIssueDecodesTheDocument(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetIssue: `{"issue":` + wireIssueJSON + `,
	 "latestEvent":{"timestamp":"2026-10-22T11:00:00Z","traceId":"tr1","serviceVersion":"v1.4.0","message":"boom","attributes":{"a":"b"}},
	 "frames":[{"function":"store.Exec","file":"internal/store/db.go","line":88,"inApp":true}],
	 "byVersion":[{"version":"v1.4.0","events":"30","affected":"6"}],
	 "coOccurringIssueIds":["ISS-1"],
	 "firstRelease":{"version":"v1.3.0","gitCommit":"abc1234","commitRange":"aaa1111..abc1234","promotedAt":"2026-10-20T09:00:00Z"}}`}}
	n := narrowC()
	n.Filters = nil
	res, err := cloudWith(f).GetIssue(context.Background(), GetIssueRequest{Narrow: n, ID: "ISS-7f3a"})
	if err != nil {
		t.Fatal(err)
	}
	if f.reqs[procGetIssue]["issueId"] != "ISS-7f3a" {
		t.Errorf("request = %v", f.reqs[procGetIssue])
	}
	if res.Issue.EnvID != "env-1" || res.Issue.LatestTraceID != "tr1" || res.LatestEvent.Message != "boom" || res.Frames[0].Line != 88 ||
		res.ByVersion[0].Events != 30 || res.CoOccurring[0] != "ISS-1" || res.FirstRelease.CommitRange != "aaa1111..abc1234" {
		t.Errorf("%+v", res)
	}
}

func TestCloudGetIssueRefusesAnotherEnvironmentsIssue(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetIssue: `{"issue":` + strings.Replace(wireIssueJSON, `"env-1"`, `"env-2"`, 1) + `}`}}
	n := narrowC()
	n.Filters = nil
	if _, err := cloudWith(f).GetIssue(context.Background(), GetIssueRequest{Narrow: n, ID: "ISS-7f3a"}); exitOf(err) != ExitWrong {
		t.Errorf("exit %d, want 1: the issue is in another env than the one named", exitOf(err))
	}
}

func TestCloudTriageSendsExpectedVersionAndMapsResolveIn(t *testing.T) {
	updated := strings.Replace(strings.Replace(wireIssueJSON, "ISSUE_STATUS_REGRESSED", "ISSUE_STATUS_RESOLVED", 1), `"version":4`, `"version":5`, 1)
	f := &fakeCaller{replies: map[string]string{procGetIssue: `{"issue":` + wireIssueJSON + `}`, procUpdateIssueStatus: `{"issue":` + updated + `}`}}
	until := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		req  UpdateIssueStatusRequest
		want map[string]any
	}{
		{UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueResolved, ResolveIn: ResolveNextRelease},
			map[string]any{"status": "ISSUE_STATUS_CHANGE_RESOLVED", "resolveIn": map[string]any{"kind": "ISSUE_RESOLVE_KIND_NEXT_RELEASE"}}},
		{UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueResolved, ResolveIn: ResolveVersion, Version: "v1.5.0"},
			map[string]any{"status": "ISSUE_STATUS_CHANGE_RESOLVED", "resolveIn": map[string]any{"kind": "ISSUE_RESOLVE_KIND_VERSION", "version": "v1.5.0"}}},
		{UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueResolved},
			map[string]any{"resolveIn": map[string]any{"kind": "ISSUE_RESOLVE_KIND_NOW"}}},
		{UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueIgnored, IgnoredUntil: until},
			map[string]any{"status": "ISSUE_STATUS_CHANGE_IGNORED", "ignoredUntil": "2026-10-25T00:00:00Z"}},
		{UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueUnresolved}, map[string]any{"status": "ISSUE_STATUS_CHANGE_UNRESOLVED"}},
	}
	for _, tc := range cases {
		res, err := cloudWith(f).UpdateIssueStatus(context.Background(), tc.req)
		if err != nil {
			t.Fatal(err)
		}
		req := f.reqs[procUpdateIssueStatus]
		if req["issueId"] != "ISS-7f3a" || req["envId"] != "env-1" || req["expectedVersion"] != float64(4) {
			t.Errorf("%+v: request %v must carry the issue, its env and the version read", tc.req, req)
		}
		for k, want := range tc.want {
			if got := jsonOf(req[k]); got != jsonOf(want) {
				t.Errorf("%+v: %s = %s, want %s", tc.req, k, got, jsonOf(want))
			}
		}
		if res.Issue.Status != IssueResolved || res.Issue.Version != 5 {
			t.Errorf("result = %+v", res.Issue)
		}
	}
	if _, has := f.reqs[procUpdateIssueStatus]["resolveIn"]; has {
		t.Error("only a resolve carries resolve_in")
	}
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestCloudTriageStaleWriteIsExit1WithRetryAdvice(t *testing.T) {
	f := &fakeCaller{
		replies: map[string]string{procGetIssue: `{"issue":` + wireIssueJSON + `}`},
		errs:    map[string]error{procUpdateIssueStatus: &cloud.Error{Code: "aborted"}},
	}
	_, err := cloudWith(f).UpdateIssueStatus(context.Background(), UpdateIssueStatusRequest{ID: "ISS-7f3a", Status: IssueResolved})
	if exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "re-read and retry") {
		t.Errorf("err = %v (exit %d)", err, exitOf(err))
	}
	var ce *cloud.Error
	if !errors.As(err, &ce) {
		t.Error("the control plane's error stays in the chain")
	}
}

func TestCloudTriageValidatesBeforeCalling(t *testing.T) {
	f := &fakeCaller{}
	for _, req := range []UpdateIssueStatusRequest{
		{ID: "x", Status: IssueRegressed},
		{ID: "x", Status: IssueResolved, ResolveIn: ResolveVersion},
		{ID: "x", Status: IssueResolved, ResolveIn: "soon"},
	} {
		if _, err := cloudWith(f).UpdateIssueStatus(context.Background(), req); exitOf(err) != ExitWrong {
			t.Errorf("%+v: exit %d, want 1", req, exitOf(err))
		}
	}
}

func TestCloudListIssueEventsOmitsTheTimeRange(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListIssueEvents: `{"events":[{"timestamp":"2026-10-22T11:00:00Z","traceId":"t1"}],"nextPageToken":"n"}`}}
	res, err := cloudWith(f).ListIssueEvents(context.Background(), ListIssueEventsRequest{Narrow: narrowC(), ID: "ISS-7f3a", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	req := f.reqs[procListIssueEvents]
	if _, has := req["timeRange"]; has || req["issueId"] != "ISS-7f3a" || req["pageSize"] != float64(3) {
		t.Errorf("request = %v", req)
	}
	if !res.Truncated || res.NextPageToken != "n" || res.Events[0].TraceID != "t1" {
		t.Errorf("%+v", res)
	}
}

func causeRow(fp, svc, cause, first string) []any { return []any{fp, svc, cause, first} }

// T6: issues that share a cause and first fired together are marked as one
// incident, across services, without being merged.
func TestLocalListIssuesMarksIncidents(t *testing.T) {
	l, fr := localIssues(
		fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{issueRow(testFP), issueRow("aaaaaaaaaaaaaaaa"), issueRow("cccccccccccccccc")}}},
		fakeAnswer{"AS cause", Result{Rows: [][]any{
			causeRow(testFP, "api", "SQLSTATE 57P01", "2026-10-22 10:00:00.000000000"),
			causeRow("aaaaaaaaaaaaaaaa", "worker", "SQLSTATE 57P01", "2026-10-22 10:03:00.000000000"),
			causeRow("cccccccccccccccc", "api", "context deadline exceeded", "2026-10-22 10:01:00.000000000"),
		}}},
	)
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 3 {
		t.Fatalf("issues = %d: incident marking must not merge or drop issues", len(res.Issues))
	}
	a, b, c := res.Issues[0], res.Issues[1], res.Issues[2]
	if a.IncidentKey == "" || a.IncidentKey != b.IncidentKey || a.RelatedCount != 1 || b.RelatedCount != 1 {
		t.Errorf("incident = %q/%q related %d/%d", a.IncidentKey, b.IncidentKey, a.RelatedCount, b.RelatedCount)
	}
	if c.IncidentKey != "" || c.RelatedCount != 0 {
		t.Errorf("an issue alone with its cause is not an incident: %+v", c)
	}
	// The incident search spans services even though the list is narrowed to one.
	var sawIncident bool
	for _, st := range fr.ran {
		if strings.Contains(st.SQL, "AS cause") {
			sawIncident = true
			if strings.Contains(st.SQL, "ServiceName = ") || strings.Contains(st.SQL, "ServiceName IN") {
				t.Errorf("the incident search must not narrow to the list's --service:\n%s", st.SQL)
			}
		}
	}
	if !sawIncident {
		t.Error("no incident statement ran")
	}
}

func TestLocalListIssuesOfASourceFormNoIncident(t *testing.T) {
	l, fr := localIssues(fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{issueRow(testFP)}}})
	if _, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5, Source: "llm_bug_report"}); err != nil {
		t.Fatal(err)
	}
	for _, st := range fr.ran {
		if strings.Contains(st.SQL, "AS cause") {
			t.Error("source issues are reports, not errors: they form no incident")
		}
	}
}

func TestLocalGetIssueListsItsIncident(t *testing.T) {
	l, _ := localIssues(
		fakeAnswer{"AS cause", Result{Rows: [][]any{
			causeRow(testFP, "api", "SQLSTATE 57P01", "2026-10-22 10:00:00.000000000"),
			causeRow("aaaaaaaaaaaaaaaa", "worker", "SQLSTATE 57P01", "2026-10-22 10:03:00.000000000"),
		}}},
		fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{issueRow(testFP)}}},
	)
	res, err := l.GetIssue(context.Background(), GetIssueRequest{Narrow: narrowC(), ID: "sfp1-" + testFP})
	if err != nil {
		t.Fatal(err)
	}
	if res.Issue.IncidentKey == "" || res.Issue.RelatedCount != 1 || len(res.Incident) != 1 || res.Incident[0] != "sfp1-aaaaaaaaaaaaaaaa" {
		t.Errorf("incident = key %q related %d members %v", res.Issue.IncidentKey, res.Issue.RelatedCount, res.Incident)
	}
}

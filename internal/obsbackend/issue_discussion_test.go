package obsbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

const discussionIssueJSON = `{"id":"ISS-7f3a","platform":"ISSUE_PLATFORM_SERVER","envId":"env-1","service":"api","title":"boom","status":"ISSUE_STATUS_UNRESOLVED",
 "version":7,"incidentKey":"sqlstate:57P01","relatedCount":2,"assignee":{"id":"u-1","name":"Ada"}}`

func TestCloudIssueDecodesAssigneeAndIncident(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{
		procGetIssue: `{"issue":` + discussionIssueJSON + `,"incidentIssueIds":["ISS-a","ISS-b"]}`,
	}}
	n := narrowC()
	n.Filters = nil
	res, err := cloudWith(f).GetIssue(context.Background(), GetIssueRequest{Narrow: n, ID: "ISS-7f3a"})
	if err != nil {
		t.Fatal(err)
	}
	i := res.Issue
	if i.Assignee == nil || i.Assignee.ID != "u-1" || i.Assignee.Name != "Ada" || i.IncidentKey != "sqlstate:57P01" || i.RelatedCount != 2 {
		t.Errorf("issue = %+v", i)
	}
	if fmt.Sprint(res.Incident) != "[ISS-a ISS-b]" {
		t.Errorf("incident ids = %v", res.Incident)
	}
}

func TestCloudIssueUnassignedIsNil(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"id":"ISS-1"}`,
		"empty":  `{"id":"ISS-1","assignee":{}}`,
		"null":   `{"id":"ISS-1","assignee":null}`,
	} {
		f := &fakeCaller{replies: map[string]string{procListIssues: `{"issues":[` + body + `]}`}}
		n := narrowC()
		n.Filters = nil
		res, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		if res.Issues[0].Assignee != nil {
			t.Errorf("%s: assignee = %+v, want nil", name, res.Issues[0].Assignee)
		}
	}
}

func TestCloudListIssuesAssigneeFilter(t *testing.T) {
	for _, tc := range []struct {
		assignee string
		want     bool
	}{{"", false}, {"u-1", true}, {"me", true}} {
		f := &fakeCaller{replies: map[string]string{procListIssues: `{}`}}
		n := narrowC()
		n.Filters = nil
		if _, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Limit: 5, Assignee: tc.assignee}); err != nil {
			t.Fatal(err)
		}
		got, has := f.reqs[procListIssues]["assignee"]
		if has != tc.want || (has && got != tc.assignee) {
			t.Errorf("assignee %q: request has %v=%v, want present=%v (an empty filter must be omitted: the control plane refuses it)", tc.assignee, has, got, tc.want)
		}
	}
}

func TestCloudListIssuesUnassignedWire(t *testing.T) {
	for _, unassigned := range []bool{false, true} {
		f := &fakeCaller{replies: map[string]string{procListIssues: `{}`}}
		n := narrowC()
		n.Filters = nil
		if _, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Narrow: n, Unassigned: unassigned}); err != nil {
			t.Fatal(err)
		}
		req := f.reqs[procListIssues]
		got, has := req["unassigned"]
		if has != unassigned || (has && got != true) || req["assignee"] != nil {
			t.Fatalf("only send unassigned=true or omit it, never false: %v", req)
		}
	}
	f := &fakeCaller{}
	_, err := cloudWith(f).ListIssues(context.Background(), ListIssuesRequest{Assignee: "me", Unassigned: true})
	if exitOf(err) != ExitWrong || len(f.reqs) != 0 {
		t.Fatalf("exclusive filters must not reach server: %v %v", err, f.reqs)
	}
}

func TestLocalListIssuesWithAnAssigneeMatchesNothing(t *testing.T) {
	l, fr := localIssues()
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5, Assignee: "u-1"})
	if err != nil || len(res.Issues) != 0 || len(fr.ran) != 0 {
		t.Errorf("issues=%v err=%v ran=%d: every local issue is unassigned", res.Issues, err, len(fr.ran))
	}
}

func TestLocalUnassignedFilterIncludesAllIssues(t *testing.T) {
	l, _ := localIssues(fakeAnswer{"GROUP BY fingerprint", Result{Rows: [][]any{issueRow(testFP)}}})
	res, err := l.ListIssues(context.Background(), ListIssuesRequest{Narrow: narrowC(), Limit: 5, Unassigned: true})
	if err != nil || len(res.Issues) != 1 || res.Issues[0].Assignee != nil {
		t.Fatalf("local issues are all unassigned: %+v %v", res, err)
	}
}

func TestLocalDiscussion(t *testing.T) {
	l := Local{Env: "dev"}
	tl, err := l.ListIssueTimeline(context.Background(), ListIssueTimelineRequest{ID: "sfp1-x", Limit: 5})
	if err != nil || len(tl.Entries) != 0 {
		t.Errorf("a local issue has an empty timeline, got %v %v", tl, err)
	}
	if _, err := l.AddIssueComment(context.Background(), AddIssueCommentRequest{ID: "sfp1-x", Body: "hi"}); exitOf(err) != ExitUndetermined {
		t.Errorf("comment: exit %d, want 2", exitOf(err))
	}
	if _, err := l.AssignIssue(context.Background(), AssignIssueRequest{ID: "sfp1-x", Assignee: "u"}); exitOf(err) != ExitUndetermined {
		t.Errorf("assign: exit %d, want 2", exitOf(err))
	}
}

// pagedTimeline serves a timeline of n comment entries oldest first, size to a page.
type pagedTimeline struct {
	n, size int
	pages   int
}

func timelineEntryJSON(i int) string {
	return fmt.Sprintf(`{"id":"e%03d","time":"2026-10-22T10:%02d:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1","name":"Ada"},
 "comment":{"comment":{"id":"c%03d","issueId":"ISS-7f3a","body":"note %d","createdAt":"2026-10-22T10:%02d:00Z","updatedAt":"1970-01-01T00:00:00Z","author":{"id":"u-1","name":"Ada"}}}}`, i, i%60, i, i, i%60)
}

func (p *pagedTimeline) Call(_ context.Context, _ string, req, out any) error {
	var in struct {
		PageToken string `json:"pageToken"`
	}
	b, _ := json.Marshal(req)
	_ = json.Unmarshal(b, &in)
	p.pages++
	start := 0
	if in.PageToken != "" {
		fmt.Sscanf(in.PageToken, "t%d", &start)
	}
	end := min(start+p.size, p.n)
	var entries []string
	for i := start; i < end; i++ {
		entries = append(entries, timelineEntryJSON(i))
	}
	next := ""
	if end < p.n {
		next = fmt.Sprintf(`"nextPageToken":"t%d",`, end)
	}
	return json.Unmarshal([]byte(`{`+next+`"issueId":"ISS-target","entries":[`+strings.Join(entries, ",")+`]}`), out)
}

func TestCloudListIssueTimelineKeepsTheNewestEntriesOldestFirst(t *testing.T) {
	for _, tc := range []struct {
		n, limit, wantFirst, wantOmitted, wantPages int
	}{
		{n: 3, limit: 10, wantFirst: 0, wantOmitted: 0, wantPages: 1},
		{n: 250, limit: 5, wantFirst: 245, wantOmitted: 245, wantPages: 3},
		{n: 100, limit: 100, wantFirst: 0, wantOmitted: 0, wantPages: 1},
		{n: 101, limit: 1, wantFirst: 100, wantOmitted: 100, wantPages: 2},
	} {
		p := &pagedTimeline{n: tc.n, size: timelinePageSize}
		res, err := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: p}.ListIssueTimeline(context.Background(), ListIssueTimelineRequest{ID: "ISS-7f3a", Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		want := min(tc.n, tc.limit)
		if len(res.Entries) != want || res.Omitted != tc.wantOmitted || p.pages != tc.wantPages {
			t.Errorf("n=%d limit=%d: %d entries, %d omitted, %d pages; want %d, %d, %d", tc.n, tc.limit, len(res.Entries), res.Omitted, p.pages, want, tc.wantOmitted, tc.wantPages)
			continue
		}
		if got := res.Entries[0].ID; got != fmt.Sprintf("e%03d", tc.wantFirst) {
			t.Errorf("n=%d limit=%d: first entry %s, want e%03d (the newest %d, oldest first)", tc.n, tc.limit, got, tc.wantFirst, want)
		}
		if res.IssueID != "ISS-target" {
			t.Errorf("issue id = %q: a merged issue's timeline is its target's", res.IssueID)
		}
	}
	if _, err := (Cloud{Caller: &pagedTimeline{}}).ListIssueTimeline(context.Background(), ListIssueTimelineRequest{ID: "x"}); exitOf(err) != ExitWrong {
		t.Errorf("a zero limit is refused, got %v", err)
	}
}

func TestCloudListIssueTimelineDecodesEveryEntryKind(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procListIssueTimeline: `{"issueId":"ISS-7f3a","entries":[
 {"id":"s1","time":"2026-10-22T10:00:00Z","issueId":"ISS-old","actor":{"kind":"KIND_SYSTEM"},"status":{"to":"ISSUE_STATUS_NEW"}},
 {"id":"s2","time":"2026-10-22T10:05:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1","name":"Ada"},
  "status":{"from":"ISSUE_STATUS_NEW","to":"ISSUE_STATUS_RESOLVED","version":"v1.5.0"}},
 {"id":"c1","time":"2026-10-22T10:10:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1"},
  "comment":{"comment":{"id":"cm1","issueId":"ISS-7f3a","body":"look **here**","createdAt":"2026-10-22T10:10:00Z","updatedAt":"2026-10-22T10:20:00Z","author":{"id":"u-1","name":"Ada"}}}},
 {"id":"a1","time":"2026-10-22T10:15:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_MACHINE","id":"tok-1"},"assignment":{"to":{"id":"u-2","name":"Bo"}}},
 {"id":"a2","time":"2026-10-22T10:16:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_MACHINE","id":"tok-1"},"assignment":{"from":{"id":"u-2","name":"Bo"}}},
 {"id":"h1","time":"2026-10-22T10:17:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1"},"chat":{"link":{"chatId":"chat-9","chatTitle":"Ask Reliant","linkedBy":{"id":"u-1"}}}},
 {"id":"x1","time":"2026-10-22T10:18:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER"},"somethingNew":{}}
]}`}}
	res, err := cloudWith(f).ListIssueTimeline(context.Background(), ListIssueTimelineRequest{ID: "ISS-old", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if res.IssueID != "ISS-7f3a" || len(res.Entries) != 6 {
		t.Fatalf("issue %q with %d entries (an entry of an unknown kind is skipped)", res.IssueID, len(res.Entries))
	}
	e := res.Entries
	if e[0].Kind != TimelineStatus || e[0].Status.From != "" || e[0].Status.To != IssueNew || e[0].IssueID != "ISS-old" || e[0].Actor.Kind != ActorSystem {
		t.Errorf("opening = %+v %+v", e[0], e[0].Status)
	}
	if s := e[1].Status; s.From != IssueNew || s.To != IssueResolved || s.Version != "v1.5.0" || e[1].Actor.Name != "Ada" {
		t.Errorf("resolve = %+v", s)
	}
	c := e[2].Comment
	if e[2].Kind != TimelineComment || c.Body != "look **here**" || c.Author.Name != "Ada" || c.UpdatedAt.IsZero() {
		t.Errorf("comment = %+v", c)
	}
	if a := e[3].Assignment; a.From != nil || a.To == nil || a.To.ID != "u-2" || e[3].Actor.Kind != ActorMachine {
		t.Errorf("assigned = %+v", a)
	}
	if a := e[4].Assignment; a.To != nil || a.From == nil || a.From.ID != "u-2" {
		t.Errorf("unassigned = %+v", a)
	}
	if l := e[5].Chat; e[5].Kind != TimelineChat || l.ChatID != "chat-9" || l.ChatTitle != "Ask Reliant" || l.LinkedBy.ID != "u-1" {
		t.Errorf("chat = %+v", l)
	}
	if got := f.reqs[procListIssueTimeline]["issueId"]; got != "ISS-old" {
		t.Errorf("request issueId = %v", got)
	}
}

func TestCloudDiscussionCallsUnimplementedAsUndetermined(t *testing.T) {
	f := &fakeCaller{errs: map[string]error{
		procListIssueTimeline: &cloud.Error{Code: "unimplemented"},
		procAddIssueComment:   &cloud.Error{Code: "unimplemented"},
	}}
	c := cloudWith(f)
	if _, err := c.ListIssueTimeline(context.Background(), ListIssueTimelineRequest{ID: "x", Limit: 1}); exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "does not serve issues yet") {
		t.Errorf("timeline: %v", err)
	}
	if _, err := c.AddIssueComment(context.Background(), AddIssueCommentRequest{ID: "x", Body: "hi"}); exitOf(err) != ExitUndetermined || !strings.Contains(err.Error(), "does not serve issues yet") {
		t.Errorf("comment: %v", err)
	}
}

func TestCloudAddIssueComment(t *testing.T) {
	reply := `{"comment":{"id":"cm1","issueId":"ISS-7f3a","body":"hello","createdAt":"2026-10-22T10:10:00Z","updatedAt":"1970-01-01T00:00:00Z","author":{"id":"u-1","name":"Ada"}}}`
	f := &fakeCaller{replies: map[string]string{procAddIssueComment: reply}}
	res, err := cloudWith(f).AddIssueComment(context.Background(), AddIssueCommentRequest{ID: "ISS-7f3a", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if req := f.reqs[procAddIssueComment]; req["issueId"] != "ISS-7f3a" || req["body"] != "hello" {
		t.Errorf("request = %v", req)
	}
	if c := res.Comment; c.ID != "cm1" || c.Author.ID != "u-1" || !c.UpdatedAt.IsZero() || !c.CreatedAt.Equal(time.Date(2026, 10, 22, 10, 10, 0, 0, time.UTC)) {
		t.Errorf("comment = %+v", c)
	}
	for _, body := range []string{"", "  \n"} {
		g := &fakeCaller{}
		if _, err := cloudWith(g).AddIssueComment(context.Background(), AddIssueCommentRequest{ID: "x", Body: body}); exitOf(err) != ExitWrong || g.reqs != nil {
			t.Errorf("body %q: exit %d, calls %v: an empty comment is refused before calling", body, exitOf(err), g.reqs)
		}
	}
	for code, want := range map[string]int{"failed_precondition": ExitUndetermined, "permission_denied": ExitUndetermined, "not_found": ExitWrong, "invalid_argument": ExitWrong} {
		g := &fakeCaller{errs: map[string]error{procAddIssueComment: &cloud.Error{Code: code}}}
		if _, err := cloudWith(g).AddIssueComment(context.Background(), AddIssueCommentRequest{ID: "x", Body: "hi"}); exitOf(err) != want {
			t.Errorf("%s: exit %d, want %d", code, exitOf(err), want)
		}
	}
}

func TestCloudAssignIssueSendsTheVersionItRead(t *testing.T) {
	for _, tc := range []struct {
		name, assignee string
		wantAssignee   bool
	}{{"assign", "u-2", true}, {"clear", "", false}} {
		f := &fakeCaller{replies: map[string]string{
			procGetIssue:    `{"issue":` + discussionIssueJSON + `}`,
			procAssignIssue: `{"issue":` + strings.Replace(discussionIssueJSON, `"version":7`, `"version":8`, 1) + `}`,
		}}
		res, err := cloudWith(f).AssignIssue(context.Background(), AssignIssueRequest{ID: "ISS-7f3a", Assignee: tc.assignee})
		if err != nil {
			t.Fatal(tc.name, err)
		}
		req := f.reqs[procAssignIssue]
		if req["issueId"] != "ISS-7f3a" || req["expectedVersion"] != float64(7) {
			t.Errorf("%s: request %v must carry the issue and the version read", tc.name, req)
		}
		if got, has := req["assigneeUserId"]; has != tc.wantAssignee || (has && got != tc.assignee) {
			t.Errorf("%s: assigneeUserId = %v (present=%v), want present=%v", tc.name, got, has, tc.wantAssignee)
		}
		if res.Issue.Version != 8 || res.Issue.EnvID != "env-1" {
			t.Errorf("%s: result = %+v", tc.name, res.Issue)
		}
	}
}

func TestCloudAssignIssueAbortedIsNeverRetried(t *testing.T) {
	calls := 0
	f := &countingCaller{fakeCaller: fakeCaller{
		replies: map[string]string{procGetIssue: `{"issue":` + discussionIssueJSON + `}`},
		errs:    map[string]error{procAssignIssue: &cloud.Error{Code: "aborted"}},
	}, calls: &calls}
	_, err := Cloud{Env: "prod", EnvID: "env-1", OrgID: "org-1", Caller: f}.AssignIssue(context.Background(), AssignIssueRequest{ID: "ISS-7f3a", Assignee: "u-2"})
	if exitOf(err) != ExitWrong || !strings.Contains(err.Error(), "changed since you read it") {
		t.Errorf("err = %v (exit %d)", err, exitOf(err))
	}
	if calls != 2 {
		t.Errorf("%d calls, want 2: one read, one write, no retry", calls)
	}
}

type countingCaller struct {
	fakeCaller
	calls *int
}

func (c *countingCaller) Call(ctx context.Context, procedure string, req, out any) error {
	*c.calls++
	return c.fakeCaller.Call(ctx, procedure, req, out)
}

func TestCloudAssignIssueRefusesAnotherEnvironmentsIssue(t *testing.T) {
	f := &fakeCaller{replies: map[string]string{procGetIssue: `{"issue":` + strings.Replace(discussionIssueJSON, `"env-1"`, `"env-2"`, 1) + `}`}}
	if _, err := cloudWith(f).AssignIssue(context.Background(), AssignIssueRequest{ID: "ISS-7f3a", Assignee: "u"}); exitOf(err) != ExitWrong {
		t.Errorf("exit %d, want 1", exitOf(err))
	}
	if _, wrote := f.reqs[procAssignIssue]; wrote {
		t.Error("an issue of another env was written")
	}
}

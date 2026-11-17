package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/suspect"
)

const (
	cloudTimeline = "controlplane.v1.IssuesService/ListIssueTimeline"
	cloudComment  = "controlplane.v1.IssuesService/AddIssueComment"
	cloudAssign   = "controlplane.v1.IssuesService/AssignIssue"
)

// discussedIssueJSON is cloudIssueJSON with an assignee and an incident.
var discussedIssueJSON = strings.Replace(cloudIssueJSON, `"version":4,`,
	`"version":4,"incidentKey":"sqlstate:57P01","relatedCount":2,"assignee":{"id":"u-1","name":"Ada"},`, 1)

const timelineReply = `{"issueId":"ISS-7f3a","entries":[
 {"id":"e1","time":"2026-10-20T10:00:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_SYSTEM"},"status":{"to":"ISSUE_STATUS_NEW"}},
 {"id":"e2","time":"2026-10-21T10:00:00Z","issueId":"ISS-old","actor":{"kind":"KIND_USER","id":"u-1","name":"Ada"},
  "comment":{"comment":{"id":"cm1","issueId":"ISS-old","body":"pool is\nexhausted","createdAt":"2026-10-21T10:00:00Z","author":{"id":"u-1","name":"Ada"}}}},
 {"id":"e3","time":"2026-10-21T11:00:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1","name":"Ada"},"assignment":{"to":{"id":"u-1","name":"Ada"}}},
 {"id":"e4","time":"2026-10-21T12:00:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_USER","id":"u-1","name":"Ada"},"chat":{"link":{"chatId":"chat-9","chatTitle":"Ask Reliant"}}},
 {"id":"e5","time":"2026-10-22T09:00:00Z","issueId":"ISS-7f3a","actor":{"kind":"KIND_SYSTEM"},"status":{"from":"ISSUE_STATUS_NEW","to":"ISSUE_STATUS_REGRESSED"}}
]}`

func discussedIssueCaller() *fakeCloudCaller {
	return &fakeCloudCaller{replies: map[string]string{
		cloudIssue:    `{"issue":` + discussedIssueJSON + `,"incidentIssueIds":["ISS-a","ISS-b"]}`,
		cloudEvents:   `{}`,
		cloudTimeline: timelineReply,
	}}
}

func noSuspects(t *testing.T) {
	withSuspects(t, func(context.Context, string, []string) ([]suspect.Commit, error) { return nil, nil })
}

func TestObsIssueCloudShowsAssigneeIncidentAndTimeline(t *testing.T) {
	noSuspects(t)
	fc := discussedIssueCaller()
	out, _, err := obsRunCloud(t, fc, "issue", "ISS-7f3a", "prod", "--json", "--events", "0", "--timeline-limit", "3")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	issue := doc["issue"].(map[string]any)
	if a := issue["assignee"].(map[string]any); a["id"] != "u-1" || a["name"] != "Ada" {
		t.Errorf("assignee = %v", a)
	}
	if issue["incident_key"] != "sqlstate:57P01" || issue["related_count"] != float64(2) {
		t.Errorf("incident = %v %v", issue["incident_key"], issue["related_count"])
	}
	if got := strings.Join(toStrings(doc["incident_issue_ids"]), ","); got != "ISS-a,ISS-b" {
		t.Errorf("incident_issue_ids = %s", got)
	}
	tl := doc["timeline"].([]any)
	if len(tl) != 3 || doc["timeline_omitted"] != float64(2) {
		t.Fatalf("timeline = %d entries, omitted %v; want the newest 3 and 2 omitted", len(tl), doc["timeline_omitted"])
	}
	var ids []string
	for _, e := range tl {
		ids = append(ids, e.(map[string]any)["id"].(string))
	}
	if strings.Join(ids, ",") != "e3,e4,e5" {
		t.Errorf("timeline ids = %v, want the newest three, newest last", ids)
	}
	first := tl[0].(map[string]any)
	if first["kind"] != "assignment" || first["assignment"].(map[string]any)["to"].(map[string]any)["id"] != "u-1" {
		t.Errorf("entry = %v", first)
	}
	if last := tl[2].(map[string]any); last["status"].(map[string]any)["to"] != "regressed" || last["actor"].(map[string]any)["kind"] != "system" {
		t.Errorf("entry = %v", last)
	}
}

func TestObsIssueCloudTimelineFollowsAMerge(t *testing.T) {
	noSuspects(t)
	fc := discussedIssueCaller()
	fc.replies[cloudTimeline] = strings.Replace(timelineReply, `"issueId":"ISS-7f3a","entries"`, `"issueId":"ISS-target","entries"`, 1)
	out, _, err := obsRunCloud(t, fc, "issue", "ISS-7f3a", "prod", "--json", "--events", "0")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if doc["timeline_issue_id"] != "ISS-target" {
		t.Errorf("timeline_issue_id = %v", doc["timeline_issue_id"])
	}
	tl := doc["timeline"].([]any)
	if got := tl[1].(map[string]any)["issue_id"]; got != "ISS-old" {
		t.Errorf("each entry keeps its own issue_id, got %v", got)
	}
}

func TestObsIssueCloudTextForm(t *testing.T) {
	noSuspects(t)
	out, _, err := obsRunCloud(t, discussedIssueCaller(), "issue", "ISS-7f3a", "prod", "--events", "0")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"  assignee Ada (u-1)\n",
		"\nincident sqlstate:57P01: 2 other issues share this cause: ISS-a, ISS-b\n",
		"\ntimeline:\n",
		"2026-10-20T10:00:00Z  status     opened→new  by system\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, want := range []string{
		"2026-10-21T10:00:00Z  comment    cm1  Ada (u-1): pool is exhausted\n",
		"2026-10-21T11:00:00Z  assignment none→Ada (u-1)  by Ada\n",
		`2026-10-21T12:00:00Z  chat       chat-9 "Ask Reliant"  by Ada` + "\n",
		"2026-10-22T09:00:00Z  status     new→regressed  by system\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Index(out, "opened→new") > strings.Index(out, "new→regressed") {
		t.Error("the timeline must read oldest first, newest last")
	}
}

func TestObsIssueCloudTimelineLimit(t *testing.T) {
	noSuspects(t)
	for _, tc := range []struct {
		args      []string
		wantCalls int
		wantExit  int
	}{
		{[]string{"--timeline-limit", "0"}, 0, 0},
		{[]string{"--timeline-limit", "101"}, 0, 1},
		{[]string{"--timeline-limit", "-1"}, 0, 1},
		{nil, 1, 0},
	} {
		fc := discussedIssueCaller()
		out, _, err := obsRunCloud(t, fc, append([]string{"issue", "ISS-7f3a", "prod", "--json", "--events", "0"}, tc.args...)...)
		if exitCodeForError(err) != tc.wantExit {
			t.Errorf("%v: exit %d, want %d (%v)", tc.args, exitCodeForError(err), tc.wantExit, err)
		}
		n := 0
		for _, c := range fc.calls {
			if c.procedure == cloudTimeline {
				n++
			}
		}
		if n != tc.wantCalls {
			t.Errorf("%v: %d timeline calls, want %d", tc.args, n, tc.wantCalls)
		}
		if tc.wantExit == 0 && tc.wantCalls == 0 {
			if _, has := decode(t, out)["timeline"]; has {
				t.Errorf("%v: --timeline-limit 0 must leave the timeline out", tc.args)
			}
		}
	}
}

// A control plane that predates the timeline must not take the issue with it.
func TestObsIssueCloudTimelineFailureDegradesToANote(t *testing.T) {
	noSuspects(t)
	for _, code := range []string{"unimplemented", "permission_denied"} {
		fc := discussedIssueCaller()
		fc.errs = map[string]error{cloudTimeline: &cloud.Error{Code: code}}
		out, errOut, err := obsRunCloud(t, fc, "issue", "ISS-7f3a", "prod", "--json", "--events", "0")
		if err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		doc := decode(t, out)
		note, _ := doc["timeline_note"].(string)
		if doc["ok"] != true || !strings.HasPrefix(note, "timeline unavailable") || !strings.Contains(errOut, "timeline unavailable") {
			t.Errorf("%s: ok=%v note=%q stderr=%q", code, doc["ok"], note, errOut)
		}
		if doc["issue"].(map[string]any)["id"] != "ISS-7f3a" {
			t.Errorf("%s: the issue is still shown", code)
		}
	}
}

func TestObsIssueLocalHasNoDiscussion(t *testing.T) {
	noSuspects(t)
	sr := &scriptedRunner{answers: localIssueAnswers()}
	out, _, err := obsRun(t, devFacts(), sr, "issue", "sfp1-"+issueFP, "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	for _, k := range []string{"timeline", "timeline_note", "incident_issue_ids"} {
		if _, has := doc[k]; has {
			t.Errorf("a local issue has no %s: %v", k, doc[k])
		}
	}
	if _, has := doc["issue"].(map[string]any)["assignee"]; has {
		t.Error("a local issue is unassigned")
	}
	text, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: localIssueAnswers()}, "issue", "sfp1-"+issueFP, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "assignee") || strings.Contains(text, "timeline") {
		t.Errorf("the local text form is unchanged:\n%s", text)
	}
}

func TestObsIssuesCloudAssigneeFilterAndColumns(t *testing.T) {
	list := `{"issues":[` + discussedIssueJSON + `,` + strings.Replace(cloudIssueJSON, `ISS-7f3a`, `ISS-2`, 1) + `]}`
	fc := &fakeCloudCaller{replies: map[string]string{cloudList: list}}
	out, _, err := obsRunCloud(t, fc, "issues", "prod", "--assignee", "u-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fc.calls[0].body, `"assignee":"u-1"`) {
		t.Errorf("request lacks the assignee: %s", fc.calls[0].body)
	}
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[0], "ASSIGNEE") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "+2 related") || !strings.Contains(lines[1], "u-1") || !strings.Contains(lines[2], " -  ") {
		t.Errorf("rows = %q / %q", lines[1], lines[2])
	}

	jout, _, err := obsRunCloud(t, &fakeCloudCaller{replies: map[string]string{cloudList: list}}, "issues", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	rows := decode(t, jout)["issues"].([]any)
	if rows[0].(map[string]any)["assignee"].(map[string]any)["id"] != "u-1" || rows[0].(map[string]any)["incident_key"] != "sqlstate:57P01" {
		t.Errorf("row = %v", rows[0])
	}
	if _, has := rows[1].(map[string]any)["assignee"]; has {
		t.Errorf("an unassigned row has no assignee: %v", rows[1])
	}

	// The columns appear only when some issue has the datum: an ordinary list is unchanged.
	plain, _, err := obsRunCloud(t, &fakeCloudCaller{replies: map[string]string{cloudList: `{"issues":[` + cloudIssueJSON + `]}`}}, "issues", "prod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "ASSIGNEE") {
		t.Errorf("no assignee: the table is as before:\n%s", plain)
	}
}

func TestObsIssuesAssigneeFlag(t *testing.T) {
	for _, tc := range []struct {
		assignee string
		wantExit int
		say      string
	}{
		{"u-1", 0, ""},
		{"none", 0, ""},
		{"None", 0, ""},
		{"me", 0, ""},
		{" ME ", 0, ""},
		{" u-1 ", 0, ""},
		{strings.Repeat("x", 129), 1, "128"},
	} {
		fc := &fakeCloudCaller{replies: map[string]string{cloudList: `{}`}}
		out, _, err := obsRunCloud(t, fc, "issues", "prod", "--json", "--assignee", tc.assignee)
		if exitCodeForError(err) != tc.wantExit {
			t.Errorf("%q: exit %d, want %d (%v)", tc.assignee, exitCodeForError(err), tc.wantExit, err)
			continue
		}
		if tc.wantExit == 1 {
			if len(fc.calls) != 0 || !strings.Contains(decode(t, out)["error"].(string), tc.say) {
				t.Errorf("%q: calls=%d out=%s: a refused flag never reaches the control plane", tc.assignee, len(fc.calls), out)
			}
			continue
		}
		if len(fc.calls) != 1 || fc.calls[0].procedure != cloudList {
			t.Fatalf("%q: expected one list call, got %v", tc.assignee, fc.calls)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(fc.calls[0].body), &body); err != nil {
			t.Fatal(err)
		}
		want := strings.TrimSpace(tc.assignee)
		if strings.EqualFold(want, "none") {
			if body["unassigned"] != true || body["assignee"] != nil {
				t.Errorf("none must send only unassigned=true: %v", body)
			}
			continue
		}
		if strings.EqualFold(want, "me") {
			want = "me"
		}
		if body["assignee"] != want || body["unassigned"] != nil {
			t.Errorf("assignee %q: %v", want, body)
		}
	}
	// A local env has no assignees: the filter matches nothing, without a query.
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, devFacts(), sr, "issues", "dev", "--assignee", "u-1", "--json")
	if err != nil || len(sr.ran) != 0 || len(decode(t, out)["issues"].([]any)) != 0 {
		t.Errorf("local: err=%v ran=%d out=%s", err, len(sr.ran), out)
	}
}

// The CLI must not turn automation's missing acting user into an empty filter
// or an unassignment, nor translate a permission failure to a successful list.
func TestObsIssueMePreservesServerRefusals(t *testing.T) {
	for _, command := range []string{"list", "assign"} {
		for _, code := range []string{cloud.CodeFailedPrecondition, "permission_denied", "unauthenticated"} {
			t.Run(command+"/"+code, func(t *testing.T) {
				procedure := cloudList
				args := []string{"issues", "prod", "--assignee", "me", "--json"}
				wantCalls := 1
				if command == "assign" {
					procedure = cloudAssign
					args = []string{"issue", "assign", "ISS-7f3a", "me", "prod", "--json"}
					wantCalls = 2
				}
				refusal := &cloud.Error{Code: code, Message: "verified credential refusal"}
				fc := &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`}, errs: map[string]error{procedure: refusal}}
				out, _, err := obsRunCloud(t, fc, args...)
				var ce *cloud.Error
				if !errors.As(err, &ce) || ce.Code != code || exitCodeForError(err) != 2 {
					t.Fatalf("refusal lost: %v", err)
				}
				if len(fc.calls) != wantCalls || decode(t, out)["ok"] != false {
					t.Fatalf("must refuse without retries/fallbacks: calls=%v out=%s", fc.calls, out)
				}
			})
		}
	}
}

// ── comment ─────────────────────────────────────────────────────────────

const commentReply = `{"comment":{"id":"cm9","issueId":"ISS-7f3a","body":"checked the pool","createdAt":"2026-10-22T12:00:00Z","author":{"id":"u-1","name":"Ada"}}}`

func TestObsIssueComment(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{cloudComment: commentReply}}
	out, _, err := obsRunCloud(t, fc, "issue", "comment", "ISS-7f3a", "checked the pool", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.calls) != 1 || fc.calls[0].procedure != cloudComment || !strings.Contains(fc.calls[0].body, `"issueId":"ISS-7f3a"`) || !strings.Contains(fc.calls[0].body, `"body":"checked the pool"`) {
		t.Errorf("calls = %v", fc.calls)
	}
	doc := decode(t, out)
	c := doc["comment"].(map[string]any)
	if doc["ok"] != true || doc["schema"] != "forge.obs.issue/v1" || c["id"] != "cm9" || c["body"] != "checked the pool" || c["author"].(map[string]any)["id"] != "u-1" || doc["issue"].(map[string]any)["id"] != "ISS-7f3a" {
		t.Errorf("document = %v", doc)
	}
	text, _, err := obsRunCloud(t, &fakeCloudCaller{replies: map[string]string{cloudComment: commentReply}}, "issue", "comment", "ISS-7f3a", "checked the pool", "prod")
	if err != nil || text != "commented on ISS-7f3a (cm9)\n" {
		t.Errorf("text = %q %v", text, err)
	}
}

func TestObsIssueCommentFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		errs  map[string]error
		exit  int
		calls int
	}{
		{"empty text", []string{"issue", "comment", "ISS-1", "  ", "prod"}, nil, 1, 0},
		{"merged issue", []string{"issue", "comment", "ISS-1", "hi", "prod"}, map[string]error{cloudComment: &cloud.Error{Code: "failed_precondition"}}, 2, 1},
		{"no such issue", []string{"issue", "comment", "ISS-1", "hi", "prod"}, map[string]error{cloudComment: &cloud.Error{Code: "not_found"}}, 1, 1},
		{"no scope", []string{"issue", "comment", "ISS-1", "hi", "prod"}, map[string]error{cloudComment: &cloud.Error{Code: "permission_denied"}}, 2, 1},
		{"old control plane", []string{"issue", "comment", "ISS-1", "hi", "prod"}, map[string]error{cloudComment: &cloud.Error{Code: "unimplemented"}}, 2, 1},
		{"too few args", []string{"issue", "comment", "ISS-1"}, nil, 1, 0},
	} {
		fc := &fakeCloudCaller{replies: map[string]string{cloudComment: commentReply}, errs: tc.errs}
		out, _, err := obsRunCloud(t, fc, append(tc.args, "--json")...)
		if err == nil || exitCodeForError(err) != tc.exit || len(fc.calls) != tc.calls {
			t.Errorf("%s: err=%v exit %d (want %d), calls %d (want %d)", tc.name, err, exitCodeForError(err), tc.exit, len(fc.calls), tc.calls)
			continue
		}
		if tc.name != "too few args" {
			if doc := decode(t, out); doc["ok"] != false || doc["exit_code"] != float64(tc.exit) {
				t.Errorf("%s: document = %v", tc.name, doc)
			}
		}
	}
	// A local env has no discussion.
	_, _, err := obsRun(t, devFacts(), &scriptedRunner{}, "issue", "comment", "sfp1-"+issueFP, "hi", "dev")
	if exitCodeForError(err) != 2 {
		t.Errorf("local: exit %d, want 2 (%v)", exitCodeForError(err), err)
	}
}

// ── assign ──────────────────────────────────────────────────────────────

func assignedReply() string {
	return `{"issue":` + strings.Replace(discussedIssueJSON, `"version":4`, `"version":5`, 1) + `}`
}

func TestObsIssueAssignSendsTheReadVersion(t *testing.T) {
	for _, tc := range []struct {
		name, who  string
		wantUserID string
		reply      string
		wantText   string
	}{
		{"assign", "u-1", "u-1", assignedReply(), "ISS-7f3a is now assigned to Ada (u-1)\n"},
		{"self", "me", "me", assignedReply(), "ISS-7f3a is now assigned to Ada (u-1)\n"},
		{"self normalized", " ME ", "me", assignedReply(), "ISS-7f3a is now assigned to Ada (u-1)\n"},
		{"clear", "none", "", `{"issue":` + cloudIssueJSON + `}`, "ISS-7f3a is now unassigned\n"},
		{"clear upper", "NONE", "", `{"issue":` + cloudIssueJSON + `}`, "ISS-7f3a is now unassigned\n"},
	} {
		fc := &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudAssign: tc.reply}}
		text, _, err := obsRunCloud(t, fc, "issue", "assign", "ISS-7f3a", tc.who, "prod")
		if err != nil {
			t.Fatal(tc.name, err)
		}
		if len(fc.calls) != 2 || fc.calls[0].procedure != cloudIssue || fc.calls[1].procedure != cloudAssign {
			t.Fatalf("%s: calls = %v: the write must follow a read", tc.name, fc.calls)
		}
		body := fc.calls[1].body
		if !strings.Contains(body, `"expectedVersion":4`) || !strings.Contains(body, `"issueId":"ISS-7f3a"`) {
			t.Errorf("%s: write lacks the version read: %s", tc.name, body)
		}
		if has := strings.Contains(body, "assigneeUserId"); has != (tc.wantUserID != "") || (has && !strings.Contains(body, `"assigneeUserId":"`+tc.wantUserID+`"`)) {
			t.Errorf("%s: body = %s", tc.name, body)
		}
		if text != tc.wantText {
			t.Errorf("%s: text = %q, want %q", tc.name, text, tc.wantText)
		}
	}

	fc := &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudAssign: assignedReply()}}
	out, _, err := obsRunCloud(t, fc, "issue", "assign", "ISS-7f3a", "u-1", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if issue := doc["issue"].(map[string]any); doc["ok"] != true || issue["assignee"].(map[string]any)["id"] != "u-1" || issue["incident_key"] != "sqlstate:57P01" {
		t.Errorf("document = %v", doc)
	}
}

func TestObsIssueAssignAbortedIsExit1AndNeverRetried(t *testing.T) {
	fc := &fakeCloudCaller{
		replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`},
		errs:    map[string]error{cloudAssign: &cloud.Error{Code: "aborted"}},
	}
	out, _, err := obsRunCloud(t, fc, "issue", "assign", "ISS-7f3a", "u-1", "prod", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("exit %d, want 1 (%v)", exitCodeForError(err), err)
	}
	if !strings.Contains(err.Error(), "changed since you read it") {
		t.Errorf("err = %v", err)
	}
	if doc := decode(t, out); doc["ok"] != false || !strings.Contains(doc["error"].(string), "changed since you read it") {
		t.Errorf("document = %v", doc)
	}
	if len(fc.calls) != 2 {
		t.Errorf("%d calls, want 2: one read and one write, never a retry", len(fc.calls))
	}
}

func TestObsIssueAssignRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		errs map[string]error
		exit int
	}{
		{"a user id", []string{"u-1"}, nil, 0},
		{"automation has no me", []string{"me"}, map[string]error{cloudAssign: &cloud.Error{Code: "failed_precondition"}}, 2},
		{"blank", []string{" "}, nil, 1},
		{"not a member", []string{"u-9"}, map[string]error{cloudAssign: &cloud.Error{Code: "invalid_argument"}}, 1},
		{"merged issue", []string{"u-9"}, map[string]error{cloudAssign: &cloud.Error{Code: "failed_precondition"}}, 2},
		{"no scope", []string{"u-9"}, map[string]error{cloudAssign: &cloud.Error{Code: "permission_denied"}}, 2},
	} {
		fc := &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudAssign: assignedReply()}, errs: tc.errs}
		args := append([]string{"issue", "assign", "ISS-7f3a"}, tc.args...)
		_, _, err := obsRunCloud(t, fc, append(args, "prod")...)
		if exitCodeForError(err) != tc.exit {
			t.Errorf("%s: exit %d, want %d (%v)", tc.name, exitCodeForError(err), tc.exit, err)
		}
		if tc.name == "blank" && len(fc.calls) != 0 {
			t.Errorf("%s: %d calls: a refused argument never reaches the control plane", tc.name, len(fc.calls))
		}
	}
	_, _, err := obsRun(t, devFacts(), &scriptedRunner{}, "issue", "assign", "sfp1-"+issueFP, "u-1", "dev")
	if exitCodeForError(err) != 2 {
		t.Errorf("local: exit %d, want 2 (%v)", exitCodeForError(err), err)
	}
}

func TestObsIssueDiscussionVerbsAreRegistered(t *testing.T) {
	cmd := newTestRootCmd(t)
	for _, path := range [][]string{{"obs", "issue", "comment"}, {"obs", "issue", "assign"}} {
		if c, _, err := cmd.Find(path); err != nil || c.Name() != path[len(path)-1] {
			t.Errorf("%v not registered: %v", path, err)
		}
	}
}

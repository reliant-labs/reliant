package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/suspect"
	"github.com/reliant-labs/forge/pkg/obsquery"
)

const (
	issueFP     = "0123456789abcdef"
	issueTrace  = "0af7651916cd43dd8448eb211c80319c"
	cloudIssue  = "controlplane.v1.IssuesService/GetIssue"
	cloudList   = "controlplane.v1.IssuesService/ListIssues"
	cloudEvents = "controlplane.v1.IssuesService/ListIssueEvents"
	cloudUpdate = "controlplane.v1.IssuesService/UpdateIssueStatus"
)

func issuesAnswer() scriptedAnswer {
	row := func(fp, svc, title string, events int) []any {
		return []any{fp, svc, title, "internal/store/db.go in store.(*DB).Exec", "*pgconn.PgError", jn(strconv.Itoa(events)), jn("3"),
			"2026-10-22 10:00:00.000000000", "2026-10-22 11:30:00.000000000", issueTrace, "v1.4.0", "v1.3.0", "", "users"}
	}
	return scriptedAnswer{"GROUP BY fingerprint, service", obsbackend.Result{Rows: [][]any{
		row(issueFP, "api", "*pgconn.PgError: duplicate key <str>", 7),
		row("aaaaaaaaaaaaaaaa", "worker", "timeout <n>", 2),
	}}}
}

func TestObsIssuesLocalJSONGolden(t *testing.T) {
	sr := &scriptedRunner{answers: []scriptedAnswer{issuesAnswer()}}
	out, _, err := obsRun(t, devFacts(), sr, "issues", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "issues.json", out)
	doc := decode(t, out)
	for k, want := range map[string]any{"schema": "forge.obs.issues/v1", "ok": true, "query_mode": "structured", "sort": "last"} {
		if doc[k] != want {
			t.Errorf("%s = %v, want %v", k, doc[k], want)
		}
	}
	first := doc["issues"].([]any)[0].(map[string]any)
	if first["id"] != "sfp1-"+issueFP || first["status"] != "unresolved" || first["latest_trace_id"] != issueTrace {
		t.Errorf("first issue = %v", first)
	}
	sug := strings.Join(toStrings(doc["suggested"]), "|")
	if !strings.Contains(sug, "forge obs issue sfp1-"+issueFP+" dev") {
		t.Errorf("suggested = %s", sug)
	}
	// The list, then the incident search over the same fingerprint expression.
	if len(sr.ran) != 2 || !strings.Contains(sr.ran[0], obsquery.ServerFingerprintSQL) || !strings.Contains(sr.ran[1], obsquery.ServerCauseSQL) {
		t.Errorf("the local statements must group by the shared fingerprint expression and then read the incident causes: %v", sr.ran)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestObsIssuesLocalPrintSQLAndFlagValidation(t *testing.T) {
	sr := &scriptedRunner{}
	out, _, err := obsRun(t, devFacts(), sr, "issues", "dev", "--explain", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.ran) != 0 {
		t.Fatal("--explain ran a statement")
	}
	st := decode(t, out)["statements"].([]any)[0].(map[string]any)
	if !strings.Contains(st["sql"].(string), "GROUP BY fingerprint") || st["params"] == nil {
		t.Errorf("statement = %v", st)
	}
	for _, args := range [][]string{
		{"issues", "dev", "--status", "closed"},
		{"issues", "dev", "--platform", "mobile"},
		{"issues", "dev", "--sort", "id"},
		{"issues", "dev", "--limit", "0"},
	} {
		if _, _, err := obsRun(t, devFacts(), &scriptedRunner{}, args...); exitCodeForError(err) != 1 {
			t.Errorf("%v: exit %d, want 1 (%v)", args, exitCodeForError(err), err)
		}
	}
}

func TestObsIssuesLocalReleaseFiltersExit2(t *testing.T) {
	_, _, err := obsRun(t, devFacts(), &scriptedRunner{}, "issues", "dev", "--since-release", "v1")
	if exitCodeForError(err) != 2 {
		t.Errorf("exit %d, want 2: %v", exitCodeForError(err), err)
	}
}

// ── issue ───────────────────────────────────────────────────────────────

func localIssueAnswers() []scriptedAnswer {
	ev := []any{"2026-10-22 11:30:00.000000000", issueTrace, "b7ad6b7169203331", "api", "v1.4.0", "insert failed",
		map[string]any{"code.function.name": "store.(*DB).Exec", "code.file.path": "internal/store/db.go", "code.line.number": "88"}}
	return []scriptedAnswer{
		issuesAnswer(),
		{"ORDER BY Timestamp DESC", obsbackend.Result{Rows: [][]any{ev}}},
		{"GROUP BY version", obsbackend.Result{Rows: [][]any{{"v1.4.0", jn("5"), jn("2"), "2026-10-22 10:30:00", "2026-10-22 11:30:00", "users"}}}},
		{"uniqExact(TraceId) AS shared", obsbackend.Result{Rows: [][]any{{"bbbbbbbbbbbbbbbb", jn("2")}}}},
	}
}

func withSuspects(t *testing.T, fn func(ctx context.Context, commitRange string, files []string) ([]suspect.Commit, error)) {
	t.Helper()
	old := obsIssueSuspects
	t.Cleanup(func() { obsIssueSuspects = old })
	obsIssueSuspects = fn
}

func TestObsIssueLocalJSONGolden(t *testing.T) {
	var gotRange string
	var gotFiles []string
	withSuspects(t, func(_ context.Context, r string, f []string) ([]suspect.Commit, error) {
		gotRange, gotFiles = r, f
		return []suspect.Commit{{SHA: "abc1234def5678abc1234def5678abc1234def56", Subject: "retry on conflict (#12)", Author: "Ada",
			Time: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC), Files: []string{"internal/store/db.go"}, PR: 12}}, nil
	})
	sr := &scriptedRunner{answers: localIssueAnswers()}
	out, _, err := obsRun(t, devFacts(), sr, "issue", "sfp1-"+issueFP, "dev", "--json", "--events", "1")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "issue.json", out)
	if gotRange != "" || len(gotFiles) == 0 || gotFiles[0] != "internal/store/db.go" {
		t.Errorf("locally there is no release range (the last commits touching the culprit file); got range %q files %v", gotRange, gotFiles)
	}
	doc := decode(t, out)
	if doc["schema"] != "forge.obs.issue/v1" || doc["query_mode"] != "structured" {
		t.Errorf("head = %v %v", doc["schema"], doc["query_mode"])
	}
	for _, k := range []string{"issue", "latest_event", "frames", "by_version", "co_occurring_issue_ids", "suspect_commits", "events"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("document lacks %s", k)
		}
	}
	if _, has := doc["first_release"]; has {
		t.Error("a local issue has no release ledger; absent means unknown")
	}
	sug := strings.Join(toStrings(doc["suggested"]), "|")
	for _, want := range []string{"forge obs trace " + issueTrace + " dev", "forge obs logs dev --trace " + issueTrace} {
		if !strings.Contains(sug, want) {
			t.Errorf("suggested lacks %q: %s", want, sug)
		}
	}
	if strings.Contains(sug, "resolve") {
		t.Errorf("a local issue cannot be resolved, so resolve is not suggested: %s", sug)
	}
}

func TestObsIssueDegradesWithoutACheckout(t *testing.T) {
	withSuspects(t, func(context.Context, string, []string) ([]suspect.Commit, error) { return nil, suspect.ErrNotCheckout })
	sr := &scriptedRunner{answers: localIssueAnswers()}
	out, errOut, err := obsRun(t, devFacts(), sr, "issue", "sfp1-"+issueFP, "dev", "--json")
	if err != nil {
		t.Fatalf("a missing checkout must not fail the verb: %v", err)
	}
	doc := decode(t, out)
	if sc, ok := doc["suspect_commits"].([]any); !ok || len(sc) != 0 {
		t.Errorf("suspect_commits = %v, want []", doc["suspect_commits"])
	}
	if !strings.Contains(doc["suspect_note"].(string), "not inside a git checkout") || !strings.Contains(errOut, "not inside a git checkout") {
		t.Errorf("note = %v, stderr = %q", doc["suspect_note"], errOut)
	}
}

func TestObsIssueLocalTriageExits2(t *testing.T) {
	for _, verb := range []string{"resolve", "ignore", "unresolve"} {
		out, _, err := obsRun(t, devFacts(), &scriptedRunner{}, "issue", verb, "sfp1-"+issueFP, "dev", "--json")
		if exitCodeForError(err) != 2 {
			t.Errorf("%s: exit %d, want 2: %v", verb, exitCodeForError(err), err)
		}
		doc := decode(t, out)
		if doc["ok"] != false || !strings.Contains(doc["error"].(string), "local issues carry no state; triage on a hosted env") {
			t.Errorf("%s: document = %v", verb, doc)
		}
	}
}

func TestObsIssueRealSuspectsFromATempRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=a@b.c", "GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=a@b.c", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.MkdirAll(filepath.Join(dir, "internal/store"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal/store/db.go"), []byte("package store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "fix pool leak (#31)")
	withSuspects(t, func(ctx context.Context, r string, f []string) ([]suspect.Commit, error) {
		return suspect.Finder{Dir: dir}.Find(ctx, r, f)
	})
	out, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: localIssueAnswers()}, "issue", "sfp1-"+issueFP, "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	sc := decode(t, out)["suspect_commits"].([]any)
	if len(sc) != 1 || sc[0].(map[string]any)["pr"] != float64(31) || sc[0].(map[string]any)["subject"] != "fix pool leak (#31)" {
		t.Errorf("suspect_commits = %v", sc)
	}
}

// ── cloud ───────────────────────────────────────────────────────────────

const cloudIssueJSON = `{"id":"ISS-7f3a","platform":"ISSUE_PLATFORM_SERVER","envId":"env-prod","service":"api","fingerprint":"` + issueFP + `",
"title":"boom","culprit":"internal/store/db.go in store.Exec","status":"ISSUE_STATUS_REGRESSED","firstSeenAt":"2026-10-20T10:00:00Z",
"firstVersion":"v1.3.0","lastSeenAt":"2026-10-22T11:00:00Z","lastVersion":"v1.4.0","events":"42","affected":"9","version":4,
"regressedAt":"2026-10-22T09:00:00Z"}`

func issuesCloudFacts() obsbackend.Facts {
	return obsbackend.Facts{Env: "prod", Declared: true, Enabled: true, HasHosted: true, ControlPlane: "https://cp"}
}

// cloudLiveRelease is the release the cloud tests' ledger says was live: v1.4.0,
// promoted two hours before the moment asked about.
func cloudLiveRelease(_ context.Context, _ string, at time.Time) (*liveRelease, error) {
	return &liveRelease{Version: "v1.4.0", PromotionID: "p-14", PromotedAt: at.Add(-2 * time.Hour).UTC()}, nil
}

func obsRunCloud(t *testing.T, fc *fakeCloudCaller, args ...string) (string, string, error) {
	t.Helper()
	return obsRunCloudWith(t, fc, cloudLiveRelease, args...)
}

func obsRunCloudWith(t *testing.T, fc *fakeCloudCaller, release func(context.Context, string, time.Time) (*liveRelease, error), args ...string) (string, string, error) {
	t.Helper()
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return issuesCloudFacts(), nil },
		Cloud: func(_ context.Context, env string) (obsbackend.Cloud, error) {
			return obsbackend.Cloud{Env: env, EnvID: "env-prod", OrgID: "org-1", Caller: fc}, nil
		},
		Now:     func() time.Time { return obsNow },
		Release: release,
	}
	var out, errb bytes.Buffer
	cmd := newTestRootCmd(t)
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs(append([]string{"obs"}, args...))
	err := cmd.ExecuteContext(context.Background())
	return out.String(), errb.String(), err
}

func TestObsIssuesCloudJSONGoldenAndRequestShape(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{cloudList: `{"issues":[` + cloudIssueJSON + `],"nextPageToken":"tok2"}`}}
	out, _, err := obsRunCloud(t, fc, "issues", "prod", "--json", "--status", "regressed", "--platform", "server",
		"--since-release", "v1.4.0", "--since-deploy", "promo-9", "--service", "api", "--sort", "events", "--limit", "10", "--page-token", "tok1")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "issues.cloud.json", out)
	body := fc.calls[0].body
	for _, want := range []string{`"orgId":"org-1"`, `"envIds":["env-prod"]`, `"services":["api"]`, `"statuses":["ISSUE_STATUS_REGRESSED"]`,
		`"platform":"ISSUE_PLATFORM_SERVER"`, `"firstSeenRelease":"v1.4.0"`, `"sincePromotionId":"promo-9"`, `"sort":"ISSUE_SORT_EVENTS_DESC"`,
		`"pageSize":10`, `"pageToken":"tok1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("request lacks %s:\n%s", want, body)
		}
	}
	doc := decode(t, out)
	if doc["truncated"] != true || doc["next_page_token"] != wrapPageToken(cloudTestRange, "tok2") {
		t.Errorf("page = %v %v", doc["truncated"], doc["next_page_token"])
	}
	if sc := doc["scope"].(map[string]any); sc["backend"] != "cloud" || sc["env_id"] != "env-prod" {
		t.Errorf("scope = %v", sc)
	}
}

func TestObsIssueCloudJSONGolden(t *testing.T) {
	var gotRange string
	withSuspects(t, func(_ context.Context, r string, _ []string) ([]suspect.Commit, error) {
		gotRange = r
		return []suspect.Commit{{SHA: "abc1234def5678abc1234def5678abc1234def56", Subject: "retry on conflict (#12)", Author: "Ada",
			Time: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC), Files: []string{"internal/store/db.go"}, PR: 12}}, nil
	})
	fc := &fakeCloudCaller{replies: map[string]string{
		cloudIssue: `{"issue":` + cloudIssueJSON + `,
 "latestEvent":{"timestamp":"2026-10-22T11:00:00Z","traceId":"` + issueTrace + `","serviceVersion":"v1.4.0","message":"insert failed"},
 "frames":[{"function":"store.Exec","file":"internal/store/db.go","line":88,"inApp":true}],
 "byVersion":[{"version":"v1.4.0","events":"30","affected":"6"}],
 "coOccurringIssueIds":["ISS-1"],
 "firstRelease":{"version":"v1.3.0","gitCommit":"abc1234","commitRange":"aaa1111..abc1234","promotedAt":"2026-10-20T09:00:00Z"}}`,
		cloudEvents:   `{"events":[{"timestamp":"2026-10-22T11:00:00Z","traceId":"` + issueTrace + `","message":"insert failed"}]}`,
		cloudTimeline: `{}`,
	}}
	out, _, err := obsRunCloud(t, fc, "issue", "ISS-7f3a", "prod", "--json", "--events", "1")
	if err != nil {
		t.Fatal(err)
	}
	obsGolden(t, "issue.cloud.json", out)
	if gotRange != "aaa1111..abc1234" {
		t.Errorf("the suspect range comes from first_release.commit_range, got %q", gotRange)
	}
	doc := decode(t, out)
	if fr := doc["first_release"].(map[string]any); fr["version"] != "v1.3.0" {
		t.Errorf("first_release = %v", fr)
	}
	// Last seen 11:00; the ledger's v1.4.0 was promoted two hours before.
	if rel := doc["release"].(map[string]any); rel["version"] != "v1.4.0" || rel["promotion_id"] != "p-14" || rel["promoted_at"] != "2026-10-22T09:00:00Z" {
		t.Errorf("release = %v", rel)
	}
	sug := strings.Join(toStrings(doc["suggested"]), "|")
	for _, want := range []string{"forge obs trace " + issueTrace + " prod", "forge obs logs prod --trace " + issueTrace, "forge obs issue resolve ISS-7f3a prod --in next"} {
		if !strings.Contains(sug, want) {
			t.Errorf("suggested lacks %q: %s", want, sug)
		}
	}
}

func TestObsIssueTriageCloudSendsTheReadVersion(t *testing.T) {
	updated := strings.Replace(cloudIssueJSON, "ISSUE_STATUS_REGRESSED", "ISSUE_STATUS_RESOLVED", 1)
	fc := &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudUpdate: `{"issue":` + updated + `}`}}
	out, _, err := obsRunCloud(t, fc, "issue", "resolve", "ISS-7f3a", "prod", "--in", "next", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(fc.calls) != 2 || fc.calls[0].procedure != cloudIssue || fc.calls[1].procedure != cloudUpdate {
		t.Fatalf("calls = %v: the write must follow a read", fc.calls)
	}
	for _, want := range []string{`"expectedVersion":4`, `"envId":"env-prod"`, `"status":"ISSUE_STATUS_CHANGE_RESOLVED"`, `"kind":"ISSUE_RESOLVE_KIND_NEXT_RELEASE"`} {
		if !strings.Contains(fc.calls[1].body, want) {
			t.Errorf("update lacks %s:\n%s", want, fc.calls[1].body)
		}
	}
	if doc := decode(t, out); doc["issue"].(map[string]any)["status"] != "resolved" || doc["ok"] != true {
		t.Errorf("document = %v", doc)
	}

	fc = &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudUpdate: `{"issue":` + updated + `}`}}
	if _, _, err = obsRunCloud(t, fc, "issue", "resolve", "ISS-7f3a", "prod", "--in", "v1.5.0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fc.calls[1].body, `"kind":"ISSUE_RESOLVE_KIND_VERSION","version":"v1.5.0"`) {
		t.Errorf("--in <version>: %s", fc.calls[1].body)
	}
	fc = &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`, cloudUpdate: `{"issue":` + updated + `}`}}
	if _, _, err = obsRunCloud(t, fc, "issue", "ignore", "ISS-7f3a", "prod", "--until", "72h"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fc.calls[1].body, `"ignoredUntil":"2026-10-25T12:00:00Z"`) {
		t.Errorf("--until 72h from the clock: %s", fc.calls[1].body)
	}
	if _, _, err = obsRunCloud(t, fc, "issue", "ignore", "ISS-7f3a", "prod", "--until", "someday"); exitCodeForError(err) != 1 {
		t.Errorf("bad --until: exit %d, want 1", exitCodeForError(err))
	}
}

func TestObsIssuesCloudErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		code string
		exit int
		say  string
	}{
		{"unavailable", 2, ""},
		{"invalid_argument", 1, ""},
		{"unimplemented", 2, "the control plane does not serve issues yet"},
	} {
		fc := &fakeCloudCaller{errs: map[string]error{cloudList: &cloud.Error{Code: tc.code}}}
		out, _, err := obsRunCloud(t, fc, "issues", "prod", "--json")
		if exitCodeForError(err) != tc.exit {
			t.Errorf("%s: exit %d, want %d (%v)", tc.code, exitCodeForError(err), tc.exit, err)
		}
		doc := decode(t, out)
		if doc["exit_code"] != float64(tc.exit) || doc["ok"] != false {
			t.Errorf("%s: document = %v", tc.code, doc)
		}
		if tc.say != "" && !strings.Contains(doc["error"].(string), tc.say) {
			t.Errorf("%s: error %q should say %q, never read as a pass", tc.code, doc["error"], tc.say)
		}
	}
}

func TestObsIssueTriageCloudStaleWriteIsExit1(t *testing.T) {
	fc := &fakeCloudCaller{
		replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`},
		errs:    map[string]error{cloudUpdate: &cloud.Error{Code: "aborted"}},
	}
	out, _, err := obsRunCloud(t, fc, "issue", "unresolve", "ISS-7f3a", "prod", "--json")
	if exitCodeForError(err) != 1 {
		t.Fatalf("exit %d, want 1: %v", exitCodeForError(err), err)
	}
	if doc := decode(t, out); !strings.Contains(doc["error"].(string), "re-read and retry") {
		t.Errorf("error = %v", doc["error"])
	}
}

func TestObsIssuesVerbsAreRegistered(t *testing.T) {
	cmd := newTestRootCmd(t)
	for _, path := range [][]string{{"obs", "issues"}, {"obs", "issue"}, {"obs", "issue", "resolve"}, {"obs", "issue", "ignore"}, {"obs", "issue", "unresolve"}} {
		if c, _, err := cmd.Find(path); err != nil || c.Name() != path[len(path)-1] {
			t.Errorf("%v not registered: %v", path, err)
		}
	}
}

// The release stamp reads the ledger at the issue's last_seen; an unreadable
// ledger warns and leaves the field out, never failing the verb.
func TestObsIssueStampsTheReleaseLiveWhenLastSeen(t *testing.T) {
	withSuspects(t, func(context.Context, string, []string) ([]suspect.Commit, error) { return nil, nil })
	fc := func() *fakeCloudCaller {
		return &fakeCloudCaller{replies: map[string]string{cloudIssue: `{"issue":` + cloudIssueJSON + `}`}}
	}
	var askedAt time.Time
	out, _, err := obsRunCloudWith(t, fc(), func(ctx context.Context, env string, at time.Time) (*liveRelease, error) {
		askedAt = at
		return cloudLiveRelease(ctx, env, at)
	}, "issue", "ISS-7f3a", "prod", "--json", "--events", "0")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC); !askedAt.Equal(want) {
		t.Errorf("looked the release up at %v, want last_seen %v", askedAt, want)
	}
	if _, ok := decode(t, out)["release"]; !ok {
		t.Errorf("no release:\n%s", out)
	}
	text, _, err := obsRunCloud(t, fc(), "issue", "ISS-7f3a", "prod", "--events", "0")
	if err != nil || !strings.Contains(text, "live when last seen: v1.4.0 (promoted 2026-10-22T09:00:00Z)") {
		t.Errorf("text (%v):\n%s", err, text)
	}

	out, errOut, err := obsRunCloudWith(t, fc(), func(context.Context, string, time.Time) (*liveRelease, error) {
		return nil, errors.New("ledger unreadable")
	}, "issue", "ISS-7f3a", "prod", "--json", "--events", "0")
	if err != nil {
		t.Fatalf("an unreadable ledger failed the verb: %v", err)
	}
	if _, ok := decode(t, out)["release"]; ok || !strings.Contains(errOut, "release live in prod is unknown") {
		t.Errorf("stdout %s stderr %s", out, errOut)
	}
}

// Affected users cannot be answered when the events name none: most worker and
// reconciler records carry no user.id. The table says what was counted instead of
// reporting zero users, and the JSON carries the unit beside the count.
func TestObsIssuesCountsChatsWhenNoEventNamesAUser(t *testing.T) {
	row := []any{issueFP, "worker", "timeout <n>", "internal/x.go in x.Run", "*errors.errorString", jn("40"), jn("1"),
		"2026-10-22 10:00:00.000000000", "2026-10-22 11:30:00.000000000", issueTrace, "v1.4.0", "v1.3.0", "", "chats"}
	sr := &scriptedRunner{answers: []scriptedAnswer{{"GROUP BY fingerprint, service", obsbackend.Result{Rows: [][]any{row}}}}}

	out, _, err := obsRun(t, devFacts(), sr, "issues", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1 chat") || strings.Contains(out, "1 chats") {
		t.Errorf("table does not name the counted unit in the singular:\n%s", out)
	}

	sr = &scriptedRunner{answers: []scriptedAnswer{{"GROUP BY fingerprint, service", obsbackend.Result{Rows: [][]any{row}}}}}
	js, _, err := obsRun(t, devFacts(), sr, "issues", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	first := decode(t, js)["issues"].([]any)[0].(map[string]any)
	if first["affected"] != float64(1) || first["affected_unit"] != "chats" {
		t.Errorf("affected = %v %v, want 1 chats", first["affected"], first["affected_unit"])
	}

	if got := affectedText(3, "chats"); got != "3 chats" {
		t.Errorf("affectedText(3, chats) = %q", got)
	}
	if got := affectedText(3, ""); got != "3" {
		t.Errorf("an unknown unit is the number alone, got %q", got)
	}
}

// A control plane that predates affectedUnit says what its proto documents.
func TestObsIssuesCloudDefaultsTheUnitFromThePlatform(t *testing.T) {
	fc := &fakeCloudCaller{replies: map[string]string{cloudList: `{"issues":[` + cloudIssueJSON + `]}`}}
	out, _, err := obsRunCloud(t, fc, "issues", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	first := decode(t, out)["issues"].([]any)[0].(map[string]any)
	if first["affected_unit"] != "traces" {
		t.Errorf("affected_unit = %v, want traces (a server issue from a control plane that names none)", first["affected_unit"])
	}

	withUnit := strings.Replace(cloudIssueJSON, `"affected":"9"`, `"affected":"9","affectedUnit":"chats"`, 1)
	fc = &fakeCloudCaller{replies: map[string]string{cloudList: `{"issues":[` + withUnit + `]}`}}
	out, _, err = obsRunCloud(t, fc, "issues", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, out)["issues"].([]any)[0].(map[string]any)["affected_unit"]; got != "chats" {
		t.Errorf("affected_unit = %v, want the control plane's chats", got)
	}
}

// T6: a dead dependency fails every caller. The list collapses the issues of one
// incident under the first, says how many it hid, and --expand-incidents lists
// them all. Nothing is merged: the hidden issues keep their own rows.
func incidentAnswers() []scriptedAnswer {
	row := func(fp, svc, title string, events int) []any {
		return []any{fp, svc, title, "internal/store/db.go in store.(*DB).Exec", "", jn(strconv.Itoa(events)), jn("3"),
			"2026-10-22 10:00:00.000000000", "2026-10-22 11:30:00.000000000", issueTrace, "v1.4.0", "v1.3.0", "", "traces"}
	}
	return []scriptedAnswer{
		{"GROUP BY fingerprint, service", obsbackend.Result{Rows: [][]any{
			row(issueFP, "api", "Transaction failed", 40),
			row("aaaaaaaaaaaaaaaa", "worker", "Persistent fetch failed", 12),
			row("bbbbbbbbbbbbbbbb", "reconciler", "sweep failed", 5),
			row("cccccccccccccccc", "api", "unrelated", 1),
		}}},
		{"AS cause", obsbackend.Result{Rows: [][]any{
			{issueFP, "api", "SQLSTATE 57P01", "2026-10-22 10:00:00.000000000"},
			{"aaaaaaaaaaaaaaaa", "worker", "SQLSTATE 57P01", "2026-10-22 10:02:00.000000000"},
			{"bbbbbbbbbbbbbbbb", "reconciler", "SQLSTATE 57P01", "2026-10-22 10:05:00.000000000"},
			{"cccccccccccccccc", "api", "context deadline exceeded", "2026-10-22 10:01:00.000000000"},
		}}},
	}
}

func TestObsIssuesCollapsesAnIncidentByDefault(t *testing.T) {
	out, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: incidentAnswers()}, "issues", "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	issues := doc["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("issues = %d, want the incident's first issue and the unrelated one", len(issues))
	}
	first := issues[0].(map[string]any)
	if first["id"] != "sfp1-"+issueFP || first["related_count"] != float64(2) || !strings.HasPrefix(first["incident_key"].(string), "inc1-") {
		t.Errorf("first = %v", first)
	}
	if issues[1].(map[string]any)["incident_key"] != nil {
		t.Errorf("an issue alone with its cause is no incident: %v", issues[1])
	}
	if doc["collapsed"] != float64(2) {
		t.Errorf("collapsed = %v, want 2", doc["collapsed"])
	}
	if sug := strings.Join(toStrings(doc["suggested"]), "|"); strings.Contains(sug, "aaaaaaaaaaaaaaaa") {
		t.Errorf("a hidden issue is suggested: %s", sug)
	}

	out, _, err = obsRun(t, devFacts(), &scriptedRunner{answers: incidentAnswers()}, "issues", "dev", "--json", "--expand-incidents")
	if err != nil {
		t.Fatal(err)
	}
	doc = decode(t, out)
	if n := len(doc["issues"].([]any)); n != 4 || doc["collapsed"] != nil {
		t.Errorf("--expand-incidents: %d issues, collapsed %v; want all 4", n, doc["collapsed"])
	}
	keys := map[any]int{}
	for _, i := range doc["issues"].([]any) {
		keys[i.(map[string]any)["incident_key"]]++
	}
	if keys[nil] != 1 || len(keys) != 2 {
		t.Errorf("incident keys = %v: three issues must share one key", keys)
	}
}

func TestObsIssuesTextShowsTheCollapsedIncident(t *testing.T) {
	out, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: incidentAnswers()}, "issues", "dev")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+2 related, incident inc1-", "2 more issues of the same incidents are collapsed", "--expand-incidents"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Persistent fetch failed") {
		t.Errorf("a collapsed issue is listed:\n%s", out)
	}
}

func TestObsIssuesIncidentWindowIsValidated(t *testing.T) {
	for _, w := range []string{"0s", "-1m", "2h"} {
		if _, _, err := obsRun(t, devFacts(), &scriptedRunner{}, "issues", "dev", "--incident-window", w); exitCodeForError(err) != 1 {
			t.Errorf("--incident-window %s: exit %d, want 1 (%v)", w, exitCodeForError(err), err)
		}
	}
}

func TestObsIssueListsTheRestOfItsIncident(t *testing.T) {
	withSuspects(t, func(context.Context, string, []string) ([]suspect.Commit, error) { return nil, nil })
	answers := append(localIssueAnswers(), scriptedAnswer{"AS cause", obsbackend.Result{Rows: [][]any{
		{issueFP, "api", "SQLSTATE 57P01", "2026-10-22 10:00:00.000000000"},
		{"aaaaaaaaaaaaaaaa", "worker", "SQLSTATE 57P01", "2026-10-22 10:03:00.000000000"},
	}}})
	out, _, err := obsRun(t, devFacts(), &scriptedRunner{answers: answers}, "issue", "sfp1-"+issueFP, "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, out)
	if ids := toStrings(doc["incident_issue_ids"]); len(ids) != 1 || ids[0] != "sfp1-aaaaaaaaaaaaaaaa" {
		t.Errorf("incident_issue_ids = %v", ids)
	}
	if doc["issue"].(map[string]any)["related_count"] != float64(1) {
		t.Errorf("issue = %v", doc["issue"])
	}
}

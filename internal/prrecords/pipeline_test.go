// Copyright (c) 2026 Reliant Labs
package prrecords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/ghcontents"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/pipeline/config"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
)

// The recorded GitHub deliveries the receiver's own tests parse
// (internal/integrations/webhook/github/testdata): PR #7 of acme/app,
// repository 123456, opened at head aaaa…, pushed to cccc…, then closed.
const (
	recordedRepositoryID = "123456"
	recordedHead         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	recordedPushedHead   = "cccccccccccccccccccccccccccccccccccccccc"
	recordedPushSHA      = "eeee000000000000000000000000000000000002"
)

// recorded parses one recorded delivery, after edit (when set) rewrites its
// body, into the projector's events.
func recorded(t *testing.T, event, file string, edit func(map[string]any)) []Event {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "integrations", "webhook", "github", "testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc)
		if body, err = json.Marshal(doc); err != nil {
			t.Fatal(err)
		}
	}
	parsed, err := github.Parse(event, body, time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Events) != 1 {
		t.Fatalf("%s parsed to %d events", file, len(parsed.Events))
	}
	ev := parsed.Events[0]
	return []Event{{Type: ev.Type, AccountKey: ev.AccountKey, ResourceKey: ev.ResourceKey, Attributes: ev.Attributes, Data: ev.Data}}
}

type fakeContents struct {
	files map[string]string // ref -> file
	err   error
	reqs  []ghcontents.Request
}

func (f *fakeContents) Read(_ context.Context, req ghcontents.Request) ([]byte, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.files[req.Ref]
	if !ok {
		return nil, ghcontents.ErrNotFound
	}
	return []byte(data), nil
}

type pipelineHarness struct {
	t        *testing.T
	env      *storetest.Env
	p        *projector
	contents *fakeContents
	now      time.Time
	key      int
}

func newPipelineHarness(t *testing.T) *pipelineHarness {
	t.Helper()
	env := storetest.New(t)
	h := &pipelineHarness{t: t, env: env, contents: &fakeContents{files: map[string]string{}},
		now: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)}
	h.p = New(Deps{Store: store.NewPrRecordExtStore(env.ORM), Scope: NewScope(nil), Pipeline: env.Repo, Contents: h.contents}).(*projector)
	h.p.now = func() time.Time { return h.now }
	return h
}

// enrol enrols acme/app (repository 123456) in mode.
func (h *pipelineHarness) enrol(mode core.PipelineRepoMode) *core.PipelineRepo {
	h.t.Helper()
	ctx := context.Background()
	r, err := h.env.Repo.UpsertPipelineRepo(ctx, core.PipelineRepoEnrolment{
		OrgID: "org-1", ProjectID: storetest.SeedProjectID, Provider: ProviderGitHub, AccountKey: "98765",
		RepositoryID: recordedRepositoryID, Repository: "acme/app", CreatedByUserID: storetest.SeedUserID,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	if mode != r.Mode {
		if r, err = h.env.Repo.SetPipelineRepoMode(ctx, r.ID, r.Version, mode); err != nil {
			h.t.Fatal(err)
		}
	}
	return r
}

// apply applies events as a new delivery.
func (h *pipelineHarness) apply(events []Event) {
	h.t.Helper()
	h.key++
	if err := h.p.Apply(context.Background(), Delivery{Provider: ProviderGitHub, Key: fmt.Sprintf("d-%d", h.key), Events: events}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *pipelineHarness) record() *core.PRRecord {
	h.t.Helper()
	rec, err := h.env.Repo.GetPRRecordByNumber(context.Background(), ProviderGitHub, recordedRepositoryID, 7)
	if err != nil {
		h.t.Fatal(err)
	}
	return rec
}

func (h *pipelineHarness) row() *core.PRPipeline {
	h.t.Helper()
	rec := h.record()
	if rec == nil {
		return nil
	}
	row, err := h.env.Repo.GetPRPipeline(context.Background(), rec.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return row
}

// settle is pr.next having decided: nothing is due.
func (h *pipelineHarness) settle(change func(*core.PRPipeline)) *core.PRPipeline {
	h.t.Helper()
	row := h.row()
	to := *row
	to.NextAction, to.NextActionAt = "", nil
	if change != nil {
		change(&to)
	}
	out, err := h.env.Repo.TransitionPRPipeline(context.Background(), row.RecordID, row.Version, to)
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *pipelineHarness) wantDue(row *core.PRPipeline) {
	h.t.Helper()
	if row.NextAction != NextActionDecide || row.NextActionAt == nil || !row.NextActionAt.Equal(h.now) {
		h.t.Fatalf("want a decision due now (%s), got %q at %v", h.now, row.NextAction, row.NextActionAt)
	}
}

func TestPipelineProjectionFollowsAnEnrolledPR(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	repo := h.enrol(core.PipelineRepoModeShadow)

	// Opened: the PR gets its row, due for a decision.
	h.apply(recorded(t, "pull_request", "pull_request.opened.json", nil))
	row := h.row()
	if row == nil || row.State != core.PRPipelineAwaitingReview || row.StateHeadSHA != recordedHead ||
		row.RepoID != repo.ID || row.OrgID != "org-1" || row.ProjectID != storetest.SeedProjectID {
		t.Fatalf("opened: %+v", row)
	}
	h.wantDue(row)
	settled := h.settle(nil)

	// A label is never input: nothing is woken.
	h.now = h.now.Add(time.Minute)
	h.apply(recorded(t, "pull_request", "pull_request.labeled.json", nil))
	if row := h.row(); row.Version != settled.Version || row.NextActionAt != nil {
		t.Fatalf("a label woke the PR: %+v", row)
	}

	// An inline review comment wakes it.
	h.apply(recorded(t, "pull_request_review_comment", "pull_request_review_comment.created.json", nil))
	h.wantDue(h.row())
	h.settle(func(r *core.PRPipeline) { r.State, r.ReviewRound = core.PRPipelineReviewing, 1 })

	// A push starts it over on the new head.
	h.now = h.now.Add(time.Minute)
	sync := recorded(t, "pull_request", "pull_request.synchronize.json", nil)
	h.apply(sync)
	row = h.row()
	if row.StateHeadSHA != recordedPushedHead || row.State != core.PRPipelineAwaitingReview || row.ReviewRound != 0 {
		t.Fatalf("new head: %+v", row)
	}
	h.wantDue(row)
	h.settle(func(r *core.PRPipeline) { r.State, r.ReviewRound = core.PRPipelineReviewing, 1 })

	// The same push redelivered only wakes it: the head is the row's.
	h.now = h.now.Add(time.Minute)
	h.apply(sync)
	row = h.row()
	if row.StateHeadSHA != recordedPushedHead || row.State != core.PRPipelineReviewing || row.ReviewRound != 1 {
		t.Fatalf("a redelivered push started the PR over: %+v", row)
	}
	h.wantDue(row)
	h.settle(nil)

	// A conversation comment on the PR wakes it.
	h.now = h.now.Add(time.Minute)
	h.apply(recorded(t, "issue_comment", "issue_comment.created.json", func(doc map[string]any) {
		issue := doc["issue"].(map[string]any)
		issue["number"], issue["pull_request"] = 7, map[string]any{"url": "https://api.github.com/repos/acme/app/pulls/7"}
	}))
	h.wantDue(h.row())

	// Closed (unmerged, so it can be reopened): the row closes, due so its
	// run winds down.
	h.now = h.now.Add(time.Minute)
	h.apply(recorded(t, "pull_request", "pull_request.closed.json", func(doc map[string]any) {
		pr := doc["pull_request"].(map[string]any)
		pr["merged"], pr["merged_at"], pr["merge_commit_sha"] = false, nil, nil
	}))
	row = h.row()
	if row.State != core.PRPipelineClosed {
		t.Fatalf("closed: %+v", row)
	}
	h.wantDue(row)
	settled = h.settle(nil)

	// A comment on a closed PR wakes nothing.
	h.apply(recorded(t, "pull_request_review_comment", "pull_request_review_comment.created.json", nil))
	if row := h.row(); row.Version != settled.Version {
		t.Fatalf("a closed PR was woken: %+v", row)
	}

	// Reopened: it starts over on its head.
	h.now = h.now.Add(time.Minute)
	h.apply(recorded(t, "pull_request", "pull_request.reopened.json", nil))
	row = h.row()
	if row.State != core.PRPipelineAwaitingReview || row.StateHeadSHA != h.record().HeadSHA {
		t.Fatalf("reopened: %+v", row)
	}
	h.wantDue(row)
}

func TestPipelineProjectionClosesAMergedPR(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	h.enrol(core.PipelineRepoModeShadow)
	h.apply(recorded(t, "pull_request", "pull_request.opened.json", nil))
	h.settle(func(r *core.PRPipeline) { r.State = core.PRPipelineQueued })
	h.apply(recorded(t, "pull_request", "pull_request.closed.json", nil))
	if rec := h.record(); rec.State != core.PRRecordMerged {
		t.Fatalf("record: %+v", rec)
	}
	row := h.row()
	if row.State != core.PRPipelineClosed {
		t.Fatalf("merged outside the queue: %+v", row)
	}
	h.wantDue(row)
}

func TestPipelineProjectionDraftPRWaitsAsADraft(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	h.enrol(core.PipelineRepoModeOn)
	draft := func(doc map[string]any) { doc["pull_request"].(map[string]any)["draft"] = true }
	h.apply(recorded(t, "pull_request", "pull_request.opened.json", draft))
	if row := h.row(); row.State != core.PRPipelineDraft {
		t.Fatalf("a draft PR: %+v", row)
	}
	h.settle(nil)
	// ready_for_review carries a full snapshot a second newer.
	h.apply(recorded(t, "pull_request", "pull_request.ready_for_review.json", func(doc map[string]any) {
		doc["pull_request"].(map[string]any)["updated_at"] = "2026-10-05T11:00:01Z"
	}))
	row := h.row()
	if row.State != core.PRPipelineAwaitingReview {
		t.Fatalf("ready for review: %+v", row)
	}
	h.wantDue(row)
}

func TestPipelineProjectionLeavesOtherRepositoriesAlone(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]core.PipelineRepoMode{"not enrolled": "", "mode off": core.PipelineRepoModeOff} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newPipelineHarness(t)
			if mode != "" {
				h.enrol(mode)
			}
			h.apply(recorded(t, "pull_request", "pull_request.opened.json", nil))
			h.apply(recorded(t, "push", "push.json", nil))
			// Today's behaviour: the repository is in no Scope, so not
			// even a record is kept, and nothing is read from GitHub.
			if rec := h.record(); rec != nil {
				t.Fatalf("a record was kept: %+v", rec)
			}
			if len(h.contents.reqs) != 0 {
				t.Fatalf("config was read: %+v", h.contents.reqs)
			}
		})
	}
}

func TestPipelineProjectionRecordsTheBaseBranchConfig(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	h.enrol(core.PipelineRepoModeShadow)
	ctx := context.Background()
	get := func() *core.PipelineRepo {
		r, err := h.env.Repo.GetPipelineRepoByRepository(ctx, ProviderGitHub, recordedRepositoryID)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	changes := func() []map[string]any {
		rows, err := h.env.SQL.Query(`SELECT setting, coalesce(old_value, ''), new_value, changed_via, config_sha,
			changed_by_provider_user_id, changed_by_login FROM pipeline_setting_changes ORDER BY setting`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var setting, old, nw, via, sha, uid, login string
			if err := rows.Scan(&setting, &old, &nw, &via, &sha, &uid, &login); err != nil {
				t.Fatal(err)
			}
			out = append(out, map[string]any{"setting": setting, "old": old, "new": nw, "via": via, "sha": sha, "uid": uid, "login": login})
		}
		return out
	}
	push := func(after string, touched bool) []Event {
		return recorded(t, "push", "push.json", func(doc map[string]any) {
			doc["after"] = after
			if touched {
				commits := doc["commits"].([]any)
				c := commits[len(commits)-1].(map[string]any)
				c["modified"] = []any{config.FilePath}
			}
		})
	}

	// The first config: valid, sbom_scan off, recorded as a setting change
	// by the pusher (no merged PR is recorded for the commit).
	h.contents.files[recordedPushSHA] = "version: 1\ngate: {run: task test, stages: {sbom_scan: off}}\n"
	h.apply(push(recordedPushSHA, false))
	r := get()
	if r.ConfigSHA != recordedPushSHA || r.ConfigError != "" {
		t.Fatalf("first config: %+v", r)
	}
	if req := h.contents.reqs[0]; req.Path != config.FilePath || req.Ref != recordedPushSHA || req.Owner != "acme" ||
		req.Repo != "app" || req.UserID != storetest.SeedUserID || req.OrgID != "org-1" {
		t.Fatalf("read %+v", req)
	}
	stored, err := config.Load(r.ConfigJSON)
	if err != nil || stored.Gate.Stage(config.StageSBOMScan).State != config.StageOff || stored.Gate.Run != "task test" {
		t.Fatalf("config_json %s does not read back: %+v %v", r.ConfigJSON, stored.Gate, err)
	}
	got := changes()
	if len(got) != 1 || got[0]["setting"] != "gate.stage.sbom_scan" || got[0]["old"] != "on" || got[0]["new"] != "off" ||
		got[0]["via"] != "config" || got[0]["sha"] != recordedPushSHA || got[0]["uid"] != "1" || got[0]["login"] != "octocat" {
		t.Fatalf("setting changes: %+v", got)
	}

	// A push that cannot have changed the file reads nothing.
	other := "eeee000000000000000000000000000000000003"
	h.apply(push(other, false))
	if len(h.contents.reqs) != 1 || get().ConfigSHA != recordedPushSHA {
		t.Fatalf("an untouched push was read: %d reads, %+v", len(h.contents.reqs), get())
	}

	// An invalid file keeps the config in force and records why.
	bad := "eeee000000000000000000000000000000000004"
	h.contents.files[bad] = "version: 1\ngate: {stages: {lint: maybe}}\n"
	h.apply(push(bad, true))
	r = get()
	if r.ConfigSHA != recordedPushSHA || r.ConfigErrorSHA != bad || r.ConfigError == "" {
		t.Fatalf("invalid config: %+v", r)
	}

	// A missing file likewise.
	gone := "eeee000000000000000000000000000000000005"
	h.apply(push(gone, true))
	if r = get(); r.ConfigSHA != recordedPushSHA || r.ConfigErrorSHA != gone {
		t.Fatalf("missing config: %+v", r)
	}

	// A valid file turning sbom_scan back on clears the error and records
	// the change; one changing only a command records none.
	fixed := "eeee000000000000000000000000000000000006"
	h.contents.files[fixed] = "version: 1\ngate: {run: task gate}\n"
	h.apply(push(fixed, true))
	if r = get(); r.ConfigSHA != fixed || r.ConfigError != "" || r.ConfigErrorSHA != "" {
		t.Fatalf("fixed config: %+v", r)
	}
	if got := changes(); len(got) != 2 {
		t.Fatalf("setting changes: %+v", got)
	}

	// A push to another branch is not config.
	h.apply(recorded(t, "push", "push.json", func(doc map[string]any) {
		doc["ref"], doc["after"] = "refs/heads/feature", "eeee000000000000000000000000000000000007"
	}))
	if r = get(); r.ConfigSHA != fixed {
		t.Fatalf("a feature branch push moved the config: %+v", r)
	}
}

func TestPipelineProjectionWithoutAnAppRecordsWhy(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	h.p.contents = nil
	h.enrol(core.PipelineRepoModeShadow)
	h.apply(recorded(t, "push", "push.json", nil))
	r, err := h.env.Repo.GetPipelineRepoByRepository(context.Background(), ProviderGitHub, recordedRepositoryID)
	if err != nil || r.ConfigSHA != "" || r.ConfigErrorSHA != recordedPushSHA || r.ConfigError == "" {
		t.Fatalf("%+v %v", r, err)
	}
	// A read that fails for another reason is recorded too.
	h.p.contents = &fakeContents{err: errors.New("rate limited")}
	h.apply(recorded(t, "push", "push.json", func(doc map[string]any) { doc["after"] = "eeee000000000000000000000000000000000009" }))
	if r, _ = h.env.Repo.GetPipelineRepoByRepository(context.Background(), ProviderGitHub, recordedRepositoryID); r.ConfigErrorSHA != "eeee000000000000000000000000000000000009" {
		t.Fatalf("%+v", r)
	}
}

func TestConfigJSONReadsBack(t *testing.T) {
	doc := []byte("version: 1\ngate:\n  run: task test\n  timeout: 30m\n  stages:\n    lint: off\nqueue: {batch_max: 2}\n")
	raw, err := configJSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.Load(doc)
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Load(raw)
	if err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	if got.Gate.Timeout != want.Gate.Timeout || got.Queue.BatchMax != 2 || len(config.StageChanges(want, got)) != 0 {
		t.Fatalf("round trip: %+v vs %+v", got, want)
	}
}

func TestMayTouch(t *testing.T) {
	commit := func(files ...any) any { return map[string]any{"added": []any{}, "removed": []any{}, "modified": files} }
	if mayTouch(map[string]any{"commits": []any{commit("a.go")}}, config.FilePath) {
		t.Fatal("a listed push that names other files cannot touch it")
	}
	if !mayTouch(map[string]any{"commits": []any{commit("a.go"), commit(config.FilePath)}}, config.FilePath) {
		t.Fatal("a commit naming the file touches it")
	}
	if !mayTouch(map[string]any{"commits": []any{commit("a.go")}, "truncated": true}, config.FilePath) {
		t.Fatal("a truncated push may touch it")
	}
	if !mayTouch(map[string]any{}, config.FilePath) {
		t.Fatal("a push listing no commits may touch it")
	}
	many := make([]any, pushCommitsListed)
	for i := range many {
		many[i] = commit("a.go")
	}
	if !mayTouch(map[string]any{"commits": many}, config.FilePath) {
		t.Fatal("a push listing the most commits GitHub lists may have more")
	}
}

func TestPipelineProjectionAttributesAConfigChangeToTheMergedPR(t *testing.T) {
	t.Parallel()
	h := newPipelineHarness(t)
	h.enrol(core.PipelineRepoModeShadow)
	author := func(doc map[string]any) {
		doc["pull_request"].(map[string]any)["user"] = map[string]any{"login": "alice", "id": 42, "type": "User"}
	}
	h.apply(recorded(t, "pull_request", "pull_request.opened.json", author))
	h.apply(recorded(t, "pull_request", "pull_request.closed.json", author))
	merge := "dddddddddddddddddddddddddddddddddddddddd"
	h.contents.files[merge] = "version: 1\ngate: {run: task test, stages: {lint: off}}\n"
	h.apply(recorded(t, "push", "push.json", func(doc map[string]any) { doc["after"] = merge }))

	var uid, login, recordID string
	if err := h.env.SQL.QueryRow(`SELECT changed_by_provider_user_id, changed_by_login, config_record_id
		FROM pipeline_setting_changes WHERE setting = 'gate.stage.lint'`).Scan(&uid, &login, &recordID); err != nil {
		t.Fatal(err)
	}
	if uid != "42" || login != "alice" || recordID != h.record().ID {
		t.Fatalf("the change is the merged PR author's: %s %s %s", uid, login, recordID)
	}
}

// Copyright (c) 2025 Reliant Labs

package github_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/catalog"
)

// Check runs: how an automation acting as the Reliant App reports a check
// (the PR pipeline's review, merge gate and fix) on one commit, so a ruleset can
// require it. Same harness as github_test.go.

const headSHA = "6dcb09b5b57875f334f61aebed695e2e4193db5e"

const checkRunReply = `{"id": 4242, "node_id": "CR_1", "html_url": "https://github.com/acme/app/runs/4242",
  "name": "Reliant review", "head_sha": "` + headSHA + `", "status": "in_progress", "conclusion": null,
  "details_url": "https://app.reliantlabs.io/workflows/runs/r1", "external_id": "r1",
  "started_at": "2026-10-30T21:00:00Z", "completed_at": null,
  "output": {"title": "Reviewing", "summary": "x"}, "app": {"id": 1041943, "slug": "reliant-labs"}}`

// What the pipeline reports is the pipeline's record: the writes are workflow
// steps, never a tool an agent could call to mark its own head as reviewed.
func TestCheckRunWritesAreWorkflowOnly(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("github/check_run.create@1")
	require.NoError(t, err)
	got := map[string][2]bool{}
	for _, a := range resolved.Manifest.GetActions() {
		switch a.GetId() {
		case "check_run.create", "check_run.update", "check_run.list":
			got[a.GetId()] = [2]bool{a.GetMutates(), a.GetTool().GetExpose()}
		}
	}
	assert.Equal(t, map[string][2]bool{
		"check_run.create": {true, false},
		"check_run.update": {true, false},
		"check_run.list":   {false, true},
	}, got, "[mutates, exposed as a tool]")
}

func TestCheckRunCreateSendsWhatWasGiven(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/check-runs", jsonReply(201, checkRunReply))
	out := data(t, f.run("check_run.create", map[string]any{
		"owner": "acme", "repo": "app", "name": "Reliant review", "head_sha": headSHA,
		"status": "in_progress", "details_url": "https://app.reliantlabs.io/workflows/runs/r1", "external_id": "r1",
		"output": map[string]any{"title": "Reviewing", "summary": "Tier HIGH: two passes."},
	}))

	req := f.only()
	assert.Equal(t, "POST", req.Method)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"name": "Reliant review", "head_sha": headSHA, "status": "in_progress",
		"details_url": "https://app.reliantlabs.io/workflows/runs/r1", "external_id": "r1",
		"output": map[string]any{"title": "Reviewing", "summary": "Tier HIGH: two passes."},
	}, req.Body, "only what was given: no conclusion, no timestamps")
	assert.Equal(t, 4242.0, out["id"])
	assert.Equal(t, "CR_1", out["node_id"])
	assert.Equal(t, "https://github.com/acme/app/runs/4242", out["html_url"])
	assert.Equal(t, "in_progress", out["status"])
	assert.Nil(t, out["conclusion"])
	assert.Equal(t, headSHA, out["head_sha"])
}

func TestCheckRunCreateMinimal(t *testing.T) {
	f := newFake(t)
	f.on("POST", "/repos/acme/app/check-runs", jsonReply(201, checkRunReply))
	f.run("check_run.create", map[string]any{"owner": "acme", "repo": "app", "name": "Reliant review", "head_sha": headSHA})
	assert.Equal(t, map[string]any{"name": "Reliant review", "head_sha": headSHA}, f.only().Body,
		"GitHub defaults status to queued")
}

// A check is about one exact commit; a short or moved ref would let a required
// check pass for a commit nobody reviewed.
func TestCheckRunCreateRefusesBadInput(t *testing.T) {
	f := newFake(t)
	base := func(over map[string]any) map[string]any {
		p := map[string]any{"owner": "acme", "repo": "app", "name": "Reliant review", "head_sha": headSHA}
		for k, v := range over {
			if v == nil {
				delete(p, k)
			} else {
				p[k] = v
			}
		}
		return p
	}
	for name, p := range map[string]map[string]any{
		"short sha":         base(map[string]any{"head_sha": "6dcb09b"}),
		"branch name":       base(map[string]any{"head_sha": "main"}),
		"no name":           base(map[string]any{"name": nil}),
		"empty name":        base(map[string]any{"name": ""}),
		"unknown status":    base(map[string]any{"status": "done"}),
		"unknown verdict":   base(map[string]any{"conclusion": "passed"}),
		"http details url":  base(map[string]any{"details_url": "http://example.com/x"}),
		"output no summary": base(map[string]any{"output": map[string]any{"title": "t"}}),
		"output extra key":  base(map[string]any{"output": map[string]any{"title": "t", "summary": "s", "annotations": []any{}}}),
		"bad timestamp":     base(map[string]any{"started_at": "yesterday"}),
	} {
		_, err := f.try("check_run.create", p)
		assert.Error(t, err, name)
	}
	assert.Empty(t, f.requests(), "nothing is sent for a refused call")
}

func TestCheckRunUpdateCompletesTheRun(t *testing.T) {
	f := newFake(t)
	f.on("PATCH", "/repos/acme/app/check-runs/4242", jsonReply(200, checkRunReply))
	out := data(t, f.run("check_run.update", map[string]any{
		"owner": "acme", "repo": "app", "check_run_id": 4242, "conclusion": "failure",
		"output": map[string]any{"title": "Changes requested: 2 blockers", "summary": "- a\n- b"},
	}))

	req := f.only()
	assert.Equal(t, "PATCH", req.Method)
	assertGitHubHeaders(t, req)
	assert.Equal(t, map[string]any{
		"conclusion": "failure",
		"output":     map[string]any{"title": "Changes requested: 2 blockers", "summary": "- a\n- b"},
	}, req.Body, "only what changes; the commit is never sent")
	assert.Equal(t, 4242.0, out["id"])
}

func TestCheckRunUpdateRefusesBadInput(t *testing.T) {
	f := newFake(t)
	for name, p := range map[string]map[string]any{
		"no id":    {"owner": "acme", "repo": "app", "status": "completed"},
		"zero id":  {"owner": "acme", "repo": "app", "check_run_id": 0},
		"head_sha": {"owner": "acme", "repo": "app", "check_run_id": 1, "head_sha": headSHA},
	} {
		_, err := f.try("check_run.update", p)
		assert.Error(t, err, name)
	}
	assert.Empty(t, f.requests())
}

func TestCheckRunListFiltersAndPaginates(t *testing.T) {
	f := newFake(t)
	path := "/repos/acme/app/commits/" + headSHA + "/check-runs"
	f.on("GET", path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"total_count": 2, "check_runs": [
			  {"id": 2, "name": "Reliant review", "head_sha": "` + headSHA + `", "status": "queued", "conclusion": null,
			   "app": {"id": 1041943, "slug": "reliant-labs"}, "output": {"title": null}}]}`))
			return
		}
		w.Header().Set("Link", `<`+f.srv.URL+path+`?per_page=100&page=2>; rel="next"`)
		_, _ = w.Write([]byte(`{"total_count": 2, "check_runs": [
		  {"id": 1, "name": "Reliant review", "head_sha": "` + headSHA + `", "status": "completed", "conclusion": "success",
		   "html_url": "h1", "details_url": "d1", "external_id": "r1", "started_at": "s1", "completed_at": "c1",
		   "app": {"id": 1041943, "slug": "reliant-labs"}, "output": {"title": "Passed (LOW)", "summary": "x"}}]}`))
	})
	out := data(t, f.run("check_run.list", map[string]any{
		"owner": "acme", "repo": "app", "head_sha": headSHA, "check_name": "Reliant review", "app_id": 1041943,
	}))

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, path, reqs[0].Path)
	assert.Equal(t, "app_id=1041943&check_name=Reliant+review&filter=latest&per_page=100", sortedQuery(reqs[0].Query))
	assertGitHubHeaders(t, reqs[0])
	items := out["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, map[string]any{
		"id": 1.0, "name": "Reliant review", "head_sha": headSHA, "status": "completed", "conclusion": "success",
		"app_slug": "reliant-labs", "app_id": 1041943.0, "html_url": "h1", "details_url": "d1", "external_id": "r1",
		"title": "Passed (LOW)", "started_at": "s1", "completed_at": "c1",
	}, items[0])
	assert.Nil(t, items[1].(map[string]any)["conclusion"], "a run still queued has no conclusion")
}

func TestCheckRunListSendsOnlyTheDefaultFilter(t *testing.T) {
	f := newFake(t)
	f.on("GET", "/repos/acme/app/commits/"+headSHA+"/check-runs", jsonReply(200, `{"total_count": 0, "check_runs": []}`))
	out := data(t, f.run("check_run.list", map[string]any{"owner": "acme", "repo": "app", "head_sha": headSHA}))
	assert.Equal(t, "filter=latest&per_page=100", sortedQuery(f.only().Query))
	assert.Empty(t, out["items"])

	_, err := f.try("check_run.list", map[string]any{"owner": "acme", "repo": "app", "head_sha": "main"})
	assert.Error(t, err, "a branch tip moves; a check is read for one exact commit")
}

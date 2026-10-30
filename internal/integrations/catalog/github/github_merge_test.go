// Copyright (c) 2025 Reliant Labs

package github_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	// pr.merge's executor, registered on import.
	_ "github.com/reliant-labs/reliant/internal/integrations/ghmerge"
)

// pr.merge (a go: executor): one GraphQL lookup of the pull request, then at
// most one mutation, both through the same declarative runner as every other
// GitHub action. GraphQL reference: docs.github.com/en/graphql.

const (
	reviewed = "6dcb09b5b57875f334f61aebed695e2e4193db5e"
	pushed   = "0123456789abcdef0123456789abcdef01234567"
)

func mergeAction(t *testing.T) *reliantv1.ActionSpec {
	t.Helper()
	resolved, err := catalog.MustBuiltin().Resolve("github/pr.merge@1")
	require.NoError(t, err)
	for _, a := range resolved.Manifest.GetActions() {
		if a.GetId() == "pr.merge" {
			return a
		}
	}
	t.Fatal("pr.merge is not in the manifest")
	return nil
}

// graphQL answers the lookup with pr and each mutation with mutate (by the
// mutation's name), recording the mutations sent.
type graphQL struct {
	pr       string
	mutate   map[string]string
	mutation []string
	vars     []map[string]any
}

// handler reads the request as the fake recorded it (the fake has already
// consumed the body).
func (g *graphQL) handler(t *testing.T, f *fakeGitHub) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		got := f.requests()
		rec := got[len(got)-1].Body
		var body struct {
			Query     string
			Variables map[string]any
		}
		body.Query, _ = rec["query"].(string)
		body.Variables, _ = rec["variables"].(map[string]any)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(strings.TrimSpace(body.Query), "query") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequest":` + g.pr + `}}}`))
			return
		}
		for name, reply := range g.mutate {
			if strings.Contains(body.Query, name+"(") {
				g.mutation = append(g.mutation, name)
				g.vars = append(g.vars, body.Variables)
				_, _ = w.Write([]byte(reply))
				return
			}
		}
		t.Fatalf("unexpected mutation: %s", body.Query)
	}
}

func mergePRJSON(head string, queueOn, inQueue, auto bool) string {
	am := "null"
	if auto {
		am = `{"enabledAt":"2026-10-30T10:00:00Z"}`
	}
	b, _ := json.Marshal(map[string]any{"id": "PR_kw1", "databaseId": 77, "number": 9, "url": "https://github.com/acme/app/pull/9",
		"state": "OPEN", "headRefOid": head, "isMergeQueueEnabled": queueOn, "isInMergeQueue": inQueue})
	return strings.TrimSuffix(string(b), "}") + `,"autoMergeRequest":` + am + `}`
}

func mergeParams(action, head string) map[string]any {
	p := map[string]any{"owner": "acme", "repo": "app", "pull_number": 9, "action": action}
	if head != "" {
		p["match_head_commit"] = head
	}
	return p
}

func TestPRMergeIsAWorkflowActionOnly(t *testing.T) {
	a := mergeAction(t)
	assert.False(t, a.GetTool().GetExpose(), "no agent may merge")
	assert.True(t, a.GetMutates())
}

func TestPRMergeEnableAutoAtTheReviewedHead(t *testing.T) {
	f := newFake(t)
	g := &graphQL{pr: mergePRJSON(reviewed, true, false, false), mutate: map[string]string{
		"enablePullRequestAutoMerge": `{"data":{"enablePullRequestAutoMerge":{"clientMutationId":null}}}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	out := data(t, f.run("pr.merge", mergeParams("enable_auto", reviewed)))
	assert.Equal(t, []string{"enablePullRequestAutoMerge"}, g.mutation)
	assert.Equal(t, map[string]any{"id": "PR_kw1", "m": "SQUASH", "h": reviewed}, g.vars[0])
	assert.Equal(t, "auto_merge_enabled", out["state"])
	assert.Equal(t, true, out["changed"])
	assert.Equal(t, "PR_kw1", out["node_id"])
	assertGitHubHeaders(t, f.requests()[0])
}

// Arming a pull request GitHub already finds mergeable is refused ("clean
// status"); like gh pr merge --auto it is then queued (a queue-protected
// base) or merged, still at the reviewed head.
func TestPRMergeEnableAutoOnAMergeablePRQueuesIt(t *testing.T) {
	f := newFake(t)
	g := &graphQL{pr: mergePRJSON(reviewed, true, false, false), mutate: map[string]string{
		"enablePullRequestAutoMerge": `{"data":null,"errors":[{"type":"UNPROCESSABLE","message":"Pull request Pull request is in clean status"}]}`,
		"enqueuePullRequest":         `{"data":{"enqueuePullRequest":{"clientMutationId":null}}}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	out := data(t, f.run("pr.merge", mergeParams("enable_auto", reviewed)))
	assert.Equal(t, []string{"enablePullRequestAutoMerge", "enqueuePullRequest"}, g.mutation)
	assert.Equal(t, reviewed, g.vars[1]["h"])
	assert.Equal(t, "queued", out["state"])
}

// The head moved after the review: nothing is armed or merged.
func TestPRMergeRefusesAMovedHead(t *testing.T) {
	for _, action := range []string{"enable_auto", "merge"} {
		f := newFake(t)
		g := &graphQL{pr: mergePRJSON(pushed, false, false, false), mutate: map[string]string{}}
		f.on("POST", "/graphql", g.handler(t, f))
		res := f.run("pr.merge", mergeParams(action, reviewed))
		assert.True(t, res.IsError, action)
		assert.False(t, res.Retryable, action)
		assert.Contains(t, res.Content, "moved after the review")
		assert.Empty(t, g.mutation, "%s must send no mutation", action)
	}
}

func TestPRMergeRequiresTheReviewedHead(t *testing.T) {
	for _, action := range []string{"enable_auto", "merge"} {
		f := newFake(t)
		_, err := f.try("pr.merge", mergeParams(action, ""))
		require.Error(t, err, action)
		assert.Contains(t, err.Error(), "match_head_commit")
		assert.Empty(t, f.requests(), "%s without a head sends nothing", action)
	}
}

func TestPRMergeMergesOrQueues(t *testing.T) {
	f := newFake(t)
	g := &graphQL{pr: mergePRJSON(reviewed, false, false, false), mutate: map[string]string{
		"mergePullRequest": `{"data":{"mergePullRequest":{"clientMutationId":null}}}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	out := data(t, f.run("pr.merge", mergeParams("merge", reviewed)))
	assert.Equal(t, []string{"mergePullRequest"}, g.mutation)
	assert.Equal(t, "merged", out["state"])

	f = newFake(t)
	g = &graphQL{pr: mergePRJSON(reviewed, true, false, false), mutate: map[string]string{
		"enqueuePullRequest": `{"data":{"enqueuePullRequest":{"clientMutationId":null}}}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	out = data(t, f.run("pr.merge", mergeParams("merge", reviewed)))
	assert.Equal(t, []string{"enqueuePullRequest"}, g.mutation, "a queue-protected base is merged through the queue")
	assert.Equal(t, "queued", out["state"])
}

func TestPRMergeDisarms(t *testing.T) {
	f := newFake(t)
	g := &graphQL{pr: mergePRJSON(pushed, true, true, true), mutate: map[string]string{
		"disablePullRequestAutoMerge": `{"data":{"disablePullRequestAutoMerge":{"clientMutationId":null}}}`,
		"dequeuePullRequest":          `{"data":{"dequeuePullRequest":{"clientMutationId":null}}}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	assert.Equal(t, "auto_merge_disabled", data(t, f.run("pr.merge", mergeParams("disable_auto", "")))["state"])
	assert.Equal(t, "dequeued", data(t, f.run("pr.merge", mergeParams("dequeue", "")))["state"])
	assert.Equal(t, []string{"disablePullRequestAutoMerge", "dequeuePullRequest"}, g.mutation)
	assert.Equal(t, map[string]any{"id": "PR_kw1"}, g.vars[1])

	// Nothing armed or queued: success, nothing sent.
	f = newFake(t)
	g = &graphQL{pr: mergePRJSON(pushed, true, false, false), mutate: map[string]string{}}
	f.on("POST", "/graphql", g.handler(t, f))
	for _, action := range []string{"disable_auto", "dequeue"} {
		out := data(t, f.run("pr.merge", mergeParams(action, "")))
		assert.Equal(t, "unchanged", out["state"])
		assert.Equal(t, false, out["changed"])
	}
	assert.Empty(t, g.mutation)
}

func TestPRMergeGraphQLErrorIsAnErrorResult(t *testing.T) {
	f := newFake(t)
	g := &graphQL{pr: mergePRJSON(reviewed, false, false, false), mutate: map[string]string{
		"mergePullRequest": `{"data":null,"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`}}
	f.on("POST", "/graphql", g.handler(t, f))
	res := f.run("pr.merge", mergeParams("merge", reviewed))
	assert.True(t, res.IsError)
	assert.Equal(t, 403, res.StatusCode)
	assert.Contains(t, res.Content, "Resource not accessible by integration")
}

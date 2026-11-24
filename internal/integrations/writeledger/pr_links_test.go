// Copyright (c) 2025 Reliant Labs
package writeledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

type linkCall struct {
	chatID, runID string
	pr            core.PRRef
}

// TestAPullRequestARunOpensIsLinkedToItsChat: a GitHub pr.create that
// succeeds links the chat it was made for to the new pull request
// (chat_pr_links), with the run whose step made it; nothing else links.
func TestAPullRequestARunOpensIsLinkedToItsChat(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	chatID, _ := seedRun(t, repo, "autopilot")
	const runID = "wf-row-1"
	workflows := &writeledger.MockWorkflowLoader{LoadWorkflowFunc: func(_ context.Context, id string) (*core.Workflow, error) {
		if id == runID {
			return &core.Workflow{ID: runID, ChatID: chatID}, nil
		}
		return nil, errors.New("no such run")
	}}
	created := &httpaction.Result{StatusCode: 201, Data: map[string]any{"number": int64(91), "html_url": "https://github.com/acme/app/pull/91"}}
	prParams := map[string]any{"owner": "Acme", "repo": "App", "head": "feature/x", "base": "main", "title": "t"}
	pr91 := core.PRRef{Provider: "github", Repository: "acme/app", Number: 91}

	cases := []struct {
		name   string
		action string
		params map[string]any
		site   httpaction.CallSite
		result *httpaction.Result
		want   []linkCall
	}{
		{"a workflow step's pull request names its run", "pr.create", prParams, httpaction.CallSite{RunID: runID}, created,
			[]linkCall{{chatID, runID, pr91}}},
		{"the agent's tool names the chat and no run", "pr.create", prParams, httpaction.CallSite{RunID: chatID}, created,
			[]linkCall{{chatID, "", pr91}}},
		{"a refused pull request links nothing", "pr.create", prParams, httpaction.CallSite{RunID: runID},
			&httpaction.Result{StatusCode: 422, IsError: true, Content: "a pull request already exists"}, nil},
		{"another write links nothing", "issue.create", map[string]any{"owner": "acme", "repo": "app", "title": "t"},
			httpaction.CallSite{RunID: runID}, created, nil},
		{"a call outside any chat links nothing", "pr.create", prParams, httpaction.CallSite{}, created, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []linkCall
			links := &writeledger.MockPRLinker{LinkRunPRFunc: func(_ context.Context, chatID, runID string, pr core.PRRef) (int, error) {
				got = append(got, linkCall{chatID, runID, pr})
				return 1, nil
			}}
			rec := writeledger.New(writeledger.Deps{Store: ledgerOf(repo), Runs: repo, Workflows: workflows, PRLinks: links})
			res, err := rec.Wrap(&fakeRunner{result: tc.result}).RunAuthenticated(ctx, githubManifest(), action(tc.action, true), tc.params, nil, tc.site)
			require.NoError(t, err)
			assert.Same(t, tc.result, res)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestAFailedLinkLeavesThePullRequestsResultAlone: the pull request exists
// whatever the link does; a link that fails changes nothing the caller sees.
func TestAFailedLinkLeavesThePullRequestsResultAlone(t *testing.T) {
	repo := db.NewTestRepo(t)
	chatID, _ := seedRun(t, repo, "autopilot")
	links := &writeledger.MockPRLinker{LinkRunPRFunc: func(context.Context, string, string, core.PRRef) (int, error) {
		return 0, errors.New("database is down")
	}}
	created := &httpaction.Result{StatusCode: 201, Data: map[string]any{"number": int64(5)}}
	rec := writeledger.New(writeledger.Deps{Store: ledgerOf(repo), Runs: repo, Workflows: workflowsOf(repo), PRLinks: links})
	res, err := rec.Wrap(&fakeRunner{result: created}).RunAuthenticated(context.Background(), githubManifest(), action("pr.create", true),
		map[string]any{"owner": "acme", "repo": "app", "head": "x", "base": "main", "title": "t"}, nil, httpaction.CallSite{RunID: chatID})
	require.NoError(t, err)
	assert.Same(t, created, res)
	assert.Equal(t, 1, links.CallCount("LinkRunPR"))
}

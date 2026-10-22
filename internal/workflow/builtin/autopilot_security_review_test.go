// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
)

// autopilot-security-review: the trigger rules, the read-only toolset, and the
// one comment it posts. The graph's paths are its scenarios
// (autopilot-security-review/scenarios); these pin what a scenario cannot
// assert: which events start a run, what the reviewer may touch, and the body.

func securityPR(event, repo string, draft bool, labels ...string) map[string]any {
	p := prPayload(repo, draft, labels...)
	p["event"] = event
	return p
}

func TestSecurityReviewTriggers_RepoScopeAndDrafts(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-security-review")
	params := map[string]any{"repos": []any{"acme/app"}}
	at := func(repo string) map[string]string { return map[string]string{"repository": repo} }

	for _, ev := range []string{"pull_request.opened", "pull_request.reopened", "pull_request.ready_for_review", "pull_request.synchronize"} {
		assert.True(t, routed(t, wf, "pr-updated", params, ev, at("acme/app")), ev)
		assert.False(t, routed(t, wf, "pr-updated", params, ev, at("acme/other")), "%s in a repository outside repos is never reviewed", ev)
		assert.True(t, matched(t, fire(t, wf, "pr-updated", securityPR(ev, "acme/app", false), sender("1", true), params)), ev)
		assert.False(t, matched(t, fire(t, wf, "pr-updated", securityPR(ev, "acme/app", true), sender("1", true), params)), "%s on a draft", ev)
	}
	assert.False(t, routed(t, wf, "pr-updated", params, "pull_request.closed", at("acme/app")), "a closed PR is not reviewed")
	assert.False(t, routed(t, wf, "pr-updated", nil, "pull_request.opened", at("acme/app")), "no repos admits nothing")

	run := fire(t, wf, "pr-updated", securityPR("pull_request.synchronize", "acme/app", false), sender("1", true), params)
	require.Empty(t, run.Errors())
	assert.Equal(t, "acme/app", run.Inputs["repo"])
	assert.EqualValues(t, 7, run.Inputs["pr_number"])
	assert.Equal(t, "pull_request.synchronize", run.Inputs["reason"])
}

// With require_labels set, only PRs carrying one are reviewed, and an opened PR is reviewed through
// label-added alone: GitHub sends pull_request.labeled for the labels a PR is opened with, so the opened
// event must not also start a run.
func TestSecurityReviewTriggers_RequiredLabels(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-security-review")
	params := map[string]any{"repos": []any{"acme/app"}, "require_labels": []any{"security-review"}}
	fires := func(trigger string, p map[string]any) bool {
		return matched(t, fire(t, wf, trigger, p, sender("1", true), params))
	}

	assert.False(t, fires("pr-updated", securityPR("pull_request.opened", "acme/app", false, "security-review")),
		"opened with the label is reviewed by label-added, not twice")
	assert.True(t, fires("pr-updated", securityPR("pull_request.synchronize", "acme/app", false, "security-review")))
	assert.True(t, fires("pr-updated", securityPR("pull_request.ready_for_review", "acme/app", false, "security-review")))
	assert.False(t, fires("pr-updated", securityPR("pull_request.synchronize", "acme/app", false, "bug")))

	labelled := func(label string, draft bool) map[string]any {
		p := securityPR("pull_request.labeled", "acme/app", draft, label)
		p["data"].(map[string]any)["label"] = label
		return p
	}
	assert.True(t, fires("label-added", labelled("security-review", false)))
	assert.False(t, fires("label-added", labelled("bug", false)), "another label starts nothing")
	assert.False(t, fires("label-added", labelled("security-review", true)), "a labelled draft waits for ready_for_review")

	// Without require_labels the label trigger is inert and every non-draft PR is reviewed on open.
	plain := map[string]any{"repos": []any{"acme/app"}}
	assert.False(t, matched(t, fire(t, wf, "label-added", labelled("security-review", false), sender("1", true), plain)))
	assert.True(t, matched(t, fire(t, wf, "pr-updated", securityPR("pull_request.opened", "acme/app", false), sender("1", true), plain)))
}

func TestSecurityReviewTriggers_CommentTrust(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-security-review")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}
	ask := commentPayload("acme/app", "please /autopilot security-review")

	assert.True(t, matched(t, fire(t, wf, "review-comment", ask, sender("111", true), params)), "the owner")
	assert.True(t, matched(t, fire(t, wf, "review-comment", ask, sender("222", true), params)), "a trusted id")
	assert.False(t, matched(t, fire(t, wf, "review-comment", ask, sender("333", true), params)), "anyone else")
	assert.False(t, matched(t, fire(t, wf, "review-comment", ask, sender("111", false), params)), "an unverified sender")
	assert.False(t, matched(t, fire(t, wf, "review-comment", commentPayload("acme/app", "/autopilot re-review"), sender("111", true), params)),
		"pr-review's phrase is not this one")
	assert.False(t, routed(t, wf, "review-comment", params, "issue_comment.created", map[string]string{"repository": "acme/other", "is_pull_request": "true"}))
	assert.False(t, routed(t, wf, "review-comment", params, "issue_comment.created", map[string]string{"repository": "acme/app", "is_pull_request": "false"}),
		"a comment on an issue is not a PR")

	run := fire(t, wf, "review-comment", ask, sender("111", true), params)
	require.Empty(t, run.Errors())
	assert.Equal(t, "comment", run.Inputs["reason"], "the comment is an explicit ask (eligible reads this)")
}

// The reviewer is read-only by construction: it is offered exactly these tools, nothing is loadable
// (autopilot-agent), and its shell carries no credentials. The one write is the workflow's own `post`,
// whose event is the literal COMMENT.
func TestSecurityReviewerIsReadOnly(t *testing.T) {
	t.Parallel()
	doc := loadWorkflowYAML(t, "autopilot-security-review.yaml")
	review := nodeByID(t, doc, "review")
	args := mapAt(t, review, "args")

	var got []string
	for _, tool := range args["tools"].([]any) {
		got = append(got, tool.(string))
	}
	sort.Strings(got)
	assert.Equal(t, []string{
		"code_context", "github__code_search", "github__issue_get", "github__pr_get", "github__pr_list_files",
		"github__repo_get_content", "github__repo_get_tree", "shell", "shell_output", "shell_wait", "skill", "view",
	}, got)
	assert.Equal(t, "none", mapAt(t, mapAt(t, args, "tool_params"), "shell")["credentials"])
	assert.Equal(t, "none", mapAt(t, nodeByID(t, doc, "dir"), "args")["credentials"])

	prompt := args["system_prompt"].(string)
	for _, rule := range []string{"Never send a request to, scan, probe or fuzz any host", "Never deploy", "never touch production", "Never execute it"} {
		assert.Contains(t, prompt, rule)
	}

	post := nodeByID(t, doc, "post")
	assert.Equal(t, "github/pr.review.create@1", post["uses"])
	assert.Equal(t, "COMMENT", mapAt(t, post, "with")["event"], "never APPROVE, never REQUEST_CHANGES")

	wf := packWorkflow(t, "autopilot-security-review")
	for _, n := range wf.GetNodes() {
		switch n.GetId() {
		case "no_checkout", "no_review", "post_failed":
			assert.Equal(t, "failure", n.GetOutcome(), "%s posted no review: the run failed", n.GetId())
		}
	}
}

// The posted body, built by `render` and `compose` from the structured findings: grouped worst-first, each
// finding marked confirmed or suspected with its location, the scope statement, and the per-head marker
// that keeps an event from reviewing the same commit twice.
func TestSecurityReviewBody(t *testing.T) {
	t.Parallel()
	wf, err := parseBuiltin("autopilot-security-review.yaml")
	require.NoError(t, err)
	scenarios, err := loadScenariosForWorkflow(t, builtinLayout(t), "autopilot-security-review")
	require.NoError(t, err)

	body := func(name string) string {
		t.Helper()
		for _, sc := range scenarios {
			if sc.Name != name {
				continue
			}
			res := runner.NewRunner(wf).Run(sc)
			require.Equal(t, wfscenario.StatusPassed, res.Status, "%v", res.Mismatches)
			b, ok := res.Execution.NodeOutputs["compose"]["body"].(string)
			require.True(t, ok, "compose.body: %v", res.Execution.NodeOutputs["compose"])
			return b
		}
		require.FailNow(t, "no scenario "+name)
		return ""
	}

	b := body("findings_post_one_comment_grouped_by_severity")
	assert.True(t, strings.HasPrefix(b, "<!-- autopilot-security-review head=0123456789abcdef0123456789abcdef01234567 -->\n"), b)
	assert.Contains(t, b, "**2 finding(s):** 1 critical, 0 high, 1 medium, 0 low (1 confirmed).")
	crit, med := strings.Index(b, "### 🔴 Critical (1)"), strings.Index(b, "### 🟡 Medium (1)")
	require.True(t, crit > 0 && med > crit, "critical comes before medium:\n%s", b)
	assert.NotContains(t, b, "High (", "an empty severity has no section")
	assert.Contains(t, b, "**Confirmed · Importer fetches any URL server-side**")
	assert.Contains(t, b, "`internal/importer/handler.go:88` · ssrf")
	assert.Contains(t, b, "- **Exploit sketch:** POST /import")
	assert.Contains(t, b, "- **Fix:** Allow-list hosts")
	assert.Contains(t, b, "**Suspected · Archive entries joined without confinement**")
	assert.Contains(t, b, "Nothing was run against live systems")
	assert.Contains(t, b, "It is not a penetration test.")

	clean := body("no_findings_posts_a_clean_review")
	assert.Contains(t, clean, "**No security findings** in the changed code.")
	assert.NotContains(t, clean, "###")
	assert.Contains(t, clean, "Nothing was run against live systems")
}

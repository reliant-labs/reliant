// Copyright (c) 2025 Reliant Labs
package builtin_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// The Autopilot pack decides who may start a run and which repositories it
// touches in plain CEL over trigger.sender, trigger.activation and the event.
// These tests run each declared trigger through celeval.DryRunTrigger, the code
// behind `reliant workflow validate --trigger-payload ... --activation-params ...`
// and `reliant workflow eval`, so what they show is what a real firing does.

func packWorkflow(t *testing.T, name string) *reliantv1.Workflow {
	t.Helper()
	data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
	require.NoError(t, err)
	wf, err := wfyaml.ParseWorkflow(data)
	require.NoError(t, err)
	return wf
}

func triggerNamed(t *testing.T, wf *reliantv1.Workflow, name string) *reliantv1.WorkflowTrigger {
	t.Helper()
	for _, wt := range wf.GetTriggers() {
		if wt.GetName() == name {
			return wt
		}
	}
	require.FailNow(t, "no trigger named "+name)
	return nil
}

// fire runs one declared trigger against a sample event. The owner is GitHub
// user 111; params are the activation's.
func fire(t *testing.T, wf *reliantv1.Workflow, trigger string, payload, sender, params map[string]any) celeval.TriggerRun {
	t.Helper()
	return celeval.DryRunTrigger(triggerNamed(t, wf, trigger), wf.GetInputs(), payload, sender,
		&celeval.ActivationSample{Params: params, OwnerGitHubID: "111"})
}

func matched(t *testing.T, run celeval.TriggerRun) bool {
	t.Helper()
	require.Empty(t, run.Errors(), "the filter must evaluate cleanly: %v", run.Errors())
	require.NotNil(t, run.Matched)
	return *run.Matched
}

// activation builds trigger.activation the way intake does: the activation's own params over the workflow's
// declared defaults (runtime.ActivationParams), so a filter reads the same values it would for a real event.
func activation(wf *reliantv1.Workflow, wt *reliantv1.WorkflowTrigger, params map[string]any) map[string]any {
	return triggerspec.ActivationRoot("sample-activation", triggerspec.Owner{UserID: "u", GitHubID: "111"},
		v2.ActivationParams(wf.GetInputs(), wt.GetInputs(), params))
}

// routed reports whether an event reaches the activation at all: the pre-row
// gate intake applies before any filter runs, over the config an activation
// holding params stores (the declaration plus its typed scope resolved from
// params). The Autopilot pack scopes by repository here, with
// `match: {repository: {input: repos}}`, not in the filter.
func routed(t *testing.T, wf *reliantv1.Workflow, trigger string, params map[string]any, eventType string, attrs map[string]string) bool {
	t.Helper()
	name := trigger
	decl, err := triggers.DeclarationIn(wf, &core.Trigger{Workflow: wf.GetName(), WorkflowTrigger: &name, Params: params})
	require.NoError(t, err)
	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(decl.ProjectedConfig(params), &cfg))
	return triggers.IntegrationEventMatches(cfg, eventType, attrs)
}

func sender(id string, verified bool) map[string]any {
	return map[string]any{"kind": "github", "id": id, "display_name": "someone", "verified": verified}
}

func prPayload(repo string, draft bool, labels ...string) map[string]any {
	ls := make([]any, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, l)
	}
	return map[string]any{
		"event":      "pull_request.opened",
		"attributes": map[string]any{"repository": repo},
		"data": map[string]any{"pull_request": map[string]any{
			"number": 7, "draft": draft, "labels": ls, "state": "open",
		}},
	}
}

func TestPRReviewTriggers_RepoScopeIsActivationParams(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-pr-review")
	params := map[string]any{"repos": []any{"acme/app"}}
	opened := func(repo string) map[string]string { return map[string]string{"repository": repo} }

	assert.True(t, routed(t, wf, "pr-opened", params, "pull_request.opened", opened("acme/app")))
	assert.False(t, routed(t, wf, "pr-opened", params, "pull_request.opened", opened("acme/other")),
		"a repository outside this activation's repos is never reviewed, and no event row is written for it")
	assert.False(t, routed(t, wf, "pr-opened", params, "pull_request.opened", map[string]string{}),
		"an event that does not say which repository it is about never reaches a scoped activation")

	// Each activation is scoped by its own repos.
	both := map[string]any{"repos": []any{"acme/app", "acme/other"}}
	assert.True(t, routed(t, wf, "pr-opened", both, "pull_request.opened", opened("acme/other")))

	// No repos set: the scope is empty and admits nothing instead of everything. (repos is required, so
	// such an activation is NEEDS_SETUP; see TestAutopilotActivationsWithNoParamsNeedSetup.)
	assert.False(t, routed(t, wf, "pr-opened", nil, "pull_request.opened", opened("acme/app")))
	assert.False(t, routed(t, wf, "pr-opened", map[string]any{"repos": []any{}}, "pull_request.opened", opened("acme/app")))

	// The filter no longer carries the repository; the draft and label rules remain.
	run := fire(t, wf, "pr-opened", prPayload("acme/app", false), sender("1", true), params)
	assert.True(t, matched(t, run))
	assert.NotContains(t, triggerNamed(t, wf, "pr-opened").GetFilter(), "params.repos")
}

func TestPRReviewTriggers_DraftsAndSkipLabel(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-pr-review")
	params := map[string]any{"repos": []any{"acme/app"}}

	assert.False(t, matched(t, fire(t, wf, "pr-opened", prPayload("acme/app", true), sender("1", true), params)), "drafts are not reviewed")
	assert.False(t, matched(t, fire(t, wf, "pr-opened", prPayload("acme/app", false, "skip-review"), sender("1", true), params)),
		"the skip label opts a PR out")
	assert.True(t, matched(t, fire(t, wf, "pr-opened", prPayload("acme/app", false, "bug"), sender("1", true), params)))

	// The label is a param: a PR carrying the renamed label is skipped, and the old name means nothing.
	renamed := map[string]any{"repos": []any{"acme/app"}, "skip_label": "no-bot"}
	assert.False(t, matched(t, fire(t, wf, "pr-opened", prPayload("acme/app", false, "no-bot"), sender("1", true), renamed)))
	assert.True(t, matched(t, fire(t, wf, "pr-opened", prPayload("acme/app", false, "skip-review"), sender("1", true), renamed)))
}

func TestPRReviewTriggers_ReReviewLabelIsAParam(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-pr-review")
	labelled := func(label string) map[string]any {
		p := prPayload("acme/app", false)
		p["event"] = "pull_request.labeled"
		p["data"].(map[string]any)["label"] = label
		return p
	}
	params := map[string]any{"repos": []any{"acme/app"}}

	assert.True(t, matched(t, fire(t, wf, "re-review-label", labelled("agent:re-review"), sender("1", true), params)))
	assert.False(t, matched(t, fire(t, wf, "re-review-label", labelled("bug"), sender("1", true), params)), "only the re-review label asks again")
	renamed := map[string]any{"repos": []any{"acme/app"}, "re_review_label": "again"}
	assert.True(t, matched(t, fire(t, wf, "re-review-label", labelled("again"), sender("1", true), renamed)))
	assert.False(t, matched(t, fire(t, wf, "re-review-label", labelled("agent:re-review"), sender("1", true), renamed)))
}

func commentPayload(repo, body string) map[string]any {
	return map[string]any{
		"event":      "issue_comment.created",
		"attributes": map[string]any{"repository": repo},
		"data": map[string]any{
			"issue":   map[string]any{"number": 7, "labels": []any{"agent:authored"}, "state": "open"},
			"comment": map[string]any{"body": body},
		},
	}
}

// Trust is plain CEL over trigger.sender, trigger.activation.owner.github_id and
// trigger.activation.params.trusted_ids. There is no table behind it.
func TestPRReviewTriggers_ReReviewCommentTrust(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-pr-review")
	ask := commentPayload("acme/app", "please /autopilot re-review")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}

	assert.True(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("111", true), params)), "the owner is trusted without being listed")
	assert.True(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("222", true), params)), "a listed id is trusted")
	assert.False(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("333", true), params)), "anyone else is not")
	assert.False(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("111", false), params)), "an unverified sender is never trusted, even with the owner's id")
	assert.False(t, matched(t, fire(t, wf, "re-review-comment", commentPayload("acme/app", "looks good"), sender("111", true), params)),
		"only the exact phrase asks")
	assert.False(t, routed(t, wf, "re-review-comment", params, "issue_comment.created", map[string]string{"repository": "acme/other", "is_pull_request": "true"}),
		"and only in an enabled repository")
	assert.True(t, routed(t, wf, "re-review-comment", params, "issue_comment.created", map[string]string{"repository": "acme/app", "is_pull_request": "true"}))

	// With no trusted_ids set, only the owner can ask.
	solo := map[string]any{"repos": []any{"acme/app"}}
	assert.True(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("111", true), solo)))
	assert.False(t, matched(t, fire(t, wf, "re-review-comment", ask, sender("222", true), solo)))
}

func TestPRReviewTriggers_AnAnonymousSenderWithNoGitHubConnectionIsNeverTheOwner(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-pr-review")
	ask := commentPayload("acme/app", "/autopilot re-review")
	params := map[string]any{"repos": []any{"acme/app"}}

	// An activation without a GitHub connection has an empty owner id; an empty sender id must not equal it.
	run := celeval.DryRunTrigger(triggerNamed(t, wf, "re-review-comment"), wf.GetInputs(), ask, sender("", true),
		&celeval.ActivationSample{Params: params, OwnerGitHubID: ""})
	assert.False(t, matched(t, run), "'' == '' must not make a nameless sender the owner")
}

func labeledIssue(repo, label string) map[string]any {
	return map[string]any{
		"event":      "issues.labeled",
		"attributes": map[string]any{"repository": repo},
		"data": map[string]any{
			"label": label,
			"issue": map[string]any{"number": 7, "state": "open", "labels": []any{label}, "is_pull_request": false},
		},
	}
}

func TestIssueWorkerTriggers_OnlyATrustedVerifiedLabelOpensAnIssue(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-issue-worker")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}
	ev := labeledIssue("acme/app", "agent:ready")

	assert.True(t, matched(t, fire(t, wf, "ready-label", ev, sender("111", true), params)))
	assert.True(t, matched(t, fire(t, wf, "ready-label", ev, sender("222", true), params)))
	assert.False(t, matched(t, fire(t, wf, "ready-label", ev, sender("333", true), params)), "a repo admin who is not trusted starts nothing")
	assert.False(t, matched(t, fire(t, wf, "ready-label", ev, sender("111", false), params)), "verification is required, not just the id")
	assert.False(t, matched(t, fire(t, wf, "ready-label", labeledIssue("acme/app", "bug"), sender("111", true), params)), "another label opens nothing")
	assert.False(t, routed(t, wf, "ready-label", params, "issues.labeled", map[string]string{"repository": "acme/other"}), "only in an enabled repository")
	assert.True(t, routed(t, wf, "ready-label", params, "issues.labeled", map[string]string{"repository": "acme/app"}))

	renamed := map[string]any{"repos": []any{"acme/app"}, "ready_label": "ai:go"}
	assert.True(t, matched(t, fire(t, wf, "ready-label", labeledIssue("acme/app", "ai:go"), sender("111", true), renamed)))
	assert.False(t, matched(t, fire(t, wf, "ready-label", labeledIssue("acme/app", "agent:ready"), sender("111", true), renamed)))

	// The run's inputs come from the event, and the owner id travels with them for the in-run comment filter.
	run := fire(t, wf, "ready-label", ev, sender("111", true), params)
	require.Empty(t, run.Errors())
	assert.Equal(t, "acme/app", run.Inputs["repo"])
	assert.EqualValues(t, 7, run.Inputs["issue_number"])
	assert.Equal(t, "111", run.Inputs["owner_id"])
}

func TestIssueWorkerTriggers_PullRequestsAndClosedIssuesAreNotWorked(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-issue-worker")
	params := map[string]any{"repos": []any{"acme/app"}}

	pr := labeledIssue("acme/app", "agent:ready")
	pr["data"].(map[string]any)["issue"].(map[string]any)["is_pull_request"] = true
	assert.False(t, matched(t, fire(t, wf, "ready-label", pr, sender("111", true), params)))

	closed := labeledIssue("acme/app", "agent:ready")
	closed["data"].(map[string]any)["issue"].(map[string]any)["state"] = "closed"
	assert.False(t, matched(t, fire(t, wf, "ready-label", closed, sender("111", true), params)))
}

func TestIssueWorkerTriggers_MentionTrust(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-issue-worker")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}
	mention := map[string]any{
		"event":      "issue_comment.created",
		"attributes": map[string]any{"repository": "acme/app", "mentions_reliant": "true", "is_pull_request": "false"},
		"data": map[string]any{
			"issue":   map[string]any{"number": 7, "state": "open", "labels": []any{}},
			"comment": map[string]any{"body": "@reliant please look"},
		},
	}
	assert.True(t, matched(t, fire(t, wf, "mention", mention, sender("111", true), params)))
	assert.True(t, matched(t, fire(t, wf, "mention", mention, sender("222", true), params)))
	assert.False(t, matched(t, fire(t, wf, "mention", mention, sender("333", true), params)))
	assert.False(t, matched(t, fire(t, wf, "mention", mention, sender("111", false), params)))
}

func reviewPayload(repo, state, body string, labels ...string) map[string]any {
	ls := make([]any, 0, len(labels))
	for _, l := range labels {
		ls = append(ls, l)
	}
	return map[string]any{
		"event":      "pull_request_review.submitted",
		"attributes": map[string]any{"repository": repo, "state": state},
		"data": map[string]any{
			"pull_request": map[string]any{"number": 7, "labels": ls},
			"review":       map[string]any{"body": body},
		},
	}
}

func TestReviewResponderTriggers_FeedbackTrust(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-review-responder")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}

	changes := reviewPayload("acme/app", "changes_requested", "please fix", "agent:authored")
	assert.True(t, matched(t, fire(t, wf, "review-submitted", changes, sender("111", true), params)), "the owner's review is heard")
	assert.True(t, matched(t, fire(t, wf, "review-submitted", changes, sender("222", true), params)), "a listed id's review is heard")
	assert.False(t, matched(t, fire(t, wf, "review-submitted", changes, sender("333", true), params)), "a stranger's review is not")
	assert.False(t, matched(t, fire(t, wf, "review-submitted", changes, sender("111", false), params)))

	assert.False(t, matched(t, fire(t, wf, "review-submitted", reviewPayload("acme/app", "changes_requested", "x"), sender("111", true), params)),
		"a PR with neither opt-in label is left alone")
	assert.True(t, matched(t, fire(t, wf, "review-submitted", reviewPayload("acme/app", "commented", "x", "agent:respond"), sender("111", true), params)),
		"agent:respond opts any PR in")
	assert.False(t, matched(t, fire(t, wf, "review-submitted", reviewPayload("acme/app", "approved", "x", "agent:authored"), sender("111", true), params)),
		"an approval is not feedback")
	assert.False(t, routed(t, wf, "review-submitted", params, "pull_request_review.submitted", map[string]string{"repository": "acme/other"}), "only in an enabled repository")
	assert.True(t, routed(t, wf, "review-submitted", params, "pull_request_review.submitted", map[string]string{"repository": "acme/app"}))
}

func TestReviewResponderTriggers_ItsOwnWritesAreNotFeedbackButTheReviewerIs(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-review-responder")
	params := map[string]any{"repos": []any{"acme/app"}}
	wt := triggerNamed(t, wf, "review-submitted")

	// An event Reliant caused (here by the responder's own run) is never feedback, even from the owner's account.
	own := reviewPayload("acme/app", "commented", "replying", "agent:authored")
	filter, err := triggerspec.CompileFilter(wt.GetFilter())
	require.NoError(t, err)
	scopeFor := func(origin map[string]any, payload map[string]any, snd map[string]any) triggerspec.Scope {
		return triggerspec.Scope{
			Trigger: map[string]any{
				"payload": payload, "sender": snd, "origin": origin,
			},
			Activation: activation(wf, wt, params),
		}
	}
	none := map[string]any{"caused_by_reliant": false, "confidence": "none", "run_id": "", "workflow": "", "trigger_name": "", "same_workflow": false, "actor": "unknown", "lineage_depth": int64(0)}
	ours := map[string]any{"caused_by_reliant": true, "confidence": "exact", "run_id": "r", "workflow": "autopilot-review-responder", "trigger_name": "review-submitted", "same_workflow": true, "actor": "owner", "lineage_depth": int64(1)}
	reviewer := map[string]any{"caused_by_reliant": true, "confidence": "exact", "run_id": "r2", "workflow": "autopilot-pr-review", "trigger_name": "pr-opened", "same_workflow": false, "actor": "owner", "lineage_depth": int64(0)}
	reviewerRef := map[string]any{"caused_by_reliant": true, "confidence": "exact", "run_id": "r2", "workflow": "builtin://autopilot-pr-review", "trigger_name": "pr-opened", "same_workflow": false, "actor": "owner", "lineage_depth": int64(0)}
	someoneElse := map[string]any{"caused_by_reliant": true, "confidence": "exact", "run_id": "r3", "workflow": "my-other-bot", "trigger_name": "t", "same_workflow": false, "actor": "owner", "lineage_depth": int64(0)}

	hit, err := filter.MatchScope(scopeFor(none, own, sender("111", true)))
	require.NoError(t, err)
	assert.True(t, hit, "a human's review passes")

	hit, err = filter.MatchScope(scopeFor(ours, own, sender("111", true)))
	require.NoError(t, err)
	assert.False(t, hit, "a review the responder's own run wrote is not feedback")

	// Autopilot's pr-review is the one writer whose review IS feedback, when it has findings.
	findings := reviewPayload("acme/app", "commented", "### Defects\n- a\n<!-- autopilot:pr-review actionable=true -->", "agent:authored")
	hit, err = filter.MatchScope(scopeFor(reviewer, findings, sender("111", true)))
	require.NoError(t, err)
	assert.True(t, hit, "pr-review's review with findings wakes the responder")

	hit, err = filter.MatchScope(scopeFor(reviewerRef, findings, sender("111", true)))
	require.NoError(t, err)
	assert.True(t, hit, "an activation of the built-in is recorded as builtin://autopilot-pr-review; it is the same reviewer")

	hit, err = filter.MatchScope(scopeFor(someoneElse, findings, sender("111", true)))
	require.NoError(t, err)
	assert.False(t, hit, "another workflow's review is not pr-review's, whatever marker its body carries")

	clean := reviewPayload("acme/app", "commented", "No defects.\n<!-- autopilot:pr-review actionable=false -->", "agent:authored")
	hit, err = filter.MatchScope(scopeFor(reviewer, clean, sender("111", true)))
	require.NoError(t, err)
	assert.False(t, hit, "a clean review has nothing to respond to")

	// A person who copies the marker into their own review (origin none) is judged as a person, not as pr-review.
	forged := reviewPayload("acme/app", "commented", "x\n<!-- autopilot:pr-review actionable=true -->", "agent:authored")
	hit, err = filter.MatchScope(scopeFor(none, forged, sender("333", true)))
	require.NoError(t, err)
	assert.False(t, hit, "the marker alone proves nothing; the write ledger's origin does")
}

func TestReviewResponderTriggers_CommentFiltersDropReliantsOwnWrites(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-review-responder")
	params := map[string]any{"repos": []any{"acme/app"}, "trusted_ids": []any{"222"}}

	for _, name := range []string{"pr-comment", "review-comment"} {
		wt := triggerNamed(t, wf, name)
		filter, err := triggerspec.CompileFilter(wt.GetFilter())
		require.NoError(t, err, name)
		payload := commentPayload("acme/app", "rename this")
		if name == "review-comment" {
			payload = map[string]any{
				"event":      "pull_request_review_comment.created",
				"attributes": map[string]any{"repository": "acme/app"},
				"data": map[string]any{"pull_request": map[string]any{"number": 7, "labels": []any{"agent:authored"}},
					"comment": map[string]any{"body": "rename this"}},
			}
		}
		scope := func(caused bool, snd map[string]any) triggerspec.Scope {
			return triggerspec.Scope{
				Trigger: map[string]any{"payload": payload, "sender": snd, "origin": map[string]any{
					"caused_by_reliant": caused, "confidence": "none", "run_id": "", "workflow": "", "trigger_name": "", "same_workflow": false, "actor": "unknown", "lineage_depth": int64(0)}},
				Activation: activation(wf, wt, params),
			}
		}
		hit, err := filter.MatchScope(scope(false, sender("111", true)))
		require.NoError(t, err, name)
		assert.True(t, hit, name+": the owner's comment is feedback")
		hit, err = filter.MatchScope(scope(false, sender("222", true)))
		require.NoError(t, err, name)
		assert.True(t, hit, name+": a trusted id's comment is feedback")
		hit, err = filter.MatchScope(scope(false, sender("333", true)))
		require.NoError(t, err, name)
		assert.False(t, hit, name+": a stranger's is not")
		hit, err = filter.MatchScope(scope(true, sender("111", true)))
		require.NoError(t, err, name)
		assert.False(t, hit, name+": a comment Reliant wrote is not feedback, even from the owner's account")
	}
}

func TestAlertTriageTrigger_DedupeKeyAndFilter(t *testing.T) {
	t.Parallel()
	wf := packWorkflow(t, "autopilot-alert-triage")
	wt := triggerNamed(t, wf, "alertmanager")
	assert.Equal(t, "4h", wt.GetDedupeWindow())

	body := func(status string, fps ...string) map[string]any {
		alerts := make([]any, 0, len(fps))
		for _, fp := range fps {
			alerts = append(alerts, map[string]any{"status": status, "fingerprint": fp, "labels": map[string]any{"alertname": "A"}})
		}
		return map[string]any{"body": map[string]any{"version": "4", "groupKey": "{}:{alertname=\"A\"}", "status": status, "alerts": alerts}}
	}
	run := func(p map[string]any) celeval.TriggerRun {
		return celeval.DryRunTrigger(wt, wf.GetInputs(), p, nil, &celeval.ActivationSample{})
	}

	firing := run(body("firing", "bbb", "aaa"))
	require.Empty(t, firing.Errors())
	assert.True(t, *firing.Matched)
	// Fingerprints are sorted, so the order Alertmanager lists them in does not change the key.
	assert.Equal(t, run(body("firing", "aaa", "bbb")).DedupeKey, firing.DedupeKey)
	assert.Contains(t, firing.DedupeKey, ":firing:aaa,bbb")

	// A resolved notification for the same group is a different event; so is a group with a different member.
	assert.NotEqual(t, firing.DedupeKey, run(body("resolved", "aaa", "bbb")).DedupeKey)
	assert.NotEqual(t, firing.DedupeKey, run(body("firing", "aaa", "bbb", "ccc")).DedupeKey)

	// A body that is not an Alertmanager notification starts nothing.
	for _, p := range []map[string]any{
		{"body": map[string]any{"hello": "world"}},
		{"body": map[string]any{"alerts": []any{}}},
		{"body": "plain text"},
	} {
		r := run(p)
		if r.FilterError != "" {
			continue
		}
		require.NotNil(t, r.Matched)
		assert.False(t, *r.Matched, "%v", p)
	}

	// The inputs the run receives: the group key and at most 20 alerts.
	many := make([]string, 0, 25)
	for i := 0; i < 25; i++ {
		many = append(many, "fp"+string(rune('a'+i)))
	}
	big := run(body("firing", many...))
	require.Empty(t, big.Errors())
	assert.Len(t, big.Inputs["alerts"], 20)
}

// The default origin_filter ignores a workflow's own echoes, only when Reliant
// is confident they are its own. Every pack trigger that declares none gets it.
func TestPackTriggersUseTheDefaultOriginFilter(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"autopilot-pr-review", "autopilot-issue-worker", "autopilot-review-responder", "autopilot-alert-triage"} {
		wf := packWorkflow(t, name)
		for _, wt := range wf.GetTriggers() {
			assert.Empty(t, wt.GetOriginFilter(), "%s/%s declares no origin_filter, so the default applies", name, wt.GetName())
		}
	}

	of, err := triggerspec.CompileOriginFilter("")
	require.NoError(t, err)
	assert.Equal(t, triggerspec.DefaultOriginFilter, of.Expr())

	scope := func(same bool, confidence string) triggerspec.Scope {
		return triggerspec.Scope{Trigger: map[string]any{"origin": map[string]any{
			"caused_by_reliant": confidence != "none", "confidence": confidence, "run_id": "r", "workflow": "autopilot-pr-review",
			"trigger_name": "pr-opened", "same_workflow": same, "actor": "owner", "lineage_depth": int64(0)}}}
	}
	for _, tc := range []struct {
		name       string
		same       bool
		confidence string
		allowed    bool
	}{
		{"its own echo, exact", true, "exact", false},
		{"its own echo, intent", true, "intent", false},
		{"a push it can only claim", true, "claimed", true},
		{"reliant's app, run unknown", true, "app", true},
		{"another workflow's write", false, "exact", true},
		{"a person", false, "none", true},
	} {
		got, err := of.Allow(scope(tc.same, tc.confidence))
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.allowed, got, tc.name)
	}
}

// pr-review must stay machine-free: it reads through GitHub only.
func TestPRReviewNeedsNoMachineAndOthersDo(t *testing.T) {
	t.Parallel()
	assert.True(t, packNeedsMachine(t, "autopilot-pr-review") == false, "pr-review reads GitHub only")
	for _, name := range []string{"autopilot-issue-worker", "autopilot-review-responder", "autopilot-alert-triage"} {
		assert.True(t, packNeedsMachine(t, name), "%s works in a checkout", name)
	}
}

func packNeedsMachine(t *testing.T, name string) bool {
	t.Helper()
	wf := packWorkflow(t, name)
	loader := v2.WorkflowRefLoader(func(ref string) (*reliantv1.Workflow, error) { return builtinLoader(ref) })
	req := v2.MachineRequirements(wf, nil, loader, tools.PreflightConfig())
	t.Logf("%s needs a machine: %v (hard: %v, tools: %v)", name, !req.None(), req.Hard, req.Tools)
	return !req.None()
}

// A pack workflow whose agent never produced its required response must end
// FAILED, not completed: Node.outcome is the run's verdict, and the firing's
// run_status (trigger health, inbox) follows it. The scenario tests cannot see
// this — their `outcome:` is the Temporal lifecycle, which finishes either way.
func TestAutopilotNoResponseTerminalsDeclareFailure(t *testing.T) {
	wf := packWorkflow(t, "autopilot-pr-review")
	var found bool
	for _, n := range wf.GetNodes() {
		if n.GetId() != "no_review" {
			continue
		}
		found = true
		assert.Equal(t, "failure", n.GetOutcome(),
			"the reviewer finishing without a review is a failed run, not a completed one")
	}
	require.True(t, found, "autopilot-pr-review has a no_review terminal")
}

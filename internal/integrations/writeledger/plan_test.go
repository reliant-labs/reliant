// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

func TestPlanForEveryMutatingGitHubAction(t *testing.T) {
	base := map[string]any{"owner": "Acme", "repo": "App"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := []struct {
		action     string
		params     map[string]any
		target     string
		kind       string
		event      string
		objectFrom string
	}{
		{"issue.create", with(map[string]any{"title": "t", "body": "b"}), "github:acme/app", "issue", "issues.opened", "number"},
		{"issue.comment", with(map[string]any{"issue_number": float64(7), "body": "b"}), "github:acme/app#7", "comment", "issue_comment.created", "id"},
		{"issue.comment.update", with(map[string]any{"comment_id": float64(9), "body": "b"}), "github:acme/app", "comment", "issue_comment.edited", ""},
		{"issue.labels.add", with(map[string]any{"issue_number": float64(7), "label": "autopilot"}), "github:acme/app#7", "issue", "issues.labeled", ""},
		{"issue.labels.remove", with(map[string]any{"issue_number": float64(7), "label": "autopilot"}), "github:acme/app#7", "issue", "issues.unlabeled", ""},
		{"issue.update", with(map[string]any{"issue_number": float64(7), "state": "closed"}), "github:acme/app#7", "issue", "issues.closed", ""},
		{"pr.create", with(map[string]any{"head": "feature", "base": "main", "title": "t"}), "github:acme/app", "pull_request", "pull_request.opened", "number"},
		{"pr.update", with(map[string]any{"pull_number": float64(3), "state": "closed"}), "github:acme/app#3", "pull_request", "pull_request.closed", ""},
		{"pr.review.create", with(map[string]any{"pull_number": float64(3), "event": "APPROVE"}), "github:acme/app#3", "review", "pull_request_review.submitted", "id"},
		{"pr.review_comment.reply", with(map[string]any{"pull_number": float64(3), "comment_id": float64(1), "body": "b"}), "github:acme/app#3", "review_comment", "pull_request_review_comment.created", "id"},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			p := planFor("github", tc.action, tc.params)
			assert.Equal(t, "github:acme/app", p.scope)
			assert.Equal(t, tc.target, p.target)
			assert.Equal(t, tc.kind, p.correlation.ObjectKind)
			assert.Contains(t, p.correlation.Events, tc.event)
			assert.Equal(t, tc.objectFrom, p.objectFrom)
		})
	}
}

func TestPlanCarriesWhatAnEchoCanBeMatchedOn(t *testing.T) {
	p := planFor("github", "pr.review.create", map[string]any{"owner": "a", "repo": "b", "pull_number": float64(3), "event": "REQUEST_CHANGES", "body": "x", "commit_id": "abc1234"})
	assert.Equal(t, "changes_requested", p.correlation.ReviewState)
	assert.Equal(t, provenance.BodyDigest("x"), p.correlation.BodySHA256)
	assert.Equal(t, "abc1234", p.correlation.HeadSHA)

	p = planFor("github", "pr.review.create", map[string]any{"owner": "a", "repo": "b", "pull_number": float64(3)})
	assert.Equal(t, "commented", p.correlation.ReviewState, "COMMENT is the default verdict")
	assert.Empty(t, p.correlation.BodySHA256)

	p = planFor("github", "issue.create", map[string]any{"owner": "a", "repo": "b", "title": "t", "labels": []any{"bug", "p1"}, "assignees": []any{"octo"}})
	assert.Equal(t, []string{"bug", "p1"}, p.correlation.Labels)
	assert.Contains(t, p.correlation.Events, "issues.labeled")
	assert.Contains(t, p.correlation.Events, "issues.assigned")
}

func TestCompletionReadsTheNewObjectFromTheResponse(t *testing.T) {
	p := planFor("github", "pr.create", map[string]any{"owner": "a", "repo": "b", "head": "h"})
	target, id, url := p.completion(map[string]any{"number": int64(12), "html_url": "https://x/12"})
	assert.Equal(t, "github:a/b#12", target)
	assert.Equal(t, "12", id)
	assert.Equal(t, "https://x/12", url)

	p = planFor("github", "issue.comment", map[string]any{"owner": "a", "repo": "b", "issue_number": float64(5)})
	target, id, _ = p.completion(map[string]any{"id": int64(77)})
	assert.Empty(t, target, "a comment's target stays the issue it is on")
	assert.Equal(t, "77", id)
}

func TestOtherIntegrationsGetAScopeOnlyEntry(t *testing.T) {
	p := planFor("slack", "chat.post", map[string]any{"channel": "C1"})
	assert.Equal(t, "slack:", p.scope)
	assert.Empty(t, p.correlation.ObjectKind)
}

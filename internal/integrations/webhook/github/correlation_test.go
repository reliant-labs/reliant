// Copyright (c) 2025 Reliant Labs
package github

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

// The correlation keys are how an arriving event is attributed to a write
// Reliant made: the parser computes them from the RAW body and the ledger's
// planner computes the same ones from the write's params. The two must agree,
// so these tests pin the parser's half against the shared provenance package.

func echoOfFixture(t *testing.T, event, name string) *provenance.Echo {
	t.Helper()
	ev := one(t, parseFixture(t, event, name))
	require.NotNil(t, ev.Echo, "%s carries no echo", name)
	return ev.Echo
}

func TestIssueCommentEchoCarriesIDTargetAndBodyDigest(t *testing.T) {
	e := echoOfFixture(t, "issue_comment", "issue_comment.created")
	assert.Equal(t, "issue_comment.created", e.EventType)
	assert.Equal(t, provenance.KindComment, e.ObjectKind)
	assert.Equal(t, "555", e.ObjectID, "the comment's own id: an exact match against the ledger")
	assert.Equal(t, "github:acme/app", e.Scope)
	assert.Equal(t, "github:acme/app#42", e.Target, "the issue the comment is on, not the comment")
	assert.Equal(t, provenance.BodyDigest("@reliant please take a look at this"), e.BodySHA256)
	assert.Equal(t, "User", e.SenderType)
	assert.Equal(t, "octocat", e.SenderLogin)
	assert.Empty(t, e.AppSlug, "a person's comment has no performed_via_github_app")
}

func TestAppCommentEchoNamesTheAppAndSurvivesLineEndingChanges(t *testing.T) {
	e := echoOfFixture(t, "issue_comment", "issue_comment.created.app")
	assert.Equal(t, "reliant-labs", e.AppSlug, "lowercased, as it is compared")
	assert.Equal(t, "User", e.SenderType, "a user-to-server token still shows the person as sender")
	// GitHub returned CRLF; the write sent LF.
	assert.Equal(t, provenance.BodyDigest("**Review**\n\nLooks good overall."), e.BodySHA256)
}

func TestEchoDigestIsOfTheFullBodyNotTheTruncatedPayload(t *testing.T) {
	ev := one(t, parseFixture(t, "issue_comment", "issue_comment.created.long"))
	recorded := ev.Data["comment"].(map[string]any)["body"].(string)
	require.Contains(t, recorded, truncatedSuffix, "the recorded payload truncates the body")

	raw := ""
	for i := 0; i < 1000; i++ {
		raw += "long line of review text. "
	}
	raw += "END"
	assert.Equal(t, provenance.BodyDigest(raw), ev.Echo.BodySHA256, "the digest is taken before the cap")
	assert.NotEqual(t, provenance.BodyDigest(recorded), ev.Echo.BodySHA256)
}

func TestReviewEchoCarriesIDStateAndCommit(t *testing.T) {
	e := echoOfFixture(t, "pull_request_review", "pull_request_review.submitted")
	assert.Equal(t, provenance.KindReview, e.ObjectKind)
	assert.Equal(t, "3003", e.ObjectID)
	assert.Equal(t, "github:acme/app#7", e.Target)
	assert.Equal(t, "approved", e.ReviewState)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", e.HeadSHA)
	assert.Equal(t, provenance.BodyDigest("Looks good"), e.BodySHA256)
}

func TestReviewCommentEchoCarriesIDAndTarget(t *testing.T) {
	e := echoOfFixture(t, "pull_request_review_comment", "pull_request_review_comment.created")
	assert.Equal(t, provenance.KindReviewComment, e.ObjectKind)
	assert.Equal(t, "9001", e.ObjectID)
	assert.Equal(t, "github:acme/app#7", e.Target)
	assert.NotEmpty(t, e.BodySHA256)
}

func TestPullRequestOpenedEchoCarriesBranchAndHead(t *testing.T) {
	e := echoOfFixture(t, "pull_request", "pull_request.opened")
	assert.Equal(t, provenance.KindPullRequest, e.ObjectKind)
	assert.Equal(t, "7", e.ObjectID)
	assert.Equal(t, "github:acme/app#7", e.Target)
	assert.Equal(t, "fix-save", e.Branch, "matched against a pr.create intent's head")
	assert.NotEmpty(t, e.HeadSHA)
}

func TestLabelEchoesCarryTheLabelAndTheirTarget(t *testing.T) {
	for _, tc := range []struct{ event, name, kind, target string }{
		{"issues", "issues.labeled", provenance.KindIssue, "github:acme/app#42"},
		{"issues", "issues.unlabeled", provenance.KindIssue, "github:acme/app#42"},
		{"pull_request", "pull_request.labeled", provenance.KindPullRequest, "github:acme/app#7"},
		{"pull_request", "pull_request.unlabeled", provenance.KindPullRequest, "github:acme/app#7"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echoOfFixture(t, tc.event, tc.name)
			assert.Equal(t, tc.kind, e.ObjectKind)
			assert.Equal(t, tc.target, e.Target)
			assert.NotEmpty(t, e.Label)
		})
	}
}

func TestSynchronizeEchoCarriesTheHeadAfterThePush(t *testing.T) {
	e := echoOfFixture(t, "pull_request", "pull_request.synchronize")
	assert.Equal(t, "cccccccccccccccccccccccccccccccccccccccc", e.HeadSHA, "the `after` sha, which is what a Reliant push produced")
	assert.Equal(t, "fix-save", e.Branch)
	assert.Equal(t, "github:acme/app#7", e.Target)
}

func TestPushEchoCarriesBranchHeadAndTheRunTrailer(t *testing.T) {
	e := echoOfFixture(t, "push", "push.trailer")
	assert.Equal(t, provenance.KindBranch, e.ObjectKind)
	assert.Equal(t, "main", e.Branch)
	assert.Equal(t, "github:acme/app@main", e.Target)
	assert.Equal(t, "eeee000000000000000000000000000000000002", e.HeadSHA)
	assert.Equal(t, "6e1c0b1e-5d6f-4a3b-9a77-0a9c1d2e3f40", e.RunTrailer)

	plain := echoOfFixture(t, "push", "push")
	assert.Empty(t, plain.RunTrailer, "a push without the trailer claims nothing")
}

func TestATagPushHasNoBranchTarget(t *testing.T) {
	e := echoOfFixture(t, "push", "push.tag")
	assert.Empty(t, e.Branch)
	assert.Empty(t, e.Target)
}

func TestEveryMappedEventCarriesAnEchoWithItsTypeAndScope(t *testing.T) {
	cases := map[string]string{
		"issues.opened": "issues", "issues.labeled": "issues", "issues.closed": "issues",
		"issues.edited": "issues", "issues.assigned": "issues", "issues.unlabeled": "issues", "issues.reopened": "issues",
		"issue_comment.created": "issue_comment", "pull_request.reopened": "pull_request",
		"pull_request.labeled": "pull_request", "pull_request.unlabeled": "pull_request",
		"pull_request_review_comment.created": "pull_request_review_comment",
		"check_run.completed":                 "check_run", "check_suite.completed": "check_suite",
		"check_suite.requested": "check_suite",
		"pull_request.opened":   "pull_request", "pull_request.synchronize": "pull_request",
		"pull_request.closed": "pull_request", "pull_request.review_requested": "pull_request",
		"pull_request.ready_for_review": "pull_request", "pull_request_review.submitted": "pull_request_review",
		"push": "push", "workflow_run.completed": "workflow_run",
	}
	for name, header := range cases {
		ev := one(t, parseFixture(t, header, name))
		require.NotNil(t, ev.Echo, name)
		assert.Equal(t, ev.Type, ev.Echo.EventType, name)
		assert.Equal(t, "github:acme/app", ev.Echo.Scope, name)
		assert.Equal(t, ev.Sender.DisplayName, ev.Echo.SenderLogin, name)
	}
}

func TestEchoIsNotPartOfTheRecordedPayload(t *testing.T) {
	ev := one(t, parseFixture(t, "issue_comment", "issue_comment.created.app"))
	for _, forbidden := range []string{"echo", "body_sha256", "app_slug", "run_trailer"} {
		assert.NotContains(t, ev.Data, forbidden, "correlation is evidence, not data a filter or an agent reads")
	}
}

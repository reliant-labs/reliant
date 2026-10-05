// Copyright (c) 2025 Reliant Labs
package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The fixtures under testdata are trimmed copies of the payloads GitHub's own
// webhook schemas describe (github/rest-api-description, x-webhooks), keeping
// the fields the parser reads and dropping most URLs.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	require.NoError(t, err)
	return b
}

var received = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func parseBody(t *testing.T, event string, body []byte) *Parsed {
	t.Helper()
	p, err := Parse(event, body, received)
	require.NoError(t, err)
	require.NotNil(t, p)
	return p
}

func parseFixture(t *testing.T, event, name string) *Parsed {
	t.Helper()
	return parseBody(t, event, fixture(t, name))
}

func one(t *testing.T, p *Parsed) Event {
	t.Helper()
	require.Len(t, p.Events, 1)
	return p.Events[0]
}

func TestPingIsAHandshake(t *testing.T) {
	p := parseFixture(t, "ping", "ping")
	assert.True(t, p.Ping, "ping is answered, not recorded")
	assert.Empty(t, p.Events)
	assert.Empty(t, p.Revocations)
}

func TestParseRejectsAMissingEventOrABadBody(t *testing.T) {
	_, err := Parse("", fixture(t, "issues.opened"), received)
	assert.Error(t, err)
	_, err = Parse("issues", []byte(`{not json`), received)
	assert.Error(t, err)
}

// Common to every event about a repository: routed by installation and
// repository id (access-gated), with the attributes every trigger exposes.
func assertRepoEvent(t *testing.T, ev Event, typ string) {
	t.Helper()
	assert.Equal(t, typ, ev.Type)
	assert.Equal(t, "98765", ev.AccountKey)
	assert.Equal(t, "123456", ev.ResourceKey)
	assert.Equal(t, "acme/app", ev.Attributes["repository"])
	assert.Equal(t, "98765", ev.Attributes["installation_id"])
	assert.Equal(t, "octocat", ev.Attributes["sender"])
	assert.NotContains(t, ev.Data, "installation", "routing internals stay out of the payload")
}

func TestIssueEvents(t *testing.T) {
	for _, action := range []string{"opened", "labeled", "closed", "edited", "assigned"} {
		t.Run(action, func(t *testing.T) {
			ev := one(t, parseFixture(t, "issues", "issues."+action))
			assertRepoEvent(t, ev, "issues."+action)
			assert.Equal(t, action, ev.Attributes["action"])
			issue := ev.Data["issue"].(map[string]any)
			assert.EqualValues(t, 42, issue["number"])
			assert.Equal(t, "Crash on save", issue["title"])
			assert.Equal(t, []any{"bug"}, issue["labels"], "labels are names")
		})
	}
	labeled := one(t, parseFixture(t, "issues", "issues.labeled"))
	assert.Equal(t, "bug", labeled.Attributes["label"])
	assert.Equal(t, "bug", labeled.Data["label"])
	assigned := one(t, parseFixture(t, "issues", "issues.assigned"))
	assert.Equal(t, "hubot", assigned.Attributes["assignee"])
	closed := one(t, parseFixture(t, "issues", "issues.closed"))
	assert.Equal(t, "completed", closed.Data["issue"].(map[string]any)["state_reason"])
	edited := one(t, parseFixture(t, "issues", "issues.edited"))
	assert.Equal(t, map[string]any{"title": map[string]any{"from": "Crash"}}, edited.Data["changes"])
	assert.Equal(t, time.Date(2026, 10, 5, 10, 0, 0, 0, time.UTC), edited.OccurredAt, "the issue's updated_at")
}

func TestIssueCommentCreated(t *testing.T) {
	ev := one(t, parseFixture(t, "issue_comment", "issue_comment.created"))
	assertRepoEvent(t, ev, "issue_comment.created")
	assert.Equal(t, "true", ev.Attributes["mentions_reliant"], "@reliant in the body")
	assert.Equal(t, "false", ev.Attributes["is_pull_request"])
	comment := ev.Data["comment"].(map[string]any)
	assert.Equal(t, "@reliant please take a look at this", comment["body"])
	assert.EqualValues(t, 42, ev.Data["issue"].(map[string]any)["number"])
}

func TestMentionDetection(t *testing.T) {
	for body, want := range map[string]bool{
		"@reliant fix it":           true,
		"hey @Reliant, look":        true,
		"(@reliant)":                true,
		"ping @reliant-labs please": false,
		"mail me@reliant.dev":       false,
		"no mention":                false,
		"cc @reliantbot":            false,
	} {
		assert.Equal(t, want, mentionPattern.MatchString(body), body)
	}
}

// An App's own comment (ours included) comes back as an event. Dropping it
// stops a trigger whose run comments from re-firing itself forever.
func TestBotCommentsAreDropped(t *testing.T) {
	assert.Empty(t, parseFixture(t, "issue_comment", "issue_comment.created.bot").Events)
}

func TestPullRequestEvents(t *testing.T) {
	for _, action := range []string{"opened", "synchronize", "closed", "review_requested", "ready_for_review"} {
		t.Run(action, func(t *testing.T) {
			ev := one(t, parseFixture(t, "pull_request", "pull_request."+action))
			assertRepoEvent(t, ev, "pull_request."+action)
			assert.Equal(t, "main", ev.Attributes["base_branch"])
			assert.Equal(t, "fix-save", ev.Attributes["branch"])
			pr := ev.Data["pull_request"].(map[string]any)
			assert.EqualValues(t, 7, pr["number"])
			assert.Equal(t, "fix-save", pr["head"].(map[string]any)["ref"])
		})
	}
	merged := one(t, parseFixture(t, "pull_request", "pull_request.closed"))
	assert.Equal(t, "true", merged.Attributes["merged"])
	assert.Equal(t, true, merged.Data["pull_request"].(map[string]any)["merged"])
	requested := one(t, parseFixture(t, "pull_request", "pull_request.review_requested"))
	assert.Equal(t, "hubot", requested.Attributes["requested_reviewer"])
	sync := one(t, parseFixture(t, "pull_request", "pull_request.synchronize"))
	assert.Equal(t, "cccccccccccccccccccccccccccccccccccccccc", sync.Data["after"])
}

func TestPullRequestReviewSubmitted(t *testing.T) {
	ev := one(t, parseFixture(t, "pull_request_review", "pull_request_review.submitted"))
	assert.Equal(t, "pull_request_review.submitted", ev.Type)
	assert.Equal(t, "123456", ev.ResourceKey)
	assert.Equal(t, "approved", ev.Attributes["state"])
	assert.Equal(t, "main", ev.Attributes["base_branch"])
	assert.Equal(t, "Looks good", ev.Data["review"].(map[string]any)["body"])
}

func TestPush(t *testing.T) {
	ev := one(t, parseFixture(t, "push", "push"))
	assertRepoEvent(t, ev, "push")
	assert.NotContains(t, ev.Attributes, "action", "a push has no action")
	assert.Equal(t, "refs/heads/main", ev.Attributes["ref"])
	assert.Equal(t, "main", ev.Attributes["branch"])
	assert.NotContains(t, ev.Attributes, "tag")
	commits := ev.Data["commits"].([]any)
	assert.Len(t, commits, 3)
	first := commits[0].(map[string]any)
	assert.Equal(t, "commit 0", first["message"])
	assert.NotContains(t, first["author"], "email", "author emails are dropped")

	tag := one(t, parseFixture(t, "push", "push.tag"))
	assert.Equal(t, "v1.2.0", tag.Attributes["tag"])
	assert.NotContains(t, tag.Attributes, "branch", "a tag push has no branch, so a branch match never fires on it")
}

func TestWorkflowRunCompleted(t *testing.T) {
	ev := one(t, parseFixture(t, "workflow_run", "workflow_run.completed"))
	assertRepoEvent(t, ev, "workflow_run.completed")
	assert.Equal(t, "failure", ev.Attributes["conclusion"])
	assert.Equal(t, "main", ev.Attributes["branch"])
	assert.Equal(t, "CI", ev.Attributes["workflow"])
	assert.Equal(t, "failure", ev.Data["workflow_run"].(map[string]any)["conclusion"])
}

// Types and actions nobody can listen to parse to no events (and are acked).
func TestUnmappedEventsYieldNoEvents(t *testing.T) {
	assert.Empty(t, parseFixture(t, "star", "star.created").Events)
	assert.Len(t, parseFixture(t, "issues", "issues.opened").Events, 1, "control")
	pinned := []byte(`{"action":"pinned","issue":{"number":1},"repository":{"id":1,"full_name":"a/b"},"installation":{"id":2},"sender":{"login":"x"}}`)
	assert.Empty(t, parseBody(t, "issues", pinned).Events, "an issues action that is not mapped")
}

// An event that does not say which installation and repository it is about
// cannot be routed by access, and is dropped rather than guessed.
func TestEventWithoutInstallationOrRepositoryIsDropped(t *testing.T) {
	noInstallation := []byte(`{"action":"opened","issue":{"number":1},"repository":{"id":1,"full_name":"a/b"},"sender":{"login":"x"}}`)
	assert.Empty(t, parseBody(t, "issues", noInstallation).Events)
	noRepository := []byte(`{"action":"opened","issue":{"number":1},"installation":{"id":2},"sender":{"login":"x"}}`)
	assert.Empty(t, parseBody(t, "issues", noRepository).Events)
}

func TestRevocations(t *testing.T) {
	cases := map[string]struct {
		event, fixture string
		want           []core.IntegrationAccessRevocation
	}{
		"repository removed from installation": {"installation_repositories", "installation_repositories.removed",
			[]core.IntegrationAccessRevocation{{AccountKey: "98765", ResourceKey: "123456"}}},
		"app uninstalled": {"installation", "installation.deleted",
			[]core.IntegrationAccessRevocation{{AccountKey: "98765"}}},
		"member removed from org": {"organization", "organization.member_removed",
			[]core.IntegrationAccessRevocation{{AccountKey: "98765", SubjectID: "22"}}},
		"collaborator removed from repository": {"member", "member.removed",
			[]core.IntegrationAccessRevocation{{AccountKey: "98765", ResourceKey: "123456", SubjectID: "22"}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := parseFixture(t, c.event, c.fixture)
			assert.Equal(t, c.want, p.Revocations)
			assert.Empty(t, p.Events)
		})
	}

	suspend := []byte(`{"action":"suspend","installation":{"id":98765},"sender":{"login":"x"}}`)
	assert.Equal(t, []core.IntegrationAccessRevocation{{AccountKey: "98765"}}, parseBody(t, "installation", suspend).Revocations)

	team := []byte(`{"action":"removed","scope":"team","member":{"login":"bob","id":22},"team":{"name":"core"},"organization":{"login":"acme"},"installation":{"id":98765},"sender":{"login":"x"}}`)
	assert.Equal(t, []core.IntegrationAccessRevocation{{AccountKey: "98765", SubjectID: "22"}}, parseBody(t, "membership", team).Revocations,
		"losing a team can lose repositories: revoke, and the next refresh restores what remains")

	private := []byte(`{"action":"privatized","repository":{"id":123456,"full_name":"acme/app"},"installation":{"id":98765},"sender":{"login":"x"}}`)
	assert.Equal(t, []core.IntegrationAccessRevocation{{AccountKey: "98765", ResourceKey: "123456"}}, parseBody(t, "repository", private).Revocations)

	added := []byte(`{"action":"added","installation":{"id":98765},"repositories_added":[{"id":1}],"repositories_removed":[],"sender":{"login":"x"}}`)
	assert.Empty(t, parseBody(t, "installation_repositories", added).Revocations, "gaining access revokes nothing")
}

// The payload is size-capped: a huge body is trimmed, never stored whole.
func TestDataIsTrimmedAndCapped(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "issues.opened"), &raw))
	raw["issue"].(map[string]any)["body"] = strings.Repeat("x", 200_000)
	body, err := json.Marshal(raw)
	require.NoError(t, err)
	ev := one(t, parseBody(t, "issues", body))
	issueBody := ev.Data["issue"].(map[string]any)["body"].(string)
	assert.LessOrEqual(t, len(issueBody), maxTextBytes+len(truncatedSuffix))
	assert.True(t, strings.HasSuffix(issueBody, truncatedSuffix))
	encoded, err := json.Marshal(ev.Data)
	require.NoError(t, err)
	assert.Less(t, len(encoded), maxDataBytes)
}

// A push whose commit file lists blow the size cap has them cut, then its
// commits, until the payload fits.
func TestPushIsCutToFit(t *testing.T) {
	var raw map[string]any
	require.NoError(t, json.Unmarshal(fixture(t, "push"), &raw))
	files := make([]any, maxListItems)
	for i := range files {
		files[i] = strings.Repeat("dir/", 60) + "file.go"
	}
	var commits []any
	for i := 0; i < maxCommits; i++ {
		c := map[string]any{"id": "c", "message": strings.Repeat("m", 3000), "added": files, "modified": files, "removed": files}
		commits = append(commits, c)
	}
	raw["commits"] = commits
	body, err := json.Marshal(raw)
	require.NoError(t, err)
	ev := one(t, parseBody(t, "push", body))
	encoded, err := json.Marshal(ev.Data)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(encoded), maxDataBytes)
	assert.Equal(t, true, ev.Data["truncated"])
}

// Every event type the parser emits is declared by a trigger in the embedded
// github manifest, with every attribute it sets — so search, the editor and
// filter validation describe what actually arrives.
func TestEmittedEventsMatchTheManifest(t *testing.T) {
	declared := declaredTriggers(t)
	cases := map[string]string{
		"issues.opened": "issues", "issues.labeled": "issues", "issues.closed": "issues",
		"issues.edited": "issues", "issues.assigned": "issues",
		"issue_comment.created":         "issue_comment",
		"pull_request.opened":           "pull_request",
		"pull_request.synchronize":      "pull_request",
		"pull_request.closed":           "pull_request",
		"pull_request.review_requested": "pull_request",
		"pull_request.ready_for_review": "pull_request",
		"pull_request_review.submitted": "pull_request_review",
		"push":                          "push",
		"push.tag":                      "push",
		"workflow_run.completed":        "workflow_run",
	}
	for name, header := range cases {
		ev := one(t, parseFixture(t, header, name))
		attrs, ok := declared[ev.Type]
		require.True(t, ok, "event type %q has no manifest trigger", ev.Type)
		for k := range ev.Attributes {
			assert.Contains(t, attrs, k, "%s sets attribute %q the manifest does not declare", ev.Type, k)
		}
	}
	assert.Len(t, declared, len(EventTypes()), "every mapped type is declared, and nothing else")
}

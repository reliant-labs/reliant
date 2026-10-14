// Copyright (c) 2025 Reliant Labs
package writeledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
)

// The writer (this package's planner) and the reader (the webhook parser) never
// call each other, yet a write's echo has to match its intent field for field.
// A drift in either — a different digest, a differently cased repository, a
// renamed event — silently turns every echo into "not ours". These tests run
// the planner's output against the parser's output for the same logical write.

func parsedEcho(t *testing.T, event, fixture string) *provenance.Echo {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "webhook", "github", "testdata", fixture+".json"))
	require.NoError(t, err)
	parsed, err := github.Parse(event, body, time.Now())
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	require.NotNil(t, parsed.Events[0].Echo)
	return parsed.Events[0].Echo
}

func TestACommentWriteAgreesWithItsEcho(t *testing.T) {
	p := planFor("github", "issue.comment", map[string]any{
		"owner": "Acme", "repo": "App", "issue_number": float64(42),
		"body": "**Review**\n\nLooks good overall.",
	})
	echo := parsedEcho(t, "issue_comment", "issue_comment.created.app")

	assert.Equal(t, p.scope, echo.Scope)
	assert.Equal(t, p.target, echo.Target)
	assert.Equal(t, p.correlation.ObjectKind, echo.ObjectKind)
	assert.Contains(t, p.correlation.Events, echo.EventType)
	assert.Equal(t, p.correlation.BodySHA256, echo.BodySHA256, "GitHub's CRLF and trailing newline must not break the match")
}

func TestASignedCommentWriteStillAgreesWithItsEcho(t *testing.T) {
	// The bot's footer is added after the ledger plans the write, so the echo's
	// body carries it and the intent's does not.
	body := "**Review**\n\nLooks good overall."
	p := planFor("github", "issue.comment", map[string]any{"owner": "Acme", "repo": "App", "issue_number": float64(42), "body": body})
	signed := body + "\n\n" + ghdelegated.Footer("nightly triage", "octocat")
	assert.Equal(t, provenance.BodyDigest(signed), p.correlation.BodySHA256)
}

func TestAReviewWriteAgreesWithItsEcho(t *testing.T) {
	p := planFor("github", "pr.review.create", map[string]any{
		"owner": "acme", "repo": "app", "pull_number": float64(7), "event": "APPROVE", "body": "Looks good",
	})
	echo := parsedEcho(t, "pull_request_review", "pull_request_review.submitted")

	assert.Equal(t, p.target, echo.Target)
	assert.Equal(t, p.correlation.ObjectKind, echo.ObjectKind)
	assert.Contains(t, p.correlation.Events, echo.EventType)
	assert.Equal(t, p.correlation.BodySHA256, echo.BodySHA256)
	assert.Equal(t, p.correlation.ReviewState, echo.ReviewState)
}

func TestALabelWriteAgreesWithItsEcho(t *testing.T) {
	p := planFor("github", "issue.labels.add", map[string]any{"owner": "acme", "repo": "app", "issue_number": float64(7), "label": "agent:ready"})
	echo := parsedEcho(t, "pull_request", "pull_request.labeled")

	assert.Equal(t, p.target, echo.Target)
	assert.Equal(t, p.correlation.Label, echo.Label)
	assert.Contains(t, p.correlation.Events, echo.EventType, "issue.labels.add on a PR's number echoes as pull_request.labeled")
}

func TestAPullRequestCreateAgreesWithItsOpenedEcho(t *testing.T) {
	p := planFor("github", "pr.create", map[string]any{"owner": "acme", "repo": "app", "head": "fix-save", "base": "main", "title": "Fix"})
	echo := parsedEcho(t, "pull_request", "pull_request.opened")

	assert.Equal(t, p.scope, echo.Scope)
	assert.Equal(t, p.correlation.ObjectKind, echo.ObjectKind)
	assert.Contains(t, p.correlation.Events, echo.EventType)
	assert.Equal(t, p.correlation.Branch, echo.Branch, "no number exists yet: the head branch is what ties the echo to the intent")
}

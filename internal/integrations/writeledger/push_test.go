// Copyright (c) 2025 Reliant Labs
package writeledger_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/webhook/github"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

func TestPushIntentIsRecordedBeforeThePushWithItsSha(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	chatID, _ := seedRun(t, repo, "autopilot-issue")
	rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})

	intent, err := rec.BeginPush(ctx, writeledger.Push{Repo: "Acme/App", Branch: "autopilot/issue-1", SHA: "abc1234def", RunID: chatID, NodeID: "publish"})
	require.NoError(t, err)

	// Before the push is made, the row is visible, matches by target and sha,
	// and names the run.
	recent, err := repo.IntegrationWrites().ListRecentIntegrationWrites(ctx, "github", "github:acme/app", time.Now().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	w := recent[0]
	assert.Equal(t, "git.push", w.Action)
	assert.Equal(t, core.IntegrationWriteIntent, w.Status)
	assert.Equal(t, "github:acme/app@autopilot/issue-1", w.Target)
	assert.Equal(t, "abc1234def", w.ObjectID)
	assert.Equal(t, core.IntegrationActorOwner, w.Actor)
	assert.Equal(t, "autopilot-issue", w.WorkflowName)
	assert.Equal(t, chatID, w.ChatID)
	c := provenance.CorrelationFromMap(w.Correlation)
	assert.Equal(t, provenance.KindBranch, c.ObjectKind)
	assert.Equal(t, []string{"push"}, c.Events)
	assert.Equal(t, "autopilot/issue-1", c.Branch)
	assert.Equal(t, "abc1234def", c.HeadSHA)

	intent.Settle(ctx, writeledger.PushOutcome{Pushed: true})
	got, err := repo.IntegrationWrites().GetIntegrationWriteByObject(ctx, "github", "github:acme/app", provenance.KindBranch, "abc1234def")
	require.NoError(t, err, "a done push is found by its sha, which is how the push event is matched exactly")
	assert.Equal(t, core.IntegrationWriteDone, got.Status)
}

// A push to a pull request's head echoes twice: push, and pull_request.synchronize.
func TestPushToAPullRequestHeadIsAlsoRecordedAsItsSynchronize(t *testing.T) {
	repo := db.NewTestRepo(t)
	ctx := context.Background()
	rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})
	intent, err := rec.BeginPush(ctx, writeledger.Push{Repo: "acme/app", Branch: "fix-save", SHA: "cccccccccccccccccccccccccccccccccccccccc", PullNumber: 7, Actor: core.IntegrationActorApp})
	require.NoError(t, err)
	assert.Len(t, intent.IDs(), 2)

	recent, err := repo.IntegrationWrites().ListRecentIntegrationWrites(ctx, "github", "github:acme/app", time.Now().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	targets := map[string]bool{}
	for _, w := range recent {
		targets[w.Target] = true
		assert.Equal(t, core.IntegrationActorApp, w.Actor, "the identity that pushed is the one recorded")
	}
	assert.True(t, targets["github:acme/app@fix-save"] && targets["github:acme/app#7"], "%v", targets)
}

// The write and the push event it causes agree: the ledger row matches the echo
// the webhook parser makes from GitHub's real push payload.
func TestAPushWriteAgreesWithItsPushEcho(t *testing.T) {
	repo := db.NewTestRepo(t)
	rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})
	_, err := rec.BeginPush(context.Background(), writeledger.Push{Repo: "acme/app", Branch: "main", SHA: "eeee000000000000000000000000000000000002"})
	require.NoError(t, err)
	w := mustOne(t, repo)

	echo := parsedPushEcho(t)
	c := provenance.CorrelationFromMap(w.Correlation)
	assert.Equal(t, echo.Scope, w.Scope)
	assert.Equal(t, provenance.KindBranch, echo.ObjectKind)
	assert.Equal(t, c.ObjectKind, echo.ObjectKind)
	assert.Contains(t, c.Events, "push")
	assert.Equal(t, "push", echo.EventType)
	assert.Equal(t, echo.Target, provenance.BranchTargetOf(w.Scope, c.Branch), "same branch target")
	assert.Equal(t, echo.HeadSHA, c.HeadSHA)
}

func TestFailedPushIsSettledFailedAndAmbiguousWhenItMayHaveApplied(t *testing.T) {
	for name, tc := range map[string]struct {
		out       writeledger.PushOutcome
		ambiguous bool
	}{
		"refused by the remote": {writeledger.PushOutcome{Error: "non-fast-forward"}, false},
		"the machine vanished":  {writeledger.PushOutcome{Error: "daemon disconnected", Ambiguous: true}, true},
	} {
		t.Run(name, func(t *testing.T) {
			repo := db.NewTestRepo(t)
			ctx := context.Background()
			rec := writeledger.New(writeledger.Deps{Store: repo.IntegrationWrites(), Runs: repo})
			intent, err := rec.BeginPush(ctx, writeledger.Push{Repo: "acme/app", Branch: "x", SHA: "abc1234"})
			require.NoError(t, err)
			intent.Settle(ctx, tc.out)

			var status string
			var ambiguous bool
			require.NoError(t, repo.DB.SQLDB().QueryRow(`SELECT status, ambiguous FROM integration_writes`).Scan(&status, &ambiguous))
			assert.Equal(t, "failed", status)
			assert.Equal(t, tc.ambiguous, ambiguous)
		})
	}
}

func TestPushIntentThatCannotBeRecordedRefusesThePush(t *testing.T) {
	rec := writeledger.New(writeledger.Deps{Store: brokenStore{}})
	intent, err := rec.BeginPush(context.Background(), writeledger.Push{Repo: "acme/app", Branch: "x", SHA: "abc1234"})
	require.Error(t, err, "an unattributable push is how two workflows loop")
	assert.Nil(t, intent)
}

func TestPushIntentNeedsRepoBranchAndSha(t *testing.T) {
	rec := writeledger.New(writeledger.Deps{Store: brokenStore{}})
	for _, p := range []writeledger.Push{{Branch: "x", SHA: "a"}, {Repo: "a/b", SHA: "a"}, {Repo: "a/b", Branch: "x"}} {
		_, err := rec.BeginPush(context.Background(), p)
		require.Error(t, err)
	}
}

func TestNilLedgerAndNilIntentAreInert(t *testing.T) {
	var rec *writeledger.Recorder
	intent, err := rec.BeginPush(context.Background(), writeledger.Push{Repo: "a/b", Branch: "x", SHA: "a"})
	require.NoError(t, err)
	assert.Nil(t, intent)
	intent.Settle(context.Background(), writeledger.PushOutcome{Pushed: true}) // must not panic
	assert.Nil(t, intent.IDs())
}

func mustOne(t *testing.T, repo *db.Repo) *core.IntegrationWrite {
	t.Helper()
	recent, err := repo.IntegrationWrites().ListRecentIntegrationWrites(context.Background(), "github", "github:acme/app", time.Now().Add(-time.Minute), 10)
	require.NoError(t, err)
	require.Len(t, recent, 1)
	return recent[0]
}

func parsedPushEcho(t *testing.T) *provenance.Echo {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "webhook", "github", "testdata", "push.json"))
	require.NoError(t, err)
	parsed, err := github.Parse("push", body, time.Now())
	require.NoError(t, err)
	require.Len(t, parsed.Events, 1)
	return parsed.Events[0].Echo
}

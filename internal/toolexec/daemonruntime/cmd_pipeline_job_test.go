// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/buildjob"
)

func TestPipelineJobCommandsAreRegistered(t *testing.T) {
	for _, c := range []string{buildjob.CommandStart, buildjob.CommandStatus, buildjob.CommandCancel} {
		if _, err := DefaultRegistry().Handle(context.Background(), c, []byte(`{"job_id":"nope"}`)); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
}

// The token is minted by this binary's `github token --as app`, scoped to
// the one repository and to a build's permissions, under the daemon session
// the git credential helper is pinned to.
func TestQueueTokenArgvIsPinnedToTheHelpersSession(t *testing.T) {
	SetGitCredentialHelper([]string{"/bin/reliant", "auth", "git-credential", "--daemon-server", "https://s.example", "--daemon-account", "acct"})
	t.Cleanup(func() { SetGitCredentialHelper(nil) })
	got := queueTokenArgv("acme/widgets")
	want := []string{"/bin/reliant", "github", "token", "--as", "app", "--repo", "acme/widgets", "--json",
		"--permission", "contents=write", "--permission", "pull_requests=read",
		"--daemon-server", "https://s.example", "--daemon-account", "acct"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv\n got %q\nwant %q", got, want)
	}
}

func TestQueueTokensAreKeptUntilShortlyBeforeTheyExpire(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	mints := 0
	q := &queueTokens{
		now: func() time.Time { return now },
		mint: func(_ context.Context, repository string) (queueToken, error) {
			mints++
			return queueToken{token: repository + "-" + string(rune('0'+mints)), expires: now.Add(time.Hour)}, nil
		},
	}
	tok := func(repo string) string {
		t.Helper()
		s, err := q.Token(context.Background(), repo)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if a, b := tok("acme/widgets"), tok("Acme/Widgets"); a != b || mints != 1 {
		t.Fatalf("a kept token was minted again: %q %q after %d mints", a, b, mints)
	}
	if tok("acme/other") == tok("acme/widgets") {
		t.Fatal("two repositories share a token")
	}
	now = now.Add(time.Hour - queueTokenSlack)
	if tok("acme/widgets") == "acme/widgets-1" {
		t.Fatal("a token within its slack of expiry was handed out")
	}
}

// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gitpublish"
)

// commitIn writes name in a fresh clone of f's remote at branch, commits and
// pushes it there (a plain push), and returns the new head: someone else's push.
func (f publishFixture) commitIn(t *testing.T, base, branch, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	pubGit(t, filepath.Dir(dir), "clone", "-q", f.remote, dir)
	if base != "" {
		pubGit(t, dir, "fetch", "-q", "origin", "refs/heads/"+base+":refs/remotes/origin/"+base)
		pubGit(t, dir, "checkout", "-q", "--detach", "origin/"+base)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644))
	pubGit(t, dir, "add", "-A")
	pubGit(t, dir, "commit", "-q", "-m", name)
	pubGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	return pubGit(t, dir, "rev-parse", "HEAD")
}

// force_with_lease replaces a branch only while the remote still holds the
// commit the run built on, and otherwise refuses, naming where the branch is.
// Without it the push is exactly what it always was: never forced.
func TestGitPublish_ForceWithLease(t *testing.T) {
	t.Parallel()
	const branch = "fix/save"

	type setup struct {
		f      publishFixture
		lease  string // the sha the node names
		moved  string // where someone else moved the branch after the lease was taken
		before string // the remote branch head before the publish
	}
	for _, tc := range []struct {
		name string
		// arrange builds the remote branch and the checkout, and says what the
		// lease is.
		arrange func(t *testing.T, s *setup)
		// overHTTP serves the remote behind basic auth, so the read-back after a
		// refused lease must authenticate with the same one-shot credential.
		overHTTP bool
		check    func(t *testing.T, s setup, prep gitpublish.PrepareResponse, res gitpublish.PushResponse)
	}{
		{
			name: "the lease holds: a rewritten history replaces the branch",
			arrange: func(t *testing.T, s *setup) {
				// The PR head is E; the checkout is main plus a different commit:
				// not a fast-forward of E.
				s.lease = s.f.commitIn(t, "", branch, "theirs.txt")
				s.before = s.lease
			},
			check: func(t *testing.T, s setup, prep gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				require.True(t, res.Pushed, res.Refusal)
				assert.True(t, res.LeaseChecked)
				assert.False(t, res.LeaseRefused)
				assert.Equal(t, prep.HeadSHA, remoteHead(t, s.f, branch), "the branch was replaced")
			},
		},
		{
			name: "the lease holds: a fast-forward of the leased head",
			arrange: func(t *testing.T, s *setup) {
				s.lease = s.f.commitIn(t, "", branch, "theirs.txt")
				s.before = s.lease
				pubGit(t, s.f.clone, "fetch", "-q", "origin")
				pubGit(t, s.f.clone, "checkout", "-q", "--detach", "origin/"+branch)
			},
			check: func(t *testing.T, s setup, prep gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				require.True(t, res.Pushed, res.Refusal)
				assert.True(t, res.LeaseChecked)
				assert.Equal(t, s.lease, prep.BaseSHA, "the fix sits on the leased head")
				assert.Equal(t, prep.HeadSHA, remoteHead(t, s.f, branch))
			},
		},
		{
			name: "the remote moved: refused, naming the expected and the actual head",
			arrange: func(t *testing.T, s *setup) {
				s.lease = s.f.commitIn(t, "", branch, "theirs.txt")
				// The owner pushes after the run took its lease.
				s.moved = s.f.commitIn(t, branch, branch, "owner.txt")
				s.before = s.moved
			},
			check: func(t *testing.T, s setup, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.False(t, res.Pushed)
				assert.True(t, res.LeaseChecked)
				assert.True(t, res.LeaseRefused, res.Refusal)
				assert.False(t, res.NonFastForward, "a lease refusal is its own kind")
				assert.Equal(t, s.moved, res.RemoteSHA)
				assert.Contains(t, res.Refusal, s.lease, "the refusal names the expected head")
				assert.Contains(t, res.Refusal, s.moved, "and the actual one")
				assert.Equal(t, s.moved, remoteHead(t, s.f, branch), "the owner's push stands")
			},
		},
		{
			name:     "the remote moved, over authenticated HTTP: the read-back uses the credential too",
			overHTTP: true,
			arrange: func(t *testing.T, s *setup) {
				s.lease = s.f.commitIn(t, "", branch, "theirs.txt")
				s.moved = s.f.commitIn(t, branch, branch, "owner.txt")
				s.before = s.moved
			},
			check: func(t *testing.T, s setup, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.True(t, res.LeaseRefused, res.Refusal)
				assert.Equal(t, s.moved, res.RemoteSHA)
				assert.NotContains(t, res.Refusal, publishToken)
				assert.Equal(t, s.moved, remoteHead(t, s.f, branch))
			},
		},
		{
			name: "the branch does not exist: refused, nothing created",
			arrange: func(t *testing.T, s *setup) {
				s.lease = pubGit(t, s.f.clone, "rev-parse", "HEAD")
			},
			check: func(t *testing.T, s setup, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.False(t, res.Pushed)
				assert.True(t, res.LeaseRefused, res.Refusal)
				assert.Empty(t, res.RemoteSHA)
				assert.Contains(t, res.Refusal, "does not exist")
				assert.Empty(t, remoteHead(t, s.f, branch))
			},
		},
		{
			name: "an abbreviated sha is refused before anything is pushed",
			arrange: func(t *testing.T, s *setup) {
				s.before = s.f.commitIn(t, "", branch, "theirs.txt")
				s.lease = s.before[:12]
			},
			check: func(t *testing.T, s setup, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.False(t, res.Pushed)
				assert.False(t, res.LeaseChecked)
				assert.Contains(t, res.Refusal, "full sha")
				assert.Equal(t, s.before, remoteHead(t, s.f, branch))
			},
		},
		{
			name: "no lease: the default is unchanged, a diverged remote is refused, not forced",
			arrange: func(t *testing.T, s *setup) {
				s.before = s.f.commitIn(t, "", branch, "theirs.txt")
			},
			check: func(t *testing.T, s setup, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.False(t, res.Pushed)
				assert.True(t, res.NonFastForward, res.Refusal)
				assert.False(t, res.LeaseChecked)
				assert.False(t, res.LeaseRefused)
				assert.Contains(t, res.Refusal, "not forced")
				assert.Equal(t, s.before, remoteHead(t, s.f, branch))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			needGit(t)
			allowFileRemotes(t)
			s := setup{f: newPublishFixture(t)}
			tc.arrange(t, &s)
			require.NoError(t, os.WriteFile(filepath.Join(s.f.clone, "ours.txt"), []byte("ours\n"), 0o644))
			prep := prepare(t, s.f.clone, "ours")
			require.Positive(t, prep.Commits)

			req := s.f.pushReq(branch, prep.HeadSHA)
			req.ForceWithLease = s.lease
			if tc.overHTTP {
				req.RemoteURL = newSmartHTTP(t, s.f.remote).URL + "/remote.git"
			}
			tc.check(t, s, prep, push(t, req))
		})
	}
}

// This daemon tells the server it enforces a lease: the server sends a
// lease-bearing push to no machine whose registration lacks it.
func TestGitPublish_RegistrationAdvertisesForceWithLease(t *testing.T) {
	t.Parallel()
	assert.Contains(t, daemonFeatures, gitpublish.CapabilityForceWithLease)
}

// Why an older daemon must never be sent a lease: it ignores the field and
// makes a plain push, which recreates a deleted branch and fast-forwards a
// branch that was moved back to an ancestor of the fix. The lease refuses both.
func TestGitPublish_PlainPushIsNoSubstituteForALease(t *testing.T) {
	t.Parallel()
	const branch = "fix/save"
	for _, tc := range []struct {
		name string
		// arrange leaves the remote branch where someone else put it after
		// the run leased head, and returns head.
		arrange func(t *testing.T, f publishFixture) (head string)
		// lease true sends the lease; false is what an old daemon does with it.
		lease bool
		check func(t *testing.T, f publishFixture, head string, prep gitpublish.PrepareResponse, res gitpublish.PushResponse)
	}{
		{
			name: "deleted branch, plain push: recreated", lease: false,
			arrange: deletedAfterLease,
			check: func(t *testing.T, f publishFixture, _ string, prep gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.True(t, res.Pushed, res.Refusal)
				assert.Equal(t, prep.HeadSHA, remoteHead(t, f, branch), "the deleted branch came back")
			},
		},
		{
			name: "deleted branch, lease: refused", lease: true,
			arrange: deletedAfterLease,
			check: func(t *testing.T, f publishFixture, _ string, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.True(t, res.LeaseRefused, res.Refusal)
				assert.Empty(t, remoteHead(t, f, branch))
			},
		},
		{
			name: "moved to an ancestor, plain push: fast-forwarded over it", lease: false,
			arrange: rewoundAfterLease,
			check: func(t *testing.T, f publishFixture, _ string, prep gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.True(t, res.Pushed, res.Refusal)
				assert.Equal(t, prep.HeadSHA, remoteHead(t, f, branch), "the rewind was overwritten")
			},
		},
		{
			name: "moved to an ancestor, lease: refused", lease: true,
			arrange: rewoundAfterLease,
			check: func(t *testing.T, f publishFixture, head string, _ gitpublish.PrepareResponse, res gitpublish.PushResponse) {
				assert.True(t, res.LeaseRefused, res.Refusal)
				ancestor := remoteHead(t, f, branch)
				assert.NotEqual(t, head, ancestor)
				assert.Equal(t, ancestor, res.RemoteSHA)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			needGit(t)
			allowFileRemotes(t)
			f := newPublishFixture(t)
			head := tc.arrange(t, f)
			require.NoError(t, os.WriteFile(filepath.Join(f.clone, "ours.txt"), []byte("ours\n"), 0o644))
			prep := prepare(t, f.clone, "ours")
			req := f.pushReq(branch, prep.HeadSHA)
			if tc.lease {
				req.ForceWithLease = head
			}
			tc.check(t, f, head, prep, push(t, req))
		})
	}
}

// deletedAfterLease: the checkout is at the PR head (two commits on main),
// and then the branch is deleted on the remote.
func deletedAfterLease(t *testing.T, f publishFixture) string {
	t.Helper()
	head := checkoutTwoCommitHead(t, f)
	pubGit(t, f.remote, "update-ref", "-d", "refs/heads/fix/save")
	return head
}

// rewoundAfterLease: the checkout is at the PR head, and then the branch is
// moved back to that head's parent on the remote.
func rewoundAfterLease(t *testing.T, f publishFixture) string {
	t.Helper()
	head := checkoutTwoCommitHead(t, f)
	pubGit(t, f.remote, "update-ref", "refs/heads/fix/save", head+"^")
	return head
}

func checkoutTwoCommitHead(t *testing.T, f publishFixture) string {
	t.Helper()
	f.commitIn(t, "", "fix/save", "one.txt")
	head := f.commitIn(t, "fix/save", "fix/save", "two.txt")
	pubGit(t, f.clone, "fetch", "-q", "origin")
	pubGit(t, f.clone, "checkout", "-q", "--detach", head)
	return head
}

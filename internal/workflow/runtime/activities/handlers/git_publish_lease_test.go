// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/gitpublish"
)

func withLease(node *reliantv1.Node, sha string) *reliantv1.Node {
	node.GetGitPublish().ForceWithLease = lit(sha)
	return node
}

// registerDaemon re-registers the fixture's machine with capabilities, as a
// daemon's DaemonRegister does on every connect. nil is a daemon that lists
// no capabilities at all.
func (f *publishFixture) registerDaemon(t *testing.T, caps ...string) {
	t.Helper()
	raw, err := json.Marshal(caps)
	require.NoError(t, err)
	js := string(raw)
	require.NoError(t, f.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: activityDefaultDaemon, UserID: f.userID, Capabilities: &js}))
}

// newLeaseDaemon is the capability list of a daemon that enforces a lease.
var newLeaseDaemon = []string{"bash", gitpublish.CapabilityForceWithLease, "read_file"}

// force_with_lease travels to the machine with the push, and what the machine
// says about it comes back as the node's outputs: a refused lease is a
// refusal the graph routes on, never retried, with both commits named.
func TestGitPublishActivity_ForceWithLease(t *testing.T) {
	t.Parallel()
	expected := strings.Repeat("e", 40)
	actual := strings.Repeat("a", 40)

	for _, tc := range []struct {
		name  string
		node  *reliantv1.Node
		reply gitpublish.PushResponse
		// wantLease is what the push payload carries; "-" means no push is sent.
		wantLease string
		check     func(t *testing.T, out *reliantv1.GitPublishOutput)
	}{
		{
			name:      "the lease holds",
			node:      withLease(publishNode("", 7), expected),
			reply:     gitpublish.PushResponse{Pushed: true, LeaseChecked: true},
			wantLease: expected,
			check: func(t *testing.T, out *reliantv1.GitPublishOutput) {
				assert.True(t, out.Pushed, out.Refusal)
				assert.False(t, out.Refused)
				assert.False(t, out.LeaseRefused)
			},
		},
		{
			name: "the remote moved",
			node: withLease(publishNode("", 7), expected),
			reply: gitpublish.PushResponse{LeaseChecked: true, LeaseRefused: true, RemoteSHA: actual,
				Refusal: "the lease did not hold: fix/save was expected at " + expected + " but is at " + actual + "; nothing was pushed"},
			wantLease: expected,
			check: func(t *testing.T, out *reliantv1.GitPublishOutput) {
				assert.False(t, out.Pushed)
				assert.True(t, out.Refused)
				assert.True(t, out.LeaseRefused)
				assert.Equal(t, actual, out.RemoteSha)
				assert.Contains(t, out.Refusal, expected)
				assert.Contains(t, out.Refusal, actual)
			},
		},
		{
			name:      "an abbreviated sha is refused before the machine is asked anything",
			node:      withLease(publishNode("", 7), expected[:12]),
			wantLease: "-",
			check: func(t *testing.T, out *reliantv1.GitPublishOutput) {
				assert.True(t, out.Refused)
				assert.Contains(t, out.Refusal, "full sha")
			},
		},
		{
			name:      "no lease: the push payload has none",
			node:      publishNode("autopilot/issue-12", 0),
			reply:     gitpublish.PushResponse{Pushed: true},
			wantLease: "",
			check: func(t *testing.T, out *reliantv1.GitPublishOutput) {
				assert.True(t, out.Pushed, out.Refusal)
				assert.False(t, out.LeaseRefused)
				assert.Empty(t, out.RemoteSha)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublishActivityFixture(t)
			f.registerDaemon(t, newLeaseDaemon...)
			f.router.push = tc.reply
			out, err := f.run(t, tc.node)
			require.NoError(t, err, "a lease refusal is an output, not a failed (retried) node")
			tc.check(t, out)

			if tc.wantLease == "-" {
				assert.Empty(t, f.router.commands(), "nothing reached the machine")
				assert.Zero(t, f.tok.calls, "no credential was fetched")
				return
			}
			require.Equal(t, []string{gitpublish.CommandPrepare, gitpublish.CommandPush}, f.router.commands())
			var push gitpublish.PushRequest
			require.NoError(t, json.Unmarshal(f.router.sent[1].payload, &push))
			assert.Equal(t, tc.wantLease, push.ForceWithLease)
			assert.Equal(t, "abc1234def", push.ExpectSHA, "the pushed commit is still the prepared one")
		})
	}
}

// A daemon older than force_with_lease would ignore the field and make a plain
// push: that recreates a branch someone deleted, and fast-forwards a branch
// someone moved back to an ancestor of the fix, both of which the lease exists
// to refuse. So the activity asks the machine's registration BEFORE sending
// anything, and a machine that does not list the capability gets nothing: no
// commit, no credential, no ledger row, no push. The simulated old daemon
// here answers every push "pushed", as its plain push would in both cases.
func TestGitPublishActivity_ForceWithLeaseNeedsADaemonThatEnforcesIt(t *testing.T) {
	t.Parallel()
	expected := strings.Repeat("e", 40)

	for _, tc := range []struct {
		name string
		// register sets the machine's registration; nil leaves the fixture's,
		// which lists no capabilities.
		register func(t *testing.T, f *publishFixture)
		lease    string
		wantPush bool
	}{
		{name: "an old daemon, the branch was deleted: refused, nothing recreated", lease: expected},
		{name: "an old daemon, the head moved to an ancestor: refused, nothing fast-forwarded", lease: expected,
			register: func(t *testing.T, f *publishFixture) { f.registerDaemon(t, "bash", "read_file") }},
		{name: "a registration whose capabilities cannot be read: refused", lease: expected,
			register: func(t *testing.T, f *publishFixture) {
				bad := "not json"
				require.NoError(t, f.repo.UpsertDaemon(context.Background(), &db.Daemon{ID: activityDefaultDaemon, UserID: f.userID, Capabilities: &bad}))
			}},
		{name: "a new daemon: the lease is sent", lease: expected, wantPush: true,
			register: func(t *testing.T, f *publishFixture) { f.registerDaemon(t, newLeaseDaemon...) }},
		{name: "an old daemon with no lease: an ordinary push, unchanged", wantPush: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublishActivityFixture(t)
			if tc.register != nil {
				tc.register(t, f)
			}
			f.router.push = gitpublish.PushResponse{Pushed: true, LeaseChecked: tc.wantPush && tc.lease != ""}
			node := publishNode("", 7)
			if tc.lease != "" {
				node = withLease(node, tc.lease)
			}
			out, err := f.run(t, node)
			require.NoError(t, err, "an unsupported lease is a refusal, not a failed (retried) node")

			if tc.wantPush {
				assert.True(t, out.Pushed, out.Refusal)
				assert.False(t, out.LeaseUnsupported)
				require.Equal(t, []string{gitpublish.CommandPrepare, gitpublish.CommandPush}, f.router.commands())
				var push gitpublish.PushRequest
				require.NoError(t, json.Unmarshal(f.router.sent[1].payload, &push))
				assert.Equal(t, tc.lease, push.ForceWithLease)
				return
			}
			assert.False(t, out.Pushed)
			assert.True(t, out.Refused)
			assert.True(t, out.LeaseUnsupported)
			assert.Contains(t, out.Refusal, gitpublish.CapabilityForceWithLease, "the refusal names the missing capability")
			assert.Contains(t, out.Refusal, activityDefaultDaemon, "and the machine")
			assert.Empty(t, f.router.commands(), "nothing reached the machine: no commit, no push")
			assert.Zero(t, f.tok.calls, "no credential was fetched")
			assert.Empty(t, f.ledger.pushes, "no write was recorded")
		})
	}
}

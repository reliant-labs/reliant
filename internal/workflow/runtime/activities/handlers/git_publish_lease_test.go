// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/gitpublish"
)

func withLease(node *reliantv1.Node, sha string) *reliantv1.Node {
	node.GetGitPublish().ForceWithLease = lit(sha)
	return node
}

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

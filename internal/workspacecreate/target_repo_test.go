package workspacecreate

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

// One project, three registered repos. Only the target has the feature
// branch; applying it to either sibling must fail, not silently use it.
func TestThreeRepoTargetKeepsSiblingDefaults(t *testing.T) {
	req := twoRepoReq()
	req.Repos = append(req.Repos, &core.Repo{ID: "r3", Name: "c", RelativePath: "c"})
	req.BaseBranches = map[string]string{"r1": "feature/target-only"}
	req.FetchRepoID, req.FetchBranch, req.ExpectedHead = "r1", "feature/target-only", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	st := &fakeStore{}
	m := &targetOnlyMachine{fakeMachine: fakeMachine{root: "/w/target-case"}}
	wt, err := Insert(context.Background(), st, req)
	require.NoError(t, err)
	require.NoError(t, Finish(context.Background(), st, m, req, wt))
	creates := m.cmds("worktree.create")
	require.Len(t, creates, 3)
	for _, call := range creates {
		if call.payload["sub_path"] == "a" {
			require.Equal(t, "feature/target-only", call.payload["base_branch"])
			require.Equal(t, "feature/target-only", call.payload["fetch_branch"])
			require.Equal(t, req.ExpectedHead, call.payload["expected_head"])
		} else {
			require.Equal(t, "", call.payload["base_branch"], "sibling auto-detects its own default")
			require.NotContains(t, call.payload, "fetch_branch")
			require.NotContains(t, call.payload, "expected_head")
		}
	}
}

type targetOnlyMachine struct{ fakeMachine }

func (m *targetOnlyMachine) Send(ctx context.Context, cmd string, payload, resp any, timeout int32) error {
	// fakeMachine records JSON payloads and succeeds; all three outputs are
	// checked above rather than requiring external git for this fast wiring proof.
	return m.fakeMachine.Send(ctx, cmd, payload, resp, timeout)
}

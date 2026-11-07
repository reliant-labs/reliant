package daemonruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLookupWorktreePR(t *testing.T) {
	t.Parallel()
	const valid = `{"number":42,"url":"https://github.com/org/repo/pull/42","title":"Fix","state":"OPEN","headRefOid":"abc"}`
	for _, tc := range []struct {
		name, list, view, head string
		failAt                 int
		err                    error
		wantError, exists      bool
	}{
		{name: "absent", list: `[]`},
		{name: "open", list: `[{"number":42}]`, view: valid, head: "abc\n", exists: true},
		{name: "missing gh", failAt: 1, err: exec.ErrNotFound, wantError: true},
		{name: "authentication", failAt: 1, err: errors.New("authentication failed"), wantError: true},
		{name: "network", failAt: 1, err: errors.New("network unavailable"), wantError: true},
		{name: "cancelled", failAt: 1, err: context.Canceled, wantError: true},
		{name: "malformed list", list: `{`, wantError: true},
		{name: "null list", list: `null`, wantError: true},
		{name: "view failure", list: `[{"number":42}]`, failAt: 2, err: errors.New("repository inaccessible"), wantError: true},
		{name: "malformed view", list: `[{"number":42}]`, view: `{`, wantError: true},
		{name: "empty view", list: `[{"number":42}]`, view: `{}`, wantError: true},
		{name: "head failure", list: `[{"number":42}]`, view: valid, failAt: 3, err: errors.New("bad HEAD"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			resp := lookupWorktreePR(context.Background(), "/worktree", "feature", func(cmd *exec.Cmd) ([]byte, error) {
				calls++
				require.Equal(t, "/worktree", cmd.Dir)
				switch calls {
				case 1:
					require.Equal(t, []string{"gh", "pr", "list", "--head", "feature", "--state", "all", "--limit", "1", "--json", "number"}, cmd.Args)
				case 2:
					require.Equal(t, []string{"gh", "pr", "view", "feature", "--json", "url,number,title,state,headRefOid"}, cmd.Args)
				case 3:
					require.Equal(t, []string{"git", "rev-parse", "HEAD"}, cmd.Args)
				default:
					t.Fatal("unexpected command")
				}
				if calls == tc.failAt {
					return nil, tc.err
				}
				return []byte([]string{tc.list, tc.view, tc.head}[calls-1]), nil
			})
			if tc.wantError {
				require.NotEmpty(t, resp.Error)
				require.False(t, resp.Exists)
			} else {
				require.Empty(t, resp.Error)
				require.Equal(t, tc.exists, resp.Exists)
			}
			if tc.name == "missing gh" {
				require.Contains(t, resp.Error, "install gh")
			}
			if tc.exists {
				require.Equal(t, int32(42), resp.Number)
				require.Equal(t, "abc", resp.LocalHead)
			}
			if tc.name == "absent" {
				require.Equal(t, 1, calls)
			}
		})
	}
}

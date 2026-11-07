package worktree_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
	"github.com/stretchr/testify/require"
)

func TestGetWorktreePRLookup(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, payload string
		err           error
		code          connect.Code
		exists        bool
	}{
		{name: "absent", payload: `{"exists":false}`},
		{name: "transport failure", err: errors.New("offline"), code: connect.CodeUnavailable},
		{name: "deadline", err: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		{name: "daemon error envelope", payload: `{"exists":false,"error":"install gh and run gh auth login"}`, code: connect.CodeUnavailable},
		{name: "invalid payload", payload: `{`, code: connect.CodeUnavailable},
		{name: "open diverged", payload: `{"exists":true,"number":42,"state":"OPEN","local_head":"new","head_ref_oid":"old"}`, exists: true},
		{name: "merged matching", payload: `{"exists":true,"number":42,"state":"MERGED","local_head":"same","head_ref_oid":"same"}`, exists: true},
		{name: "closed matching", payload: `{"exists":true,"number":42,"state":"CLOSED","local_head":"same","head_ref_oid":"same"}`, exists: true},
		{name: "merged diverged", payload: `{"exists":true,"state":"MERGED","local_head":"new","head_ref_oid":"old"}`},
		{name: "closed diverged", payload: `{"exists":true,"state":"CLOSED","local_head":"new","head_ref_oid":"old"}`},
		{name: "legacy missing heads", payload: `{"exists":true,"number":42,"state":"MERGED"}`, exists: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := readRouter(func(_ context.Context, command string, _ []byte) ([]byte, error) {
				switch command {
				case "worktree.validate_path":
					return []byte(`{"exists":true}`), nil
				case "worktree.get_pr":
					return []byte(tc.payload), tc.err
				default:
					return nil, fmt.Errorf("unexpected command %s", command)
				}
			})
			resp, err := readService(t, singleRepoStore(t), router).GetWorktreePR(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.GetWorktreePRRequest{WorktreeId: ownWorktreeID}))
			if tc.code != 0 {
				require.Error(t, err)
				require.Nil(t, resp)
				require.Equal(t, tc.code, svcerr.Code(err))
				if tc.name == "daemon error envelope" {
					require.Contains(t, err.Error(), "install gh")
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.exists, resp.Msg.Exists)
			if tc.exists {
				require.Equal(t, int32(42), resp.Msg.GetNumber())
			}
		})
	}
}

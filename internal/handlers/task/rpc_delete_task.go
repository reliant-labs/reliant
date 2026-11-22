package task

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// DeleteTask deletes a task
func (s *Service) DeleteTask(
	ctx context.Context,
	req *connect.Request[reliantv1.DeleteTaskRequest],
) (*connect.Response[reliantv1.DeleteTaskResponse], error) {
	userID := auth.MustGetUserID(ctx)

	var chat *core.ChatRow
	// Read-check-write: the authorizing reads and the delete retry as one unit.
	err := s.deps.Tx.RunTx(ctx, orm.Serializable, func(ctx context.Context) error {
		var err error
		if _, chat, err = s.writableTask(ctx, userID, req.Msg.TaskId); err != nil {
			return err
		}
		if err := s.deps.Tasks.RemoveTask(ctx, req.Msg.TaskId); err != nil {
			return svcerr.WrapOr(err, svcerr.Internal("failed to delete task"))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.emitPlanTasksRefetch(ctx, chat.ID)

	return connect.NewResponse(&reliantv1.DeleteTaskResponse{Success: true}), nil
}

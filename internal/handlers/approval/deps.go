// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
)

// Repository is the slice of *db.Repo the approval handlers use.
type Repository interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType pb.ChatUpdateType, entityID string, data string) error

	GetApproval(ctx context.Context, id string) (*db.Approval, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error

	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
}

// Signaler signals a workflow run, resetting an interrupted one so the
// signal still lands. *workflow.PauseService implements it.
type Signaler interface {
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}, inputUpdate map[string]interface{}) error
}

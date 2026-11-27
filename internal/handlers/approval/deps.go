// forge:deps — consumer-declared interfaces for this package's Deps.
// The interfaces below are this package's own declarations of what it
// needs from its Deps; mock_gen.go mocks them. *db.Repo satisfies Repository.
package approval

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Repository is the slice of *db.Repo the approval handlers use.
type Repository interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	CreateChatUpdate(ctx context.Context, chatID string, updateType pb.ChatUpdateType, entityID string, data string) error

	GetApproval(ctx context.Context, id string) (*db.Approval, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error

	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	CreateNumberedMessage(ctx context.Context, msg *db.Message) error
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
}

// Chats reads the chat an approval belongs to, so the caller's right to act on
// it can be judged. store.ChatExtStore satisfies it.
type Chats interface {
	GetChatRow(ctx context.Context, id string) (*core.ChatRow, error)
}

// Signaler signals a workflow run, resetting an interrupted one so the
// signal still lands. *workflow.PauseService implements it.
type Signaler interface {
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}, inputUpdate map[string]interface{}) error
}

// ProposalConnections reads the connection a person made to approve a
// connection proposal, as they see it: their own (Get), or an organization's
// they belong to (GetOrg). *connections.Service implements it.
type ProposalConnections interface {
	Get(ctx context.Context, userID, id string) (*core.Connection, error)
	GetOrg(ctx context.Context, userID, orgID, id string) (*core.Connection, error)
}

// CapabilityGrants writes the pipeline capability grants a capability-grant
// card asks for, once its owner approves it. *grants.Service implements it.
type CapabilityGrants interface {
	GrantApproved(ctx context.Context, approverID string, card *core.Approval) ([]*core.PipelineCapabilityGrant, error)
}

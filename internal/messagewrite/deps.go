// forge:deps — consumer-declared interfaces for this package's Deps.
package messagewrite

import (
	"context"

	"github.com/reliant-labs/forge/pkg/orm"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Tx retains the repository transaction's isolation and retry classification.
type Tx interface {
	RunTx(context.Context, orm.TxPolicy, func(context.Context) error) error
}

// Order allocates the thread ordinal and chat sequence.
type Order interface {
	AllocateMessageOrder(context.Context, string, string) (int64, int64, error)
}

// Messages stores message rows.
type Messages interface {
	InsertMessage(context.Context, *core.Message) error
	InsertMessageOnce(context.Context, *core.Message) (bool, error)
	FindMessage(context.Context, string) (*core.Message, error)
}

// Windows reads and creates context windows.
type Windows interface {
	GetMaxSequenceForThread(context.Context, string) (int, error)
	GetContextWindow(context.Context, string) (*core.ContextWindow, error)
	InsertContextWindow(context.Context, *core.ContextWindow) (*core.ContextWindow, error)
}

// Threads reads thread lineage and sender titles.
type Threads interface {
	GetThreadWithParent(context.Context, string) (*core.Thread, *string, error)
	SenderTitlesByThreadIDs(context.Context, []string) (map[string]string, error)
}

// Workflows finds the workflow associated with a thread.
type Workflows interface {
	FindWorkflowByThread(context.Context, string, string) (*core.Workflow, error)
}

// Blocks writes message content blocks.
type Blocks interface {
	InsertContentBlock(context.Context, *core.MessageContentBlock) error
}

// Attachments reads attachment metadata.
type Attachments interface {
	GetAttachment(context.Context, string) (*core.Attachment, error)
	GetAttachmentsByIDs(context.Context, []string) ([]*core.Attachment, error)
}

// Updates appends and publishes the chat update with the transaction context.
type Updates interface {
	CreateChatUpdate(context.Context, string, pb.ChatUpdateType, string, string) error
}

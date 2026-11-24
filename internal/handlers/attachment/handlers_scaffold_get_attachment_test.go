package attachment_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"

	attachment "github.com/reliant-labs/reliant/internal/handlers/attachment"
)

// getForeignRepo holds one attachment, owned by another user.
type getForeignRepo struct{ attachment.AttachmentStore }

func (getForeignRepo) GetAttachment(_ context.Context, id string) (*core.Attachment, error) {
	if id != "att-foreign" {
		return nil, nil
	}
	return &core.Attachment{ID: id, UserID: "someone-else", Filename: "a.png", Content: []byte("x")}, nil
}

// AttachedInChatOwnedBy: the foreign attachment is in no chat of the caller's.
func (getForeignRepo) AttachedInChatOwnedBy(context.Context, string, string) (bool, error) {
	return false, nil
}

func TestGetAttachment_Generated(t *testing.T) {
	t.Parallel()

	svc := attachment.NewTestAttachment(t, attachment.WithAttachmentDeps(attachment.Deps{Attachments: getForeignRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.GetAttachmentRequest, pb.GetAttachmentResponse]{
		{
			Name:    "attachment_id is required",
			Req:     connect.NewRequest(&pb.GetAttachmentRequest{}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown attachment is not found",
			Req:     connect.NewRequest(&pb.GetAttachmentRequest{AttachmentId: "att-missing"}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's attachment is not found",
			Req:     connect.NewRequest(&pb.GetAttachmentRequest{AttachmentId: "att-foreign"}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.GetAttachment)
}

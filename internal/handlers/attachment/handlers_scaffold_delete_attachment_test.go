package attachment_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"

	attachment "github.com/reliant-labs/reliant/internal/handlers/attachment"
)

// deleteForeignRepo holds one attachment, owned by another user; deleting
// anything fails the test.
type deleteForeignRepo struct{ attachment.Repository }

func (deleteForeignRepo) GetAttachment(_ context.Context, id string) (*db.Attachment, error) {
	if id != "att-foreign" {
		return nil, nil
	}
	return &db.Attachment{ID: id, UserID: "someone-else"}, nil
}

func TestDeleteAttachment_Generated(t *testing.T) {
	t.Parallel()

	svc := attachment.NewTestAttachment(t, attachment.WithAttachmentDeps(attachment.Deps{Repo: deleteForeignRepo{}}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.DeleteAttachmentRequest, pb.DeleteAttachmentResponse]{
		{
			Name:    "attachment_id is required",
			Req:     connect.NewRequest(&pb.DeleteAttachmentRequest{}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "unknown attachment is not found",
			Req:     connect.NewRequest(&pb.DeleteAttachmentRequest{AttachmentId: "att-missing"}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
		{
			Name:    "another user's attachment is not found, and is kept",
			Req:     connect.NewRequest(&pb.DeleteAttachmentRequest{AttachmentId: "att-foreign"}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeNotFound,
		},
	}, svc.DeleteAttachment)
}

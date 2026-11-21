package attachment

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/crud"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/store"
)

// ownedBy scopes a query to the caller's attachments. An attachment is
// personal: sharing a chat shares none of its owner's files, so another
// user's row is simply absent.
//
// TODO(forge#2461): replace with a `forge:owner user_id` declaration once
// forge's CRUD ownership lands.
func ownedBy(userID string) orm.QueryOption {
	return orm.WhereEq(store.AttachmentFieldUserID, userID)
}

// GetAttachment retrieves one of the caller's attachments, with its bytes.
func (s *Service) GetAttachment(ctx context.Context, req *connect.Request[pb.GetAttachmentRequest]) (*connect.Response[pb.GetAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudGetAttachmentOp()

	fetch := op.Fetch
	op.Fetch = func(ctx context.Context, id string, opts ...orm.QueryOption) (*store.Attachment, error) {
		if id == "" {
			return nil, svcerr.InvalidArgument("id is required")
		}
		return fetch(ctx, id, append(opts, ownedBy(userID))...)
	}
	// The bytes ride beside the metadata: the entity carries them, the
	// Attachment message does not.
	pack := op.Pack
	op.Pack = func(e *store.Attachment) (*pb.GetAttachmentResponse, error) {
		resp, err := pack(e)
		if err != nil {
			return nil, err
		}
		resp.Content = e.Content
		return resp, nil
	}
	return crud.HandleGet(op)(ctx, req)
}

// DeleteAttachment removes one of the caller's attachments.
func (s *Service) DeleteAttachment(ctx context.Context, req *connect.Request[pb.DeleteAttachmentRequest]) (*connect.Response[pb.DeleteAttachmentResponse], error) {
	userID := auth.MustGetUserID(ctx)
	op := s.crudDeleteAttachmentOp()

	persist := op.Persist
	op.Persist = func(ctx context.Context, id string, opts ...orm.QueryOption) error {
		if id == "" {
			return svcerr.InvalidArgument("id is required")
		}
		return persist(ctx, id, append(opts, ownedBy(userID))...)
	}
	op.Pack = func() *pb.DeleteAttachmentResponse {
		return &pb.DeleteAttachmentResponse{Message: "Attachment deleted successfully"}
	}
	return crud.HandleDelete(op)(ctx, req)
}

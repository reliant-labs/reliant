package attachment_test

import (
	"testing"

	"connectrpc.com/connect/v2"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	attachment "github.com/reliant-labs/reliant/internal/handlers/attachment"
	"github.com/reliant-labs/reliant/pkg/config"
)

// No row reaches Repo: every one is refused first.
func TestUploadAttachment_Generated(t *testing.T) {
	t.Parallel()

	svc := attachment.NewTestAttachment(t, attachment.WithAttachmentDeps(attachment.Deps{API: &config.ApiServerConfig{MaxUploadSize: 4}}))

	tdd.RunRPCCasesV2(t, []tdd.RPCCaseV2[pb.UploadAttachmentRequest, pb.UploadAttachmentResponse]{
		{
			Name:    "filename is required",
			Req:     &pb.UploadAttachmentRequest{Content: []byte("abc")},
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "content is required",
			Req:     &pb.UploadAttachmentRequest{Filename: "a.png"},
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an unsupported file type is refused",
			Req:     &pb.UploadAttachmentRequest{Filename: "a.exe", Content: []byte("abc")},
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a file over max_upload_size is refused",
			Req:     &pb.UploadAttachmentRequest{Filename: "a.png", Content: []byte("abcde")},
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeResourceExhausted,
		},
	}, svc.UploadAttachment)
}

package attachment_test

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/tdd"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	attachment "github.com/reliant-labs/reliant/internal/handlers/attachment"
)

// No row reaches Repo: every one is refused first.
func TestCreateFileReference_Generated(t *testing.T) {
	t.Parallel()

	svc := attachment.NewTestAttachment(t, attachment.WithAttachmentDeps(attachment.Deps{MaxFileSize: 4}))

	tdd.RunRPCCases(t, []tdd.RPCCase[pb.CreateFileReferenceRequest, pb.CreateFileReferenceResponse]{
		{
			Name:    "file_path is required",
			Req:     connect.NewRequest(&pb.CreateFileReferenceRequest{Content: []byte("abc")}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "content is required",
			Req:     connect.NewRequest(&pb.CreateFileReferenceRequest{FilePath: "/w/main.go"}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "an image goes through UploadAttachment",
			Req:     connect.NewRequest(&pb.CreateFileReferenceRequest{FilePath: "/w/a.png", Content: []byte("abc")}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeInvalidArgument,
		},
		{
			Name:    "a file over MaxFileSize is refused",
			Req:     connect.NewRequest(&pb.CreateFileReferenceRequest{FilePath: "/w/main.go", Content: []byte("abcde")}),
			Ctx:     attachment.AuthedContext(t),
			WantErr: connect.CodeResourceExhausted,
		},
	}, svc.CreateFileReference)
}

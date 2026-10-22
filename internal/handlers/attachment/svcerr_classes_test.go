// Copyright (c) 2025 Reliant Labs
package attachment_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	attachment "github.com/reliant-labs/reliant/internal/handlers/attachment"
)

// How a store read can fail, and the code each reaches a client with, over a
// real Connect client and the svcerr interceptor. A miss the store classified
// keeps its code; a database outage is Internal with the rpc's own words, and
// its text never reaches the wire.
func TestAttachment_StoreFailuresOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want connect.Code
	}{
		{"store not found", svcerr.NotFound("attachment"), connect.CodeNotFound},
		{"deadline", fmt.Errorf("query: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded},
		{"canceled", fmt.Errorf("query: %w", context.Canceled), connect.CodeCanceled},
		{"opaque", errors.New("dial tcp: dsn=postgres://u:secret@h"), connect.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &attachment.MockRepository{
				GetAttachmentFunc: func(context.Context, string) (*db.Attachment, error) { return nil, tc.err },
			}
			_, client := attachment.NewTestAttachmentServer(t, attachment.WithAttachmentDeps(attachment.Deps{Repo: repo}))
			_, err := client.GetAttachment(context.Background(), connect.NewRequest(&pb.GetAttachmentRequest{AttachmentId: "a1"}))
			assertWire(t, err, tc.want)
			_, err = client.DeleteAttachment(context.Background(), connect.NewRequest(&pb.DeleteAttachmentRequest{AttachmentId: "a1"}))
			assertWire(t, err, tc.want)
		})
	}
}

func assertWire(t *testing.T, err error, want connect.Code) {
	t.Helper()
	var cerr *connect.Error
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, want, cerr.Code(), "message: %s", cerr.Message())
	assert.NotContains(t, cerr.Message(), "secret", "an opaque error's text never reaches the client")
}

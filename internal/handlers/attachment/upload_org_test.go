package attachment

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
)

// memberOf is an OrgResolver for a caller who is a live member of one org:
// any other org is PermissionDenied, as orgs.Resolver.Named answers a
// non-member.
type memberOf string

func (m memberOf) Named(_ context.Context, _, requested string) (string, error) {
	if requested == "" {
		return "", nil
	}
	if requested != string(m) {
		return "", svcerr.PermissionDenied("not a member")
	}
	return requested, nil
}

// TestUploadsRecordTheirOrg proves, on a real database, that both upload rpcs
// stamp the org the request names once the caller is a member of it, stamp
// none when it names none, and refuse (NotFound, nothing stored) an org the
// caller is not a member of.
func TestUploadsRecordTheirOrg(t *testing.T) {
	repo, raw, cleanup := db.SetupTestDBWithRawDB(t)
	t.Cleanup(cleanup)
	deps := RealDeps(repo)
	deps.Orgs = memberOf("org-a")
	svc := NewTestAttachment(t, WithAttachmentDeps(deps))
	ctx := auth.WithUser(context.Background(), "uploader", "user", "")

	upload := func(org *string) (string, error) {
		resp, err := svc.UploadAttachment(ctx, connect.NewRequest(&pb.UploadAttachmentRequest{
			Filename: "a.png", Content: []byte{1, 2, 3}, MimeType: "image/png", OrgId: org,
		}))
		if err != nil {
			return "", err
		}
		return resp.Msg.GetAttachment().GetId(), nil
	}
	reference := func(org *string) (string, error) {
		resp, err := svc.CreateFileReference(ctx, connect.NewRequest(&pb.CreateFileReferenceRequest{
			FilePath: "/w/notes.md", Content: []byte("# notes"), OrgId: org,
		}))
		if err != nil {
			return "", err
		}
		return resp.Msg.GetAttachment().GetId(), nil
	}
	storedOrg := func(t *testing.T, id string) *string {
		t.Helper()
		a, err := repo.GetAttachment(context.Background(), id)
		require.NoError(t, err)
		require.NotNil(t, a)
		return a.OrgID
	}
	count := func(t *testing.T) int {
		t.Helper()
		var n int
		require.NoError(t, raw.QueryRow(`SELECT count(*) FROM attachments WHERE user_id = 'uploader'`).Scan(&n))
		return n
	}

	for name, create := range map[string]func(*string) (string, error){"UploadAttachment": upload, "CreateFileReference": reference} {
		t.Run(name, func(t *testing.T) {
			id, err := create(ptr.Of("org-a"))
			require.NoError(t, err)
			assert.Equal(t, ptr.Of("org-a"), storedOrg(t, id), "a member's org is recorded")

			id, err = create(nil)
			require.NoError(t, err)
			assert.Nil(t, storedOrg(t, id), "no org named records none")

			id, err = create(ptr.Of("  "))
			require.NoError(t, err)
			assert.Nil(t, storedOrg(t, id), "a blank org names none")

			before := count(t)
			_, err = create(ptr.Of("org-b"))
			require.Error(t, err)
			assert.Equal(t, connect.CodeNotFound, svcerr.ToConnect(err).Code(), "another org is NotFound, never PermissionDenied")
			assert.Equal(t, before, count(t), "a refused upload stores nothing")
		})
	}
}

// controlPlaneDown is an OrgResolver whose membership lookup is unavailable,
// as orgs.Resolver.Named answers when the control plane cannot be reached.
type controlPlaneDown struct{}

func (controlPlaneDown) Named(context.Context, string, string) (string, error) {
	return "", svcerr.Unavailable("could not verify the organization; try again")
}

// TestUploadsStampNoneWhenMembershipIsUnavailable: an upload does not depend
// on the control plane. When membership cannot be verified it is stored, with
// no org stamped, so no org-scoped read can take it.
func TestUploadsStampNoneWhenMembershipIsUnavailable(t *testing.T) {
	var stored []*db.Attachment
	svc := NewTestAttachment(t, WithAttachmentDeps(Deps{
		Attachments: &MockAttachmentStore{InsertAttachmentFunc: func(_ context.Context, a *db.Attachment) error {
			stored = append(stored, a)
			return nil
		}},
		Orgs: controlPlaneDown{},
	}))
	ctx := auth.WithUser(context.Background(), "uploader", "user", "")

	_, err := svc.UploadAttachment(ctx, connect.NewRequest(&pb.UploadAttachmentRequest{
		Filename: "a.png", Content: []byte{1}, MimeType: "image/png", OrgId: ptr.Of("org-a"),
	}))
	require.NoError(t, err)
	_, err = svc.CreateFileReference(ctx, connect.NewRequest(&pb.CreateFileReferenceRequest{
		FilePath: "/w/notes.md", Content: []byte("# notes"), OrgId: ptr.Of("org-a"),
	}))
	require.NoError(t, err)

	require.Len(t, stored, 2, "both are stored")
	for _, a := range stored {
		assert.Nil(t, a.OrgID, "an org the server could not verify is never stamped")
	}
}

// TestUploadsWithoutOrgsRecordNone: a deployment with no orgs (no resolver)
// records none, whatever the request names.
func TestUploadsWithoutOrgsRecordNone(t *testing.T) {
	var stored *db.Attachment
	svc := NewTestAttachment(t, WithAttachmentDeps(Deps{Attachments: &MockAttachmentStore{
		InsertAttachmentFunc: func(_ context.Context, a *db.Attachment) error { stored = a; return nil },
	}}))
	ctx := auth.WithUser(context.Background(), "uploader", "user", "")
	_, err := svc.UploadAttachment(ctx, connect.NewRequest(&pb.UploadAttachmentRequest{
		Filename: "a.png", Content: []byte{1}, MimeType: "image/png", OrgId: ptr.Of("org-a"),
	}))
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Nil(t, stored.OrgID)
}

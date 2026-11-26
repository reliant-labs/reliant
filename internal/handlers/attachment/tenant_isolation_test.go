package attachment

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// Tenant isolation for AttachmentService, on a real database. An attachment is
// personal (authz.CanAccessPersonal): only its owner reads or deletes it, and
// sharing a chat shares none of its owner's files, so the viewer, an admin of
// the owner's org and another tenant all see the same absent row. Codes and
// row effects only, never message text.
func TestAttachmentTenantIsolation(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestAttachment(t, WithAttachmentDeps(RealDeps(repo)))

	// An attachment of its own per case, so the delete cases do not change
	// what a later case reads.
	seed := func(t *testing.T, f *viewertest.Fixture) string {
		t.Helper()
		now := time.Now().UTC().Truncate(time.Second)
		id := "att-" + uuid.NewString()[:8]
		require.NoError(t, f.Repo.CreateAttachment(f.Owner, &db.Attachment{
			ID: id, UserID: f.OwnerID, Filename: "a.png", Size: 3, MimeType: "image/png",
			AttachmentType: "image", Content: []byte{1, 2, 3}, CreatedAt: now, UpdatedAt: now,
		}))
		return id
	}
	exists := func(t *testing.T, f *viewertest.Fixture, id string) bool {
		t.Helper()
		a, err := f.Repo.GetAttachment(context.Background(), id)
		return err == nil && a != nil
	}

	var cases []viewertest.IsolationCase
	add := func(name, rpc string, as viewertest.Who, want []connect.Code, build func(id string) proto.Message, after func(t *testing.T, f *viewertest.Fixture, id string, err error)) {
		var id string
		c := viewertest.IsolationCase{
			Name: rpc + "/" + name, Rpc: rpc, As: as, Want: want,
			Request: func(t *testing.T, f *viewertest.Fixture) proto.Message {
				id = seed(t, f)
				return build(id)
			},
		}
		if after != nil {
			c.After = func(t *testing.T, f *viewertest.Fixture, err error) { after(t, f, id, err) }
		}
		cases = append(cases, c)
	}
	ok := viewertest.Code(viewertest.OK)
	// Another user's attachment already answers NotFound, which is what a
	// converted rpc answers too: nothing to tighten.
	absent := viewertest.Code(connect.CodeNotFound)
	get := func(id string) proto.Message { return &pb.GetAttachmentRequest{Id: id} }
	del := func(id string) proto.Message { return &pb.DeleteAttachmentRequest{Id: id} }
	kept := func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.Error(t, err)
		require.True(t, exists(t, f, id), "a refused delete must leave the attachment")
	}

	add("owner", "GetAttachment", viewertest.AsOwner, ok, get, nil)
	for _, who := range []viewertest.Who{viewertest.AsViewer, viewertest.AsAdmin, viewertest.AsOtherTenant} {
		add(string(who), "GetAttachment", who, absent, get, nil)
		add(string(who), "DeleteAttachment", who, absent, del, kept)
	}
	add("owner", "DeleteAttachment", viewertest.AsOwner, ok, del, func(t *testing.T, f *viewertest.Fixture, id string, err error) {
		require.NoError(t, err)
		require.False(t, exists(t, f, id), "the owner's delete removes the attachment")
	})

	for _, rpc := range []struct {
		name  string
		empty proto.Message
		gone  proto.Message
	}{
		{"GetAttachment", &pb.GetAttachmentRequest{}, &pb.GetAttachmentRequest{Id: "absent"}},
		{"DeleteAttachment", &pb.DeleteAttachmentRequest{}, &pb.DeleteAttachmentRequest{Id: "absent"}},
	} {
		rpc := rpc
		for _, who := range []viewertest.Who{viewertest.AsOwner, viewertest.AsOtherTenant} {
			cases = append(cases,
				viewertest.IsolationCase{Name: rpc.name + "/absent/" + string(who), Rpc: rpc.name, As: who, Want: absent,
					Request: func(*testing.T, *viewertest.Fixture) proto.Message { return proto.Clone(rpc.gone) }},
				viewertest.IsolationCase{Name: rpc.name + "/invalid/" + string(who), Rpc: rpc.name, As: who, Want: viewertest.Code(connect.CodeInvalidArgument),
					Request: func(*testing.T, *viewertest.Fixture) proto.Message { return proto.Clone(rpc.empty) }},
			)
		}
	}

	// Writes are the caller's own: whoever uploads owns the row, so there is no
	// foreign row to refuse. Validation still answers the same for everyone.
	for _, who := range []viewertest.Who{viewertest.AsOwner, viewertest.AsOtherTenant} {
		cases = append(cases,
			viewertest.IsolationCase{Name: "UploadAttachment/invalid/" + string(who), Rpc: "UploadAttachment", As: who, Want: viewertest.Code(connect.CodeInvalidArgument),
				Request: func(*testing.T, *viewertest.Fixture) proto.Message {
					return &pb.UploadAttachmentRequest{Filename: "a.png"}
				}},
			viewertest.IsolationCase{Name: "CreateFileReference/invalid/" + string(who), Rpc: "CreateFileReference", As: who, Want: viewertest.Code(connect.CodeInvalidArgument),
				Request: func(*testing.T, *viewertest.Fixture) proto.Message { return &pb.CreateFileReferenceRequest{} }},
		)
	}
	cases = append(cases, viewertest.IsolationCase{Name: "UploadAttachment/owner", Rpc: "UploadAttachment", As: viewertest.AsOwner, Want: ok,
		Request: func(*testing.T, *viewertest.Fixture) proto.Message {
			return &pb.UploadAttachmentRequest{Filename: "up.png", Content: []byte{1, 2, 3}}
		}})

	viewertest.RunIsolation(t, f, svc, cases)
}

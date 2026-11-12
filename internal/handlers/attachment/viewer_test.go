// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// A viewer of a shared chat never reads its owner's attachments (control-plane
// docs/design/org-sharing.md §4): an attachment is personal
// (authz.CanAccessPersonal), not a part of the chat, so sharing the chat does
// not share the files its transcript names. Both doors answer NotFound, as
// for any foreign attachment: GetAttachment and the content route.
func TestViewerCannotReadTheOwnersAttachment(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	now := time.Now().UTC()
	att := &db.Attachment{
		ID: "att-" + f.ChatID, UserID: f.OwnerID, Filename: "screenshot.png", Size: 3, MimeType: "image/png",
		AttachmentType: "image", Content: []byte{1, 2, 3}, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, repo.CreateAttachment(f.Owner, att))

	svc := NewTestAttachment(t, WithAttachmentDeps(Deps{Repo: repo}))
	req := connect.NewRequest(&reliantv1.GetAttachmentRequest{AttachmentId: att.ID})

	got, err := svc.GetAttachment(f.Owner, req)
	require.NoError(t, err, "the owner reads their own attachment")
	assert.Equal(t, att.Content, got.Msg.GetContent())

	for name, ctx := range map[string]context.Context{"viewer": f.Viewer, "admin": f.Admin, "outsider": f.Outsider} {
		_, err := svc.GetAttachment(ctx, req)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "%s must not read the owner's attachment: %v", name, err)
	}

	content := func(userID string) int {
		h := AttachmentContentHandler(repo, func(*http.Request) (string, error) { return userID, nil })
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attachments/"+att.ID+"/content", nil))
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, content(f.OwnerID))
	assert.Equal(t, http.StatusNotFound, content(f.ViewerID), "a viewer's content fetch looks absent")
	assert.Equal(t, http.StatusNotFound, content(f.AdminID), "an admin's content fetch looks absent")
}

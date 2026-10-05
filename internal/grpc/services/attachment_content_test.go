// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
)

// contentRepo embeds the Repository interface so only GetAttachment needs a body.
type contentRepo struct {
	db.Repository
	atts map[string]*db.Attachment
}

func (r *contentRepo) GetAttachment(_ context.Context, id string) (*db.Attachment, error) {
	return r.atts[id], nil
}

func newContentHandler(t *testing.T, userID string) (http.Handler, []byte) {
	t.Helper()
	body := make([]byte, 1000)
	for i := range body {
		body[i] = byte(i % 251)
	}
	repo := &contentRepo{atts: map[string]*db.Attachment{
		"vid-1": {ID: "vid-1", UserID: "owner", Filename: "generated-1.mp4", MimeType: "video/mp4", Content: body, UpdatedAt: time.Unix(1700000000, 0)},
	}}
	auth := func(r *http.Request) (string, error) {
		if userID == "" {
			return "", errors.New("no creds")
		}
		return userID, nil
	}
	return AttachmentContentHandler(repo, auth), body
}

func TestAttachmentContent_FullBodyAndRange(t *testing.T) {
	h, body := newContentHandler(t, "owner")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attachments/vid-1/content", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	assert.Equal(t, "bytes", rec.Header().Get("Accept-Ranges"))
	assert.Equal(t, body, rec.Body.Bytes())

	req := httptest.NewRequest(http.MethodGet, "/api/attachments/vid-1/content", nil)
	req.Header.Set("Range", "bytes=100-199")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusPartialContent, rec.Code)
	assert.Equal(t, "bytes 100-199/1000", rec.Header().Get("Content-Range"))
	assert.Equal(t, body[100:200], rec.Body.Bytes())

	req = httptest.NewRequest(http.MethodGet, "/api/attachments/vid-1/content", nil)
	req.Header.Set("Range", "bytes=2000-")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, rec.Code)
}

func TestAttachmentContent_AuthAndOwnership(t *testing.T) {
	h, _ := newContentHandler(t, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attachments/vid-1/content", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	h, _ = newContentHandler(t, "someone-else")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attachments/vid-1/content", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "foreign attachment must look absent, not forbidden")

	h, _ = newContentHandler(t, "owner")
	for _, path := range []string{"/api/attachments/missing/content", "/api/attachments/vid-1", "/api/attachments//content", "/api/attachments/a/b/content"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/attachments/vid-1/content", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

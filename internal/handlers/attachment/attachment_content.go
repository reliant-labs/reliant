// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// AttachmentContentPathPrefix is where AttachmentContentHandler is mounted.
// The full route is /api/attachments/{id}/content.
const AttachmentContentPathPrefix = "/api/attachments/"

// AttachmentContentHandler serves an attachment's bytes over plain HTTP with
// Range support, so a <video> element can seek and stream without the browser
// buffering the whole clip in JS (the gRPC GetAttachment path is whole-blob).
//
// authenticate resolves the caller's user id from the request. An attachment
// owned by someone else is reported as 404, never 403, so ids cannot be probed.
func AttachmentContentHandler(repo ContentReader, authenticate func(*http.Request) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id, ok := parseAttachmentContentPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		userID, err := authenticate(r)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		att, err := repo.GetAttachment(r.Context(), id)
		if err != nil || att == nil || !canRead(r.Context(), repo, userID, att) || att.Content == nil {
			http.NotFound(w, r)
			return
		}
		mimeType := att.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		w.Header().Set("Content-Type", mimeType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// ServeContent owns Range, If-Range, 206/416 and Content-Length.
		http.ServeContent(w, r, att.Filename, att.UpdatedAt.Truncate(time.Second), bytes.NewReader(att.Content))
	})
}

// parseAttachmentContentPath extracts {id} from /api/attachments/{id}/content.
func parseAttachmentContentPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, AttachmentContentPathPrefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, "/content")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// canRead reports whether userID may read att: its uploader, or the owner of
// a chat a member attached it to their own message in (control-plane
// docs/design/chat-collaboration.md §6.4), whose agent and money it was sent
// to. Never another member of that chat, and never a viewer. Deleting stays
// the uploader's alone. A lookup that fails refuses.
func canRead(ctx context.Context, r ContentReader, userID string, att *core.Attachment) bool {
	if authz.CanAccessPersonal(userID, att.UserID) {
		return true
	}
	ok, err := r.AttachedInChatOwnedBy(ctx, att.ID, userID)
	return err == nil && ok
}

// Copyright (c) 2025 Reliant Labs
package services

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db"
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
func AttachmentContentHandler(repo db.Repository, authenticate func(*http.Request) (string, error)) http.Handler {
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
		if err != nil || att == nil || att.UserID != userID || att.Content == nil {
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

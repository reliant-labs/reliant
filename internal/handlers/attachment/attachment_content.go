// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"bytes"
	"encoding/binary"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/authz"
)

// AttachmentContentPathPrefix is where AttachmentContentHandler is mounted.
// The full route is /api/attachments/{id}/content.
const AttachmentContentPathPrefix = "/api/attachments/"

// AttachmentContentHandler serves an attachment's bytes over plain HTTP with
// Range support, so a <video> element can seek and stream without the browser
// buffering the whole clip in JS (the gRPC GetAttachment path is whole-blob).
//
// It is served on the API origin, so it never renders an upload that could
// run script there: only bytes that sniff as a raster image, mp4, webm or PDF
// are inline, and everything else is an application/octet-stream download.
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
		if err != nil || att == nil || !authz.CanAccessPersonal(userID, att.UserID) || att.Content == nil {
			http.NotFound(w, r)
			return
		}
		// The stored type is whatever the uploader claimed, so it decides
		// nothing: an upload named .png and labelled image/png can be HTML.
		// The bytes are sniffed, and only a type that cannot run script is
		// rendered inline, under the type the bytes are.
		contentType, disposition := "application/octet-stream", "attachment"
		if sniffed := sniffContentType(att.Content); inlineSafeTypes[sniffed] {
			contentType, disposition = sniffed, "inline"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", contentDisposition(disposition, att.Filename))
		w.Header().Set("Cache-Control", "private, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Opened as a document, the response runs nothing and loads nothing.
		// A subresource (<img>, <video>) ignores it, and Chrome's PDF viewer
		// still renders under it.
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		// ServeContent owns Range, If-Range, 206/416 and Content-Length.
		http.ServeContent(w, r, att.Filename, att.UpdatedAt.Truncate(time.Second), bytes.NewReader(att.Content))
	})
}

// inlineSafeTypes are the sniffed types served inline: raster images, video
// and PDF, none of which a browser runs as script. Every other type,
// HTML, SVG and XML among them, is sent as a download.
var inlineSafeTypes = map[string]bool{
	"image/png":       true,
	"image/jpeg":      true,
	"image/gif":       true,
	"image/webp":      true,
	"video/mp4":       true,
	"video/webm":      true,
	"application/pdf": true,
}

// sniffContentType extends Go's sniffer only for known QuickTime and fragmented
// MP4 major brands. Unknown ISO base media formats must remain downloads, not
// become inline video merely because they contain an ftyp box.
func sniffContentType(data []byte) string {
	sniffed := http.DetectContentType(data)
	if sniffed != "application/octet-stream" || len(data) < 16 || string(data[4:8]) != "ftyp" {
		return sniffed
	}
	// Require a complete ordinary ftyp box: header, major brand, minor version,
	// then zero or more four-byte compatible brands. Extended sizes fail closed.
	size := binary.BigEndian.Uint32(data[:4])
	if size < 16 || uint64(size) > uint64(len(data)) || size%4 != 0 {
		return sniffed
	}
	switch string(data[8:12]) {
	case "qt  ", "iso5", "iso6", "dash":
		return "video/mp4"
	default:
		return sniffed
	}
}

// contentDisposition is the Content-Disposition header for disposition and
// the attachment's filename. A name that is not plain ASCII is sent twice:
// RFC 2231-encoded (filename*), and as an ASCII stand-in (filename) for a
// client that reads only that. A name that cannot be encoded is dropped
// rather than sent raw.
func contentDisposition(disposition, filename string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, filename)
	if name == "" {
		return disposition
	}
	v := mime.FormatMediaType(disposition, map[string]string{"filename": name})
	if v == "" {
		return disposition
	}
	if rest, encoded := strings.CutPrefix(v, disposition+"; filename*="); encoded {
		fallback := strings.Map(func(r rune) rune {
			if r > 0x7e || r == '"' {
				return '_'
			}
			return r
		}, name)
		return disposition + `; filename="` + fallback + `"; filename*=` + rest
	}
	return v
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

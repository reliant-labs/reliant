// Copyright (c) 2025 Reliant Labs
package attachment

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func newContentHandler(t *testing.T, userID string) (http.Handler, []byte) {
	t.Helper()
	// An ISO BMFF ftyp box, so the bytes sniff as video/mp4.
	body := make([]byte, 1000)
	ftyp := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isommp41")
	copy(body, ftyp)
	for i := len(ftyp); i < len(body); i++ {
		body[i] = byte(i % 251)
	}
	atts := map[string]*core.Attachment{
		"vid-1": {ID: "vid-1", UserID: "owner", Filename: "generated-1.mp4", MimeType: "video/mp4", Content: body, UpdatedAt: time.Unix(1700000000, 0)},
	}
	for id, att := range contentFixtures {
		atts[id] = att
	}
	// Only GetAttachment is programmed: the content handler reads, never writes.
	repo := &MockContentReader{GetAttachmentFunc: func(_ context.Context, id string) (*core.Attachment, error) {
		return atts[id], nil
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

// pngHeader is enough of a PNG for the bytes to sniff as image/png.
const pngHeader = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00"

const htmlPayload = "<!DOCTYPE html><html><body><script>alert(document.domain)</script></body></html>"

// contentFixtures are attachments whose stored type, filename and bytes
// disagree in the ways an upload can make them disagree.
var contentFixtures = map[string]*core.Attachment{
	"html-1": {ID: "html-1", UserID: "owner", Filename: "page.html", MimeType: "text/html", Content: []byte(htmlPayload)},
	"svg-1": {ID: "svg-1", UserID: "owner", Filename: "logo.svg", MimeType: "image/svg+xml",
		Content: []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" onload="alert(document.domain)"><script>alert(1)</script></svg>`)},
	"png-1": {ID: "png-1", UserID: "owner", Filename: "cat.png", MimeType: "image/png", Content: []byte(pngHeader + "rest-of-image")},
	// Claimed image/png and named .png; the bytes are HTML.
	"fake-png-1": {ID: "fake-png-1", UserID: "owner", Filename: "cat.png", MimeType: "image/png", Content: []byte(htmlPayload)},
	// Claimed text/plain; the bytes are a PDF.
	"pdf-1": {ID: "pdf-1", UserID: "owner", Filename: "résumé.pdf", MimeType: "text/plain", Content: []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<<>>\nendobj\n")},
	// QuickTime (major brand "qt  ") and fragmented mp4 (iso5, dash): Go's
	// sniffer lists no "mp4" brand in either, so it reports octet-stream.
	"mov-1":  {ID: "mov-1", UserID: "owner", Filename: "generated-2.mov", MimeType: "video/quicktime", Content: []byte("\x00\x00\x00\x14ftypqt  \x00\x00\x02\x00qt  \x00\x00\x00\x08wide")},
	"fmp4-1": {ID: "fmp4-1", UserID: "owner", Filename: "clip.mp4", MimeType: "video/mp4", Content: []byte("\x00\x00\x00\x18ftypiso5\x00\x00\x02\x00iso5dash\x00\x00\x00\x08free")},
	// AVIF shares the ISO box but is an image: not served as video.
	"avif-1": {ID: "avif-1", UserID: "owner", Filename: "pic.avif", MimeType: "image/avif", Content: []byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1miaf\x00\x00\x00\x08free")},
	"crlf-1": {ID: "crlf-1", UserID: "owner", Filename: "a\r\nX-Injected: 1/../b.html", MimeType: "text/html", Content: []byte(htmlPayload)},
}

func getContent(t *testing.T, h http.Handler, id string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/attachments/"+id+"/content", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec
}

// assertLockedDown checks the headers every response carries, inline or not.
func assertLockedDown(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox; default-src 'none'", rec.Header().Get("Content-Security-Policy"))
}

func TestAttachmentContent_HTMLUploadIsADownload(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "html-1")
	assert.Equal(t, `attachment; filename=page.html`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"), "never text/html: the browser must not render it")
	assertLockedDown(t, rec)
	assert.Equal(t, htmlPayload, rec.Body.String(), "the bytes are still downloadable, unchanged")
}

func TestAttachmentContent_SVGUploadIsADownload(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "svg-1")
	assert.Equal(t, `attachment; filename=logo.svg`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"), "never image/svg+xml: SVG runs script")
	assertLockedDown(t, rec)
}

func TestAttachmentContent_ImageIsInlineUnderItsSniffedType(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "png-1")
	assert.Equal(t, `inline; filename=cat.png`, rec.Header().Get("Content-Disposition"))
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assertLockedDown(t, rec)

	// Video stays inline too, so <video> streaming keeps working.
	rec = getContent(t, h, "vid-1")
	assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"))
	assert.Equal(t, `inline; filename=generated-1.mp4`, rec.Header().Get("Content-Disposition"))
	assertLockedDown(t, rec)
}

func TestAttachmentContent_MislabelledHTMLIsADownload(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "fake-png-1")
	assert.Equal(t, `attachment; filename=cat.png`, rec.Header().Get("Content-Disposition"),
		"the claimed image/png and the .png name decide nothing; the bytes are HTML")
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assertLockedDown(t, rec)
}

func TestAttachmentContent_TypeComesFromTheBytes(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "pdf-1")
	assert.Equal(t, "application/pdf", rec.Header().Get("Content-Type"), "stored as text/plain, served as what it is")
	assert.Equal(t, `inline; filename="r_sum_.pdf"; filename*=utf-8''r%C3%A9sum%C3%A9.pdf`, rec.Header().Get("Content-Disposition"),
		"an ASCII stand-in beside the RFC 2231 name")
	assertLockedDown(t, rec)

	// HEAD answers the same headers as GET.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/api/attachments/fake-png-1/content", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Content-Disposition"), "attachment")
}

func TestAttachmentContent_FilenameCannotInjectHeaders(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	rec := getContent(t, h, "crlf-1")
	cd := rec.Header().Get("Content-Disposition")
	assert.True(t, strings.HasPrefix(cd, "attachment"), cd)
	assert.NotContains(t, cd, "\r")
	assert.NotContains(t, cd, "\n")
	assert.NotContains(t, cd, "/")
	assert.Empty(t, rec.Header().Get("X-Injected"))
}

func TestAttachmentContent_ISOBaseMediaKnownVideoBrands(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	for _, id := range []string{"mov-1", "fmp4-1"} {
		rec := getContent(t, h, id)
		assert.Equal(t, "video/mp4", rec.Header().Get("Content-Type"), id)
		assert.True(t, strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline"), id)
		assertLockedDown(t, rec)
	}

	rec := getContent(t, h, "avif-1")
	assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, `attachment; filename=pic.avif`, rec.Header().Get("Content-Disposition"))
	assertLockedDown(t, rec)
}

func TestAttachmentContent_UnknownAndMalformedISOBaseMediaDownload(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"unknown", "\x00\x00\x00\x14ftypzzzz\x00\x00\x00\x00zzzz"},
		{"document", "\x00\x00\x00\x14ftypjp2 \x00\x00\x00\x00jp2 "},
		{"audio", "\x00\x00\x00\x14ftypM4A \x00\x00\x00\x00M4A "},
		{"avif", "\x00\x00\x00\x14ftypavif\x00\x00\x00\x00avif"},
		{"heif", "\x00\x00\x00\x14ftypmif1\x00\x00\x00\x00mif1"},
		{"truncated header", "\x00\x00\x00\x14ftypqt  "},
		{"truncated box", "\x00\x00\x00\x18ftypiso6\x00\x00\x00\x00iso6"},
		{"short box", "\x00\x00\x00\x0cftypdash\x00\x00\x00\x00dash"},
		{"unaligned box", "\x00\x00\x00\x13ftypiso5\x00\x00\x00\x00iso"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &MockContentReader{GetAttachmentFunc: func(context.Context, string) (*core.Attachment, error) {
				return &core.Attachment{UserID: "owner", Filename: "clip.mp4", MimeType: "video/mp4", Content: []byte(tc.body)}, nil
			}}
			h := AttachmentContentHandler(repo, func(*http.Request) (string, error) { return "owner", nil })
			rec := getContent(t, h, "unknown")
			assert.Equal(t, "application/octet-stream", rec.Header().Get("Content-Type"))
			assert.Equal(t, `attachment; filename=clip.mp4`, rec.Header().Get("Content-Disposition"))
			assertLockedDown(t, rec)
		})
	}
}

func TestAttachmentContent_RangeKeepsTheHeaders(t *testing.T) {
	h, _ := newContentHandler(t, "owner")
	for _, tc := range []struct{ id, contentType, disposition string }{
		{"vid-1", "video/mp4", `inline; filename=generated-1.mp4`},
		{"html-1", "application/octet-stream", `attachment; filename=page.html`},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/attachments/"+tc.id+"/content", nil)
		req.Header.Set("Range", "bytes=0-9")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		require.Equal(t, http.StatusPartialContent, rec.Code, tc.id)
		assert.Equal(t, tc.contentType, rec.Header().Get("Content-Type"), tc.id)
		assert.Equal(t, tc.disposition, rec.Header().Get("Content-Disposition"), tc.id)
		assertLockedDown(t, rec)
		assert.Len(t, rec.Body.Bytes(), 10, tc.id)
	}
}

// Copyright (c) 2025 Reliant Labs

// Package gmail is the Go half of the Gmail integration
// (catalog/gmail/manifest.yaml): the go: executors for what a declarative
// request cannot express — assembling an RFC 2822 message for messages.send,
// decoding a message's MIME part tree for messages.get — and the poller that
// delivers the new-email trigger from users.history.list.
//
// Importing the package registers the executors in httpaction.Executors().
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// Executor names, as the manifest writes them after "go:".
const (
	SendExecutor = "gmail.message_send"
	GetExecutor  = "gmail.message_get"
)

func init() {
	httpaction.Executors().MustRegister(SendExecutor, send)
	httpaction.Executors().MustRegister(GetExecutor, get)
}

// sendResponse is messages.send's answer (a Message with id, threadId,
// labelIds).
type sendResponse struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	LabelIDs []string `json:"labelIds"`
}

// send is message.send: build the RFC 2822 message, base64url it, POST it.
func send(ctx context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
	p := call.Params
	msg := Message{
		To: strings2(p["to"]), Cc: strings2(p["cc"]), Bcc: strings2(p["bcc"]),
		Subject: str(p["subject"]), Text: str(p["text"]), HTML: str(p["html"]),
		InReplyTo: str(p["in_reply_to"]), References: str(p["references"]),
	}
	threadID := str(p["thread_id"])
	if msg.InReplyTo != "" && threadID == "" {
		// Gmail threads only when threadId is on the request AND the headers
		// match; without the id the reply would land as a new thread.
		return nil, fmt.Errorf("invalid params: a reply (in_reply_to) also needs thread_id")
	}
	if threadID != "" && msg.InReplyTo != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(msg.Subject)), "re:") {
		msg.Subject = "Re: " + msg.Subject
	}
	raw, err := Build(msg)
	if err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	c, err := newAPI(call.Manifest.GetConnection().GetBaseUrl(), call.Runner.Client(), call.Credential)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"raw": EncodeRaw(raw)}
	if threadID != "" {
		body["threadId"] = threadID
	}
	var out sendResponse
	if err := c.do(ctx, "POST", "/messages/send", nil, body, &out); err != nil {
		return asResult(err)
	}
	return &httpaction.Result{StatusCode: 200, Data: map[string]any{
		"id": out.ID, "thread_id": out.ThreadID, "label_ids": anyStrings(out.LabelIDs),
	}}, nil
}

// apiMessage is the slice of a Gmail Message resource this package reads.
type apiMessage struct {
	ID           string      `json:"id"`
	ThreadID     string      `json:"threadId"`
	LabelIDs     []string    `json:"labelIds"`
	Snippet      string      `json:"snippet"`
	InternalDate string      `json:"internalDate"` // ms since the epoch, as a string
	Payload      *apiPart    `json:"payload"`
	HistoryID    string      `json:"historyId"`
	SizeEstimate json.Number `json:"sizeEstimate"`
}

type apiPart struct {
	PartID   string      `json:"partId"`
	MimeType string      `json:"mimeType"`
	Filename string      `json:"filename"`
	Headers  []apiHeader `json:"headers"`
	Body     struct {
		AttachmentID string `json:"attachmentId"`
		Size         int64  `json:"size"`
		Data         string `json:"data"` // base64url
	} `json:"body"`
	Parts []*apiPart `json:"parts"`
}

type apiHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// metadataHeaders are the headers asked for with format=metadata and
// flattened into every message's output.
var metadataHeaders = []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "References"}

// get is message.get: fetch the message and flatten it.
func get(ctx context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
	id := str(call.Params["id"])
	format := str(call.Params["format"])
	if format == "" {
		format = "full"
	}
	maxChars := 20000
	if n, ok := call.Params["max_body_chars"].(float64); ok && n > 0 {
		maxChars = int(n)
	}
	c, err := newAPI(call.Manifest.GetConnection().GetBaseUrl(), call.Runner.Client(), call.Credential)
	if err != nil {
		return nil, err
	}
	q := url.Values{"format": {format}}
	if format == "metadata" {
		q["metadataHeaders"] = metadataHeaders
	}
	var m apiMessage
	if err := c.do(ctx, "GET", "/messages/"+url.PathEscape(id), q, nil, &m); err != nil {
		return asResult(err)
	}
	data := flatten(&m)
	if format == "full" && m.Payload != nil {
		var b bodies
		b.walk(m.Payload)
		text, textCut := cut(b.text, maxChars)
		html, htmlCut := cut(b.html, maxChars)
		data["text"], data["html"], data["truncated"] = text, html, textCut || htmlCut
		data["attachments"] = b.attachments
	}
	return &httpaction.Result{StatusCode: 200, Data: data}, nil
}

// flatten is a message's identity, labels, snippet and headers — what both
// message.get and the trigger payload carry.
func flatten(m *apiMessage) map[string]any {
	h := map[string]string{}
	if m.Payload != nil {
		for _, hdr := range m.Payload.Headers {
			key := strings.ToLower(hdr.Name)
			if _, seen := h[key]; !seen {
				h[key] = decodeHeader(hdr.Value)
			}
		}
	}
	return map[string]any{
		"id": m.ID, "thread_id": m.ThreadID, "label_ids": anyStrings(m.LabelIDs), "snippet": m.Snippet,
		"internal_date": internalDate(m.InternalDate),
		"from":          h["from"], "to": h["to"], "cc": h["cc"], "subject": h["subject"], "date": h["date"],
		"message_id": h["message-id"], "references": h["references"],
	}
}

// bodies collects a message's text and HTML bodies and its attachments.
type bodies struct {
	text, html  string
	attachments []any
}

// walk visits the part tree depth-first. The first text/plain and the first
// text/html that are not attachments are the bodies (in multipart/alternative
// they are the same content); a part with a filename or attachmentId is an
// attachment, listed but never fetched.
func (b *bodies) walk(p *apiPart) {
	if p == nil {
		return
	}
	mediaType, params, _ := mime.ParseMediaType(p.MimeType)
	if mediaType == "" {
		mediaType = strings.ToLower(p.MimeType)
	}
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			if mt, ps, err := mime.ParseMediaType(h.Value); err == nil {
				mediaType, params = mt, ps
			}
		}
	}
	if p.Filename != "" || p.Body.AttachmentID != "" {
		b.attachments = append(b.attachments, map[string]any{
			"filename": decodeHeader(p.Filename), "mime_type": mediaType, "size": p.Body.Size,
		})
		return
	}
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		for _, child := range p.Parts {
			b.walk(child)
		}
	case mediaType == "text/plain" && b.text == "":
		b.text = decodeBody(p.Body.Data, params["charset"])
	case mediaType == "text/html" && b.html == "":
		b.html = decodeBody(p.Body.Data, params["charset"])
	default:
		for _, child := range p.Parts {
			b.walk(child)
		}
	}
}

// decodeBody turns a part's body.data (base64url; Gmail has already undone
// the part's Content-Transfer-Encoding) into UTF-8 text.
func decodeBody(data, cs string) string {
	if data == "" {
		return ""
	}
	raw, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
		if err != nil {
			return ""
		}
	}
	if cs == "" || strings.EqualFold(cs, "utf-8") || strings.EqualFold(cs, "us-ascii") {
		return strings.ToValidUTF8(string(raw), "\uFFFD")
	}
	if enc, _ := charset.Lookup(cs); enc != nil {
		if out, _, err := transform.Bytes(enc.NewDecoder(), raw); err == nil {
			return string(out)
		}
	}
	return strings.ToValidUTF8(string(raw), "\uFFFD")
}

// headerDecoder decodes RFC 2047 encoded-words in any charset x/text knows.
var headerDecoder = &mime.WordDecoder{CharsetReader: func(cs string, input io.Reader) (io.Reader, error) {
	enc, _ := charset.Lookup(cs)
	if enc == nil {
		return nil, fmt.Errorf("unknown charset %q", cs)
	}
	return transform.NewReader(input, enc.NewDecoder()), nil
}}

func decodeHeader(v string) string {
	if out, err := headerDecoder.DecodeHeader(v); err == nil {
		return out
	}
	return v
}

// internalDate renders Gmail's ms-since-epoch string as RFC 3339; "" when it
// is absent or not a number.
func internalDate(ms string) string {
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	return time.UnixMilli(n).UTC().Format(time.RFC3339)
}

func cut(s string, n int) (string, bool) {
	if utf8.RuneCountInString(s) <= n {
		return s, false
	}
	r := []rune(s)
	return string(r[:n]), true
}

func str(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func strings2(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func anyStrings(in []string) []any {
	out := make([]any, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

// Copyright (c) 2025 Reliant Labs

package gmail_test

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every action in the shipped gmail manifest, run through the real runner
// (declarative or go: executor) against the fake Gmail API.

func TestSendBuildsAnRFC2822MessageGmailAccepts(t *testing.T) {
	f := newFakeGmail(t)
	res := f.run("message.send", map[string]any{
		"to": []any{"Ann <ann@example.com>"}, "cc": []any{"cy@example.com"}, "bcc": []any{"audit@example.com"},
		"subject": "Quarterly numbers", "text": "See attached.\n-- \nReliant", "html": "<p>See attached.</p>",
	})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, "sent0001", res.Data["id"])
	assert.Equal(t, "conn_gmail", res.ConnectionID)

	reqs := f.requests()
	require.Len(t, reqs, 1)
	r := reqs[0]
	assert.Equal(t, "POST", r.Method)
	assert.Equal(t, "/gmail/v1/users/me/messages/send", r.Path)
	assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
	assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
	assert.NotContains(t, r.Body, "threadId", "a new message names no thread")

	// Decode `raw` exactly as Gmail does and parse it as mail.
	raw, ok := r.Body["raw"].(string)
	require.True(t, ok)
	assert.NotContains(t, raw, "=", "base64url without padding")
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	require.NoError(t, err)
	p := parse(t, decoded)
	to, err := p.header.AddressList("To")
	require.NoError(t, err)
	assert.Equal(t, "ann@example.com", to[0].Address)
	bcc, _ := p.header.AddressList("Bcc")
	assert.Equal(t, "audit@example.com", bcc[0].Address)
	assert.Equal(t, "Quarterly numbers", p.header.Get("Subject"))
	assert.Equal(t, "See attached.\n-- \nReliant", p.parts["text/plain"])
	assert.Equal(t, "<p>See attached.</p>", p.parts["text/html"])
}

// Gmail threads a reply only when the request names the thread AND the
// message's In-Reply-To/References and subject match the original.
func TestSendReplyThreads(t *testing.T) {
	f := newFakeGmail(t)
	res := f.run("message.send", map[string]any{
		"to": []any{"ann@example.com"}, "subject": "Invoice 1234", "text": "Paid, thanks.",
		"thread_id": "18c1f0e2a3b4c5d6", "in_reply_to": "<CAF+abc@mail.gmail.com>",
	})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, "18c1f0e2a3b4c5d6", res.Data["thread_id"])

	body := f.requests()[0].Body
	assert.Equal(t, "18c1f0e2a3b4c5d6", body["threadId"])
	decoded, err := base64.RawURLEncoding.DecodeString(body["raw"].(string))
	require.NoError(t, err)
	p := parse(t, decoded)
	assert.Equal(t, "<CAF+abc@mail.gmail.com>", p.header.Get("In-Reply-To"))
	assert.Equal(t, "<CAF+abc@mail.gmail.com>", p.header.Get("References"))
	assert.Equal(t, "Re: Invoice 1234", p.header.Get("Subject"), "the reply subject matches the thread's")

	// A subject that already says Re: is kept as is.
	f.reset()
	f.run("message.send", map[string]any{"to": []any{"ann@example.com"}, "subject": "RE: Invoice 1234", "text": "x",
		"thread_id": "18c1f0e2a3b4c5d6", "in_reply_to": "<CAF+abc@mail.gmail.com>"})
	decoded, _ = base64.RawURLEncoding.DecodeString(f.requests()[0].Body["raw"].(string))
	assert.Equal(t, "RE: Invoice 1234", parse(t, decoded).header.Get("Subject"))
}

func TestSendRefusesBadParamsBeforeCallingGmail(t *testing.T) {
	f := newFakeGmail(t)
	for name, params := range map[string]map[string]any{
		"header injection":       {"to": []any{"a@x.com"}, "subject": "hi\r\nBcc: evil@attacker.example", "text": "t"},
		"reply without thread":   {"to": []any{"a@x.com"}, "subject": "s", "text": "t", "in_reply_to": "<a@b>"},
		"no body":                {"to": []any{"a@x.com"}, "subject": "s"},
		"no recipient (schema)":  {"to": []any{}, "subject": "s", "text": "t"},
		"unknown param (schema)": {"to": []any{"a@x.com"}, "subject": "s", "text": "t", "from": "boss@x.com"},
		"bad thread id (schema)": {"to": []any{"a@x.com"}, "subject": "s", "text": "t", "thread_id": "../labels"},
		"not an address":         {"to": []any{"definitely not an address"}, "subject": "s", "text": "t"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.try("message.send", params)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid params")
		})
	}
	assert.Empty(t, f.requests(), "nothing was sent")
}

func TestListPaginatesAndRepeatsLabelIDs(t *testing.T) {
	f := newFakeGmail(t)
	for _, id := range []string{"18c1a1", "18c1a2", "18c1a3", "18c1a4", "18c1a5"} {
		f.addMessage(id, "t-"+id, []string{"INBOX"}, nil, nil)
	}
	res := f.run("message.list", map[string]any{"q": "from:ann newer_than:2d", "label_ids": []any{"INBOX", "UNREAD"}, "page_size": 2.0})
	require.False(t, res.IsError, res.Content)
	items, _ := res.Data["items"].([]any)
	require.Len(t, items, 5, "three pages of two, two, one")
	assert.Equal(t, map[string]any{"id": "18c1a5", "thread_id": "t-18c1a5"}, items[0])

	reqs := f.requests()
	require.Len(t, reqs, 3)
	first := reqs[0]
	assert.Equal(t, "/gmail/v1/users/me/messages", first.Path)
	assert.Equal(t, []string{"INBOX", "UNREAD"}, first.Query["labelIds"], "one labelIds per label, as Gmail takes them")
	assert.Equal(t, "from:ann newer_than:2d", first.Query.Get("q"))
	assert.Equal(t, "2", first.Query.Get("maxResults"))
	assert.Equal(t, "false", first.Query.Get("includeSpamTrash"))
	assert.Empty(t, first.Query.Get("pageToken"))
	assert.Equal(t, "2", reqs[1].Query.Get("pageToken"))
	assert.Equal(t, []string{"INBOX", "UNREAD"}, reqs[2].Query["labelIds"], "later pages keep the filter")

	// An empty mailbox: Gmail omits `messages`.
	empty := newFakeGmail(t)
	res = empty.run("message.list", map[string]any{})
	require.False(t, res.IsError, res.Content)
	assert.Empty(t, res.Data["items"])
}

func b64url(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

func TestGetDecodesBodiesAndFlattensHeaders(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage("18c1f001", "18c1f0aa", []string{"INBOX", "UNREAD"}, map[string]string{
		"From": "=?UTF-8?q?Zo=C3=AB?= <zoe@example.com>", "To": "me@example.com", "Subject": "=?UTF-8?q?Caf=C3=A9_menu?=",
		"Date": "Mon, 5 Oct 2026 09:00:00 +0000", "Message-ID": "<CAF+m1@mail.gmail.com>", "References": "<CAF+m0@mail.gmail.com>",
	}, map[string]any{
		"mimeType": "multipart/mixed",
		"parts": []any{
			map[string]any{"mimeType": "multipart/alternative", "parts": []any{
				map[string]any{"mimeType": "text/plain", "headers": []any{map[string]any{"name": "Content-Type", "value": "text/plain; charset=UTF-8"}},
					"body": map[string]any{"data": b64url("Today: crêpes ✓"), "size": 17}},
				map[string]any{"mimeType": "text/html", "body": map[string]any{"data": b64url("<p>Today: crêpes ✓</p>")}},
			}},
			map[string]any{"mimeType": "application/pdf", "filename": "menu.pdf", "body": map[string]any{"attachmentId": "ANGjdJ8", "size": 48213}},
		},
	})
	res := f.run("message.get", map[string]any{"id": "18c1f001"})
	require.False(t, res.IsError, res.Content)
	d := res.Data
	assert.Equal(t, "18c1f001", d["id"])
	assert.Equal(t, "18c1f0aa", d["thread_id"])
	assert.Equal(t, []any{"INBOX", "UNREAD"}, d["label_ids"])
	assert.Equal(t, "Zoë <zoe@example.com>", d["from"], "RFC 2047 headers are decoded")
	assert.Equal(t, "Café menu", d["subject"])
	assert.Equal(t, "<CAF+m1@mail.gmail.com>", d["message_id"])
	assert.Equal(t, "Today: crêpes ✓", d["text"])
	assert.Equal(t, "<p>Today: crêpes ✓</p>", d["html"])
	assert.Equal(t, false, d["truncated"])
	assert.Equal(t, "2026-10-03T04:00:00Z", d["internal_date"], "internalDate is ms since the epoch")
	atts, _ := d["attachments"].([]any)
	require.Len(t, atts, 1)
	assert.Equal(t, "menu.pdf", atts[0].(map[string]any)["filename"])
	assert.EqualValues(t, 48213, atts[0].(map[string]any)["size"])

	r := f.requests()[0]
	assert.Equal(t, "/gmail/v1/users/me/messages/18c1f001", r.Path)
	assert.Equal(t, "full", r.Query.Get("format"))
	assert.Equal(t, "Bearer "+token, r.Header.Get("Authorization"))
}

func TestGetDecodesOtherCharsetsAndTruncates(t *testing.T) {
	f := newFakeGmail(t)
	// ISO-8859-1 "café" is 63 61 66 e9.
	f.addMessage("18c1f002", "18c1f0bb", []string{"INBOX"}, map[string]string{"Subject": "latin"}, map[string]any{
		"mimeType": "text/plain",
		"headers":  []any{},
		"body":     map[string]any{"data": base64.URLEncoding.EncodeToString([]byte{'c', 'a', 'f', 0xe9, ' ', 'o', 'k'})},
	})
	f.mu.Lock()
	f.messages["18c1f002"]["payload"].(map[string]any)["headers"] = []any{
		map[string]any{"name": "Subject", "value": "latin"},
		map[string]any{"name": "Content-Type", "value": "text/plain; charset=ISO-8859-1"},
	}
	f.mu.Unlock()
	res := f.run("message.get", map[string]any{"id": "18c1f002", "max_body_chars": 4.0})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, "café", res.Data["text"])
	assert.Equal(t, true, res.Data["truncated"])
}

func TestGetMetadataSkipsTheBody(t *testing.T) {
	f := newFakeGmail(t)
	f.addMessage("18c1f003", "18c1f0cc", []string{"INBOX"}, map[string]string{"Subject": "s", "From": "a@x.com"},
		map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": b64url("secret body")}})
	res := f.run("message.get", map[string]any{"id": "18c1f003", "format": "metadata"})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, "s", res.Data["subject"])
	assert.NotContains(t, res.Data, "text")
	q := f.requests()[0].Query
	assert.Equal(t, "metadata", q.Get("format"))
	assert.Contains(t, q["metadataHeaders"], "Message-ID")
}

func TestLabelList(t *testing.T) {
	f := newFakeGmail(t)
	f.labels = []map[string]any{{"id": "INBOX", "name": "INBOX", "type": "system"}, {"id": "Label_7", "name": "Receipts", "type": "user"}}
	res := f.run("label.list", map[string]any{})
	require.False(t, res.IsError, res.Content)
	assert.Equal(t, []any{
		map[string]any{"id": "INBOX", "name": "INBOX", "type": "system"},
		map[string]any{"id": "Label_7", "name": "Receipts", "type": "user"},
	}, res.Data["labels"])
	assert.Equal(t, "/gmail/v1/users/me/labels", f.requests()[0].Path)
}

// Failures are results the graph branches on, classified the same way by the
// declarative actions and the Go executors.
func TestErrorsAreClassified(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		reason    string
		retryable bool
		contains  string
	}{
		{"rate limited", 403, "userRateLimitExceeded", true, "rate limit"},
		{"scope missing", 403, "insufficientPermissions", false, "reconnect Gmail and allow every permission"},
		{"forbidden", 403, "domainPolicy", false, "refused"},
		{"too many", 429, "rateLimitExceeded", true, "rate limit"},
		{"bad request", 400, "invalidArgument", false, ""},
		{"backend", 503, "backendError", true, ""},
	}
	for _, tc := range cases {
		for _, action := range []string{"message.list", "label.list", "message.get", "message.send"} {
			t.Run(tc.name+"/"+action, func(t *testing.T) {
				f := newFakeGmail(t)
				f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					googleErr(w, tc.status, "X", tc.reason, "upstream said no ("+token+")")
				})
				params := map[string]any{}
				switch action {
				case "message.get":
					params["id"] = "abc123"
				case "message.send":
					params = map[string]any{"to": []any{"a@x.com"}, "subject": "s", "text": "t"}
				}
				res := f.run(action, params)
				assert.True(t, res.IsError)
				assert.Equal(t, tc.status, res.StatusCode)
				assert.Equal(t, tc.retryable, res.Retryable, res.Content)
				if tc.contains != "" {
					assert.Contains(t, res.Content, tc.contains)
				}
				assert.NotContains(t, res.Content, token, "the credential is scrubbed from an echoing error")
			})
		}
	}
}

func TestUnauthorizedSaysReconnect(t *testing.T) {
	for _, action := range []string{"message.list", "message.get", "message.send"} {
		t.Run(action, func(t *testing.T) {
			f := newFakeGmail(t)
			f.unauthorized = true
			var params map[string]any
			switch action {
			case "message.send":
				params = map[string]any{"to": []any{"a@x.com"}, "subject": "s", "text": "t"}
			case "message.list":
				params = map[string]any{}
			default:
				params = map[string]any{"id": "abc123"}
			}
			res := f.run(action, params)
			assert.True(t, res.IsError)
			assert.False(t, res.Retryable)
			assert.Equal(t, 401, res.StatusCode)
			assert.Contains(t, res.Content, "reconnect Gmail")
		})
	}
}

// The action schemas the catalog serves are what an agent and the editor see.
func TestManifestParamsAreSelfDescribing(t *testing.T) {
	f := newFakeGmail(t)
	m := f.manifest()
	for _, a := range m.GetActions() {
		raw, err := json.Marshal(a.GetParams().AsMap())
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"connection"`, "%s takes a connection", a.GetId())
		assert.True(t, a.GetTool().GetExpose(), "%s is an agent tool", a.GetId())
		assert.Equal(t, "server", a.GetPlacement(), "%s holds a credential, so it runs on the server", a.GetId())
	}
	_, params, err := mime.ParseMediaType("text/plain; charset=UTF-8")
	require.NoError(t, err)
	assert.Equal(t, "UTF-8", params["charset"])
	assert.True(t, strings.HasPrefix(m.GetConnection().GetBaseUrl(), "https://"))
}

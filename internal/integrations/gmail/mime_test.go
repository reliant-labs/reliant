// Copyright (c) 2025 Reliant Labs

package gmail_test

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/gmail"
)

// parsed is a built message read back with the standard library, the way a
// mail server would.
type parsed struct {
	header mail.Header
	parts  map[string]string // media type -> decoded body
}

func parse(t *testing.T, raw []byte) parsed {
	t.Helper()
	require.True(t, bytes.Contains(raw, []byte("\r\n\r\n")), "headers end with CRLF CRLF")
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	require.NoError(t, err)
	out := parsed{header: msg.Header, parts: map[string]string{}}
	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	require.NoError(t, err)
	decode := func(enc string, r io.Reader) string {
		require.Equal(t, "base64", enc)
		b, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, r))
		require.NoError(t, err)
		return string(b)
	}
	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			pt, pp, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
			require.NoError(t, err)
			assert.Equal(t, "UTF-8", pp["charset"])
			out.parts[pt] = decode(p.Header.Get("Content-Transfer-Encoding"), p)
		}
		out.parts[mediaType] = ""
		return out
	}
	assert.Equal(t, "UTF-8", params["charset"])
	out.parts[mediaType] = decode(msg.Header.Get("Content-Transfer-Encoding"), msg.Body)
	return out
}

func TestBuildPlainMessageParsesBack(t *testing.T) {
	when := time.Date(2026, 10, 5, 9, 30, 0, 0, time.UTC)
	raw, err := gmail.Build(gmail.Message{
		To: []string{"Ann <ann@example.com>", "bob@example.com, Cy <cy@example.org>"}, Cc: []string{"dee@example.com"},
		Bcc: []string{"audit@example.com"}, Subject: "Weekly report", Text: "Hi Ann,\n\n.\nA bare dot line, and a very long line " + strings.Repeat("x", 300),
		Date: when,
	})
	require.NoError(t, err)
	p := parse(t, raw)

	to, err := p.header.AddressList("To")
	require.NoError(t, err)
	require.Len(t, to, 3)
	assert.Equal(t, "ann@example.com", to[0].Address)
	assert.Equal(t, "Ann", to[0].Name)
	assert.Equal(t, "cy@example.org", to[2].Address)
	cc, _ := p.header.AddressList("Cc")
	assert.Equal(t, "dee@example.com", cc[0].Address)
	bcc, _ := p.header.AddressList("Bcc")
	assert.Equal(t, "audit@example.com", bcc[0].Address, "Bcc is in the raw message; Gmail strips it on delivery")
	assert.Equal(t, "Weekly report", p.header.Get("Subject"))
	assert.Empty(t, p.header.Get("From"), "Gmail sets From to the authenticated mailbox")
	d, err := p.header.Date()
	require.NoError(t, err)
	assert.True(t, d.Equal(when))
	assert.Equal(t, "1.0", p.header.Get("MIME-Version"))
	assert.Contains(t, p.parts["text/plain"], "\n.\nA bare dot line", "the body survives byte for byte")
	assert.Contains(t, p.parts["text/plain"], strings.Repeat("x", 300))
	for _, line := range strings.Split(string(raw), "\r\n") {
		assert.LessOrEqual(t, len(line), 998, "RFC 5322 line limit")
	}
}

func TestBuildTextAndHTMLIsAlternative(t *testing.T) {
	raw, err := gmail.Build(gmail.Message{To: []string{"a@x.com"}, Subject: "s", Text: "plain", HTML: "<p>rich</p>"})
	require.NoError(t, err)
	p := parse(t, raw)
	assert.Contains(t, p.parts, "multipart/alternative")
	assert.Equal(t, "plain", p.parts["text/plain"])
	assert.Equal(t, "<p>rich</p>", p.parts["text/html"])
	assert.Less(t, bytes.Index(raw, []byte("text/plain")), bytes.Index(raw, []byte("text/html")), "text first, so a text-only client shows it")

	raw, err = gmail.Build(gmail.Message{To: []string{"a@x.com"}, Subject: "s", HTML: "<b>only</b>"})
	require.NoError(t, err)
	assert.Equal(t, "<b>only</b>", parse(t, raw).parts["text/html"])
}

func TestBuildEncodesNonASCII(t *testing.T) {
	raw, err := gmail.Build(gmail.Message{To: []string{"Zoë Ñúñez <zoe@example.com>"}, Subject: "Café — ☕ 予定", Text: "naïve ünïcödé ✓"})
	require.NoError(t, err)
	for _, line := range strings.Split(strings.SplitN(string(raw), "\r\n\r\n", 2)[0], "\r\n") {
		for _, r := range line {
			require.Less(t, r, rune(128), "headers are 7-bit: %q", line)
		}
	}
	p := parse(t, raw)
	dec := new(mime.WordDecoder)
	subj, err := dec.DecodeHeader(p.header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "Café — ☕ 予定", subj)
	to, err := p.header.AddressList("To")
	require.NoError(t, err)
	assert.Equal(t, "Zoë Ñúñez", to[0].Name)
	assert.Equal(t, "naïve ünïcödé ✓", p.parts["text/plain"])
}

func TestBuildReplyHeaders(t *testing.T) {
	raw, err := gmail.Build(gmail.Message{To: []string{"ann@example.com"}, Subject: "Re: Invoice", Text: "Paid.",
		InReplyTo: "<CAF1@mail.gmail.com>", References: "<CAF0@mail.gmail.com> <CAF1@mail.gmail.com>"})
	require.NoError(t, err)
	p := parse(t, raw)
	assert.Equal(t, "<CAF1@mail.gmail.com>", p.header.Get("In-Reply-To"))
	assert.Equal(t, "<CAF0@mail.gmail.com> <CAF1@mail.gmail.com>", p.header.Get("References"))

	raw, err = gmail.Build(gmail.Message{To: []string{"ann@example.com"}, Subject: "Re: x", Text: "t", InReplyTo: "<a@b>"})
	require.NoError(t, err)
	assert.Equal(t, "<a@b>", parse(t, raw).header.Get("References"), "References defaults to the message replied to")
}

// A parameter can never add a header or end the header block: an attacker
// who controls the subject (a triggered run echoing an inbound email) must
// not be able to Bcc themselves.
func TestBuildRefusesHeaderInjection(t *testing.T) {
	for name, m := range map[string]gmail.Message{
		"subject CRLF":       {To: []string{"a@x.com"}, Subject: "hi\r\nBcc: evil@attacker.example", Text: "t"},
		"subject LF":         {To: []string{"a@x.com"}, Subject: "hi\nBcc: evil@attacker.example", Text: "t"},
		"to CRLF":            {To: []string{"a@x.com\r\nBcc: evil@attacker.example"}, Subject: "s", Text: "t"},
		"in_reply_to CRLF":   {To: []string{"a@x.com"}, Subject: "s", Text: "t", InReplyTo: "<a@b>\r\nBcc: evil@attacker.example"},
		"in_reply_to not id": {To: []string{"a@x.com"}, Subject: "s", Text: "t", InReplyTo: "hello"},
		"references alone":   {To: []string{"a@x.com"}, Subject: "s", Text: "t", References: "<a@b>"},
		"no recipient":       {Subject: "s", Text: "t"},
		"no body":            {To: []string{"a@x.com"}, Subject: "s"},
		"not an address":     {To: []string{"not an address"}, Subject: "s", Text: "t"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := gmail.Build(m)
			require.Error(t, err)
			assert.Nil(t, raw)
		})
	}
}

func TestEncodeRawIsUnpaddedBase64URL(t *testing.T) {
	in := []byte("Subject: ?>\r\n\r\n~~~ÿ")
	enc := gmail.EncodeRaw(in)
	assert.NotContains(t, enc, "=")
	assert.NotContains(t, enc, "+")
	assert.NotContains(t, enc, "/")
	out, err := base64.RawURLEncoding.DecodeString(enc)
	require.NoError(t, err)
	assert.Equal(t, in, out)
}

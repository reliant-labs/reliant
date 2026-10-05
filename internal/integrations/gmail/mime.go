// Copyright (c) 2025 Reliant Labs

package gmail

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Message is an outgoing email, before it is RFC 2822 encoded.
type Message struct {
	To, Cc, Bcc []string
	Subject     string
	Text, HTML  string
	// InReplyTo and References thread a reply (RFC 5322 §3.6.4). Gmail also
	// requires the thread id on the send request and a matching subject.
	InReplyTo  string
	References string
	// Date is the Date header; zero means now.
	Date time.Time
}

// maxHeaderValue bounds a header value: RFC 5322 caps a line at 998 octets,
// and folded header values beyond a few KiB are abuse, not mail.
const maxHeaderValue = 8000

// Build renders m as an RFC 2822 (5322) message, the bytes Gmail's
// messages.send takes base64url-encoded in `raw`.
//
// Every header value is checked for CR and LF, so a parameter cannot inject a
// header (a Bcc smuggled into the subject) or end the header block. Addresses
// are parsed with net/mail and re-rendered, so what is sent is exactly the
// addresses given. Non-ASCII subjects and display names are RFC 2047
// encoded-words. Bodies are UTF-8 with base64 transfer encoding, which
// survives any content (long lines, a bare "." line, 8-bit text) unmangled.
// Text plus HTML is multipart/alternative with text first, so a client that
// cannot show HTML shows the text.
//
// From is left out: Gmail sets it to the authenticated mailbox, and a
// caller-supplied From would be either ignored or an alias the account must
// have verified.
func Build(m Message) ([]byte, error) {
	if len(m.To) == 0 {
		return nil, fmt.Errorf("to: at least one recipient is required")
	}
	if m.Text == "" && m.HTML == "" {
		return nil, fmt.Errorf("a message needs text, html or both")
	}
	if !utf8.ValidString(m.Text) || !utf8.ValidString(m.HTML) {
		return nil, fmt.Errorf("the body must be valid UTF-8")
	}
	var h bytes.Buffer
	header := func(name, value string) error {
		if strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("%s: a header value may not contain a line break", strings.ToLower(name))
		}
		if len(value) > maxHeaderValue {
			return fmt.Errorf("%s: header value too long", strings.ToLower(name))
		}
		h.WriteString(name)
		h.WriteString(": ")
		h.WriteString(value)
		h.WriteString("\r\n")
		return nil
	}
	for _, field := range []struct {
		name string
		list []string
	}{{"To", m.To}, {"Cc", m.Cc}, {"Bcc", m.Bcc}} {
		if len(field.list) == 0 {
			continue
		}
		rendered, err := addressList(field.list)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", strings.ToLower(field.name), err)
		}
		if err := header(field.name, rendered); err != nil {
			return nil, err
		}
	}
	// Checked raw: encoding would make a line break inert (=0D=0A inside an
	// encoded-word), but a subject with one is malformed input to refuse, not
	// to send.
	if strings.ContainsAny(m.Subject, "\r\n\x00") {
		return nil, fmt.Errorf("subject: a header value may not contain a line break")
	}
	if err := header("Subject", encodeWord(m.Subject)); err != nil {
		return nil, err
	}
	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}
	if err := header("Date", date.Format(time.RFC1123Z)); err != nil {
		return nil, err
	}
	if m.InReplyTo != "" {
		if err := checkMsgIDs("in_reply_to", m.InReplyTo); err != nil {
			return nil, err
		}
		refs := m.References
		if refs == "" {
			refs = m.InReplyTo
		}
		if err := checkMsgIDs("references", refs); err != nil {
			return nil, err
		}
		if err := header("In-Reply-To", m.InReplyTo); err != nil {
			return nil, err
		}
		if err := header("References", refs); err != nil {
			return nil, err
		}
	} else if m.References != "" {
		return nil, fmt.Errorf("references: set in_reply_to too (the message replied to)")
	}
	if err := header("MIME-Version", "1.0"); err != nil {
		return nil, err
	}

	switch {
	case m.Text != "" && m.HTML != "":
		boundary, err := newBoundary()
		if err != nil {
			return nil, err
		}
		if err := header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`); err != nil {
			return nil, err
		}
		h.WriteString("\r\n")
		for _, part := range []struct{ mediaType, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			h.WriteString("--" + boundary + "\r\n")
			h.WriteString("Content-Type: " + part.mediaType + "; charset=UTF-8\r\n")
			h.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			h.WriteString(wrapBase64(part.body))
		}
		h.WriteString("--" + boundary + "--\r\n")
	default:
		mediaType, body := "text/plain", m.Text
		if m.Text == "" {
			mediaType, body = "text/html", m.HTML
		}
		if err := header("Content-Type", mediaType+"; charset=UTF-8"); err != nil {
			return nil, err
		}
		if err := header("Content-Transfer-Encoding", "base64"); err != nil {
			return nil, err
		}
		h.WriteString("\r\n")
		h.WriteString(wrapBase64(body))
	}
	return h.Bytes(), nil
}

// EncodeRaw is the `raw` field messages.send takes: base64url, unpadded.
func EncodeRaw(rfc2822 []byte) string { return base64.RawURLEncoding.EncodeToString(rfc2822) }

// addressList parses each address and renders the list as a header value.
// One entry may itself be a comma-separated list ("a@x, b@y"), which is how a
// person (or a model) often writes recipients.
func addressList(entries []string) (string, error) {
	var out []string
	for _, entry := range entries {
		if strings.ContainsAny(entry, "\r\n\x00") {
			return "", fmt.Errorf("%q: an address may not contain a line break", entry)
		}
		list, err := mail.ParseAddressList(entry)
		if err != nil {
			return "", fmt.Errorf("%q is not an email address: %w", entry, err)
		}
		for _, a := range list {
			// (*mail.Address).String RFC 2047-encodes a non-ASCII name and
			// quotes one that needs it.
			out = append(out, a.String())
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no addresses")
	}
	return strings.Join(out, ", "), nil
}

// encodeWord RFC 2047-encodes s when it is not plain printable ASCII.
func encodeWord(s string) string {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return mime.QEncoding.Encode("UTF-8", s)
		}
	}
	return s
}

// checkMsgIDs accepts a space-separated list of RFC 5322 msg-ids
// (<left@right>). A reply whose threading headers are not msg-ids does not
// thread, and an unchecked value is a header-injection vector.
func checkMsgIDs(field, value string) error {
	ids := strings.Fields(value)
	if len(ids) == 0 {
		return fmt.Errorf("%s: empty", field)
	}
	for _, id := range ids {
		if len(id) < 5 || id[0] != '<' || id[len(id)-1] != '>' || !strings.Contains(id, "@") ||
			strings.ContainsAny(id[1:len(id)-1], "<> \t\r\n") {
			return fmt.Errorf("%s: %q is not a Message-ID (expected <id@host>)", field, id)
		}
	}
	return nil
}

func newBoundary() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "reliant-" + hex.EncodeToString(b), nil
}

// wrapBase64 base64-encodes s in 76-character lines (RFC 2045 §6.8).
func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}

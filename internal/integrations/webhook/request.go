// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// MaxBodyBytes caps a delivery body. Provider payloads are well under it
// (GitHub's largest, a push, is capped at 25MB by GitHub but a trigger never
// needs one that large: the run can fetch the rest).
const MaxBodyBytes = 1 << 20

// errBodyTooLarge is a body over MaxBodyBytes.
var errBodyTooLarge = errors.New("webhook: body too large")

// readBody reads at most MaxBodyBytes.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBodyBytes {
		return nil, errBodyTooLarge
	}
	return body, nil
}

// isForm reports a form-encoded body, which some providers (Twilio) sign
// field by field.
func isForm(header http.Header) bool {
	mediaType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && mediaType == "application/x-www-form-urlencoded"
}

// secretHeaders are never recorded on an event: they carry credentials, or
// are the signature over the body (which says nothing an agent needs and
// everything an attacker could replay).
var secretHeaders = map[string]bool{
	"Authorization":       true,
	"Proxy-Authorization": true,
	"Cookie":              true,
	"Set-Cookie":          true,
}

// secretHeaderWords mark a header as secret-bearing by name.
var secretHeaderWords = []string{"token", "secret", "signature", "key", "auth", "password", "session"}

func isSecretHeader(name string) bool {
	canonical := http.CanonicalHeaderKey(name)
	if secretHeaders[canonical] {
		return true
	}
	lower := strings.ToLower(canonical)
	for _, word := range secretHeaderWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// maxRecordedHeader bounds one recorded header value.
const maxRecordedHeader = 1024

// recordedHeaders is the header map stored on an event: one value per
// header (the first), secrets removed, values bounded.
func recordedHeaders(header http.Header, also ...string) map[string]any {
	drop := make(map[string]bool, len(also))
	for _, name := range also {
		if name != "" {
			drop[http.CanonicalHeaderKey(name)] = true
		}
	}
	out := make(map[string]any, len(header))
	for name, values := range header {
		if len(values) == 0 || isSecretHeader(name) || drop[http.CanonicalHeaderKey(name)] {
			continue
		}
		value := values[0]
		if len(value) > maxRecordedHeader {
			value = value[:maxRecordedHeader]
		}
		out[http.CanonicalHeaderKey(name)] = value
	}
	return out
}

// recordedQuery is the query map stored on an event, with secret-looking
// parameters removed (a sender that puts a token in the query is common).
func recordedQuery(query url.Values) map[string]any {
	out := make(map[string]any, len(query))
	for name, values := range query {
		if len(values) == 0 || isSecretHeader(name) {
			continue
		}
		out[name] = values[0]
	}
	return out
}

// parseBody is the body as an agent and a filter see it: parsed JSON when it
// is JSON, the form as a map when it is a form, the text otherwise.
func parseBody(body []byte, header http.Header) any {
	if len(body) == 0 {
		return nil
	}
	if isForm(header) {
		form, err := url.ParseQuery(string(body))
		if err == nil {
			out := make(map[string]any, len(form))
			for k, v := range form {
				if len(v) > 0 {
					out[k] = v[0]
				}
			}
			return out
		}
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err == nil {
		return parsed
	}
	return string(body)
}

// publicURL rebuilds the URL a sender addressed from the configured base and
// the request's path and raw query. Returns nil when there is no base.
func publicURL(base *url.URL, r *http.Request) *url.URL {
	if base == nil {
		return nil
	}
	u := *base
	u.Path = strings.TrimSuffix(base.Path, "/") + r.URL.Path
	u.RawPath = ""
	if r.URL.RawPath != "" {
		u.RawPath = strings.TrimSuffix(base.EscapedPath(), "/") + r.URL.RawPath
	}
	u.RawQuery = r.URL.RawQuery
	u.Fragment = ""
	return &u
}

// parsePublicBase parses PUBLIC_URL. Empty or invalid yields nil.
func parsePublicBase(raw string) *url.URL {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		logging.Warn("PUBLIC_URL is not an absolute URL; provider webhooks cannot be verified", "public_url", raw)
		return nil
	}
	return u
}

// writeStatus writes a short plain-text reply. Bodies never echo input.
func writeStatus(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

// Intake is the trigger layer the receivers hand events to. Satisfied by
// *triggers.Intake.
type Intake interface {
	Accept(ctx context.Context, trigger *core.Trigger, ev triggers.InboundEvent, opts triggers.AcceptOptions) (*triggers.AcceptResult, error)
}

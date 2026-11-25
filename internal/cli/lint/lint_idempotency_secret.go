// File: internal/cli/lint/lint_idempotency_secret.go
//
// What forge-idempotency-secret-response-marked-read reads: does an rpc's
// RESPONSE carry a secret or a value minted for this one call?
//
// NO_SIDE_EFFECTS is a promise that the rpc only reads, and Connect acts on
// it: a client may send such a unary rpc as an HTTP GET, which a browser
// cache, a CDN or a proxy may store and replay to whoever asks next. That
// is right for an order or a list of them. It is wrong for an access token,
// a presigned URL, an OAuth state, an invite or claim link, a checkout or
// billing-portal URL, a one-time code, a nonce or a challenge: each call
// must mint a new one, and a replayed one hands a credential to someone
// else. Such an rpc may well be named Get, so rule 1 of the lane would ask
// for exactly the wrong declaration; this rule takes its place.
//
// ── What a field says ────────────────────────────────────────────────────
//
// A field is secret-shaped when (secretFieldSignal):
//
//   - it declares protobuf's `[debug_redact = true]`: the author already
//     said the value is sensitive. Any kind, a message included;
//   - it is a string or bytes (a repeated one, or a map's string or bytes
//     value) whose NAME says so. Names are split into words (snake_case or
//     camelCase) and judged by the head noun, the last word, because that
//     is what an English compound names: `access_token` is a token,
//     `token_id`, `token_prefix` and `password_hash` are not (a hash is not
//     a per-call value). A trailing value, plaintext, raw or code is looked
//     through (`secret_value`, `otp_code`). The head nouns are token,
//     secret, password, passwd, passphrase, credential, nonce, challenge and
//     otp, singular or plural. Besides those: `api_key`, `access_key`,
//     `private_key` and `signing_key`; `one_time` anywhere (`one_time_code`);
//     `oauth_state` (never a bare `state`, which is a status far more
//     often); a word presigned anywhere; and a url, uri or link whose
//     qualifier marks it as minted per call — signed, presigned, upload,
//     download, export, management, checkout, portal, authorize,
//     authorization, reset, invite, invitation, claim or magic
//     (`upload_url`, `invite_link`, `authorize_url`).
//
// Kinds other than string and bytes are never judged by name, so the
// `input_tokens` and `max_tokens` counts of an LLM usage response do not
// fire. Pagination cursors never fire either: `page_token`,
// `next_page_token`, `prev_page_token`, `previous_page_token`,
// `continuation_token`, `resume_token`, `sync_token` and `cursor` hand back
// a position, not a credential, and a GET that caches one is correct. Nor
// does a name qualified as a derived display form — masked, hashed,
// redacted, obfuscated or truncated (`masked_api_key`).
//
// A field marked `// forge:secret` is NOT judged, nor walked into: the
// marker says reads never return it (forge's generated read path never
// packs it into a response). It is also the field-level answer for a
// write-only field a shared message carries — set on a request, empty on
// every read — so one marker settles every rpc that returns the message.
//
// ── The walk ─────────────────────────────────────────────────────────────
//
// The response message is walked breadth first through the descriptor's
// deep schema (ServiceDef.Schemas), into message fields and message map
// values, at most secretWalkDepth levels down, each message once (so a
// self-referential Tree or a mutually recursive pair terminates). The
// shallowest hit is reported, by its dotted path. Well-known types are not
// in the schema and are not walked. A descriptor written before the deep
// schema existed yields nothing; `forge generate` refreshes it.

package lint

import (
	"slices"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/internal/codegen"
)

// secretWalkDepth is how many message levels below the response the walk
// descends. A credential deeper than this is unusual enough to leave to
// review; the cap bounds the walk on a wide, deep graph.
const secretWalkDepth = 6

// secretHeadNouns name a credential or a one-use value when they are a
// field's head noun.
var secretHeadNouns = []string{
	"challenge", "challenges", "credential", "credentials", "nonce", "nonces",
	"otp", "otps", "passphrase", "passwd", "password", "passwords", "secret",
	"secrets", "token", "tokens",
}

// secretValueWords are looked through when they end a name: `secret_value`
// is a secret, `otp_code` a one-time password.
var secretValueWords = []string{"code", "plaintext", "raw", "value"}

// secretKeyQualifiers make a trailing key (or keys) a credential:
// `api_key`, `access_key`, `private_key`, `signing_key`. A bare key, an
// `object_key` or an `idempotency_key` is not one.
var secretKeyQualifiers = []string{"access", "api", "private", "signing"}

// perCallURLQualifiers make a trailing url, uri or link one minted for the
// call: a signed or presigned URL, an upload or download URL, a management,
// checkout or billing-portal session, a signed export, an OAuth authorize URL
// (it carries the state), a password-reset, invite, claim or magic link.
var perCallURLQualifiers = []string{
	"authorization", "authorize", "checkout", "claim", "download", "export",
	"invitation", "invite", "magic", "management", "portal", "presigned",
	"reset", "signed", "upload",
}

// urlHeadNouns are the heads perCallURLQualifiers qualify.
var urlHeadNouns = []string{"link", "links", "uri", "uris", "url", "urls"}

// paginationFieldNames hand back a position in a list or a stream, never a
// credential.
var paginationFieldNames = []string{
	"continuation_token", "cursor", "next_cursor", "next_page_token",
	"page_token", "prev_cursor", "prev_page_token", "previous_cursor",
	"previous_page_token", "resume_token", "sync_token",
}

// displayQualifiers say a field shows a derived form of a secret, not the
// secret: `masked_api_key`, `hashed_token`, `redacted_password`.
var displayQualifiers = []string{"hashed", "masked", "obfuscated", "redacted", "truncated"}

// secretFieldSignal says why a field is secret-shaped, phrased to follow
// "which" (`which is named as a presigned URL`), or "" when it is not.
func secretFieldSignal(f codegen.SchemaFieldDef) string {
	if f.Secret {
		return "" // forge's read path never packs a forge:secret field
	}
	if f.Redacted {
		return "declares debug_redact = true"
	}
	if !secretShapedKind(f) {
		return ""
	}
	lower := strings.ToLower(f.Name)
	if slices.Contains(paginationFieldNames, lower) || strings.HasSuffix(lower, "_page_token") {
		return ""
	}
	w := fieldNameWords(f.Name)
	if len(w) == 0 || slices.ContainsFunc(w, func(q string) bool { return slices.Contains(displayQualifiers, q) }) {
		return ""
	}
	if slices.Contains(w, "presigned") {
		return "is named as a presigned URL"
	}
	for i := 0; i+1 < len(w); i++ {
		if w[i] == "one" && w[i+1] == "time" {
			return "is named as a one-time value"
		}
	}
	head := w[len(w)-1]
	if len(w) > 1 && slices.Contains(secretValueWords, head) {
		w = w[:len(w)-1]
		head = w[len(w)-1]
	}
	var prev string
	if len(w) > 1 {
		prev = w[len(w)-2]
	}
	switch {
	case slices.Contains(secretHeadNouns, head):
		return "is named as a credential or a one-use value (" + head + ")"
	case (head == "key" || head == "keys") && slices.Contains(secretKeyQualifiers, prev):
		return "is named as a credential (" + prev + "_" + head + ")"
	case head == "state" && prev == "oauth":
		return "is named as an OAuth state"
	case slices.Contains(urlHeadNouns, head) && slices.ContainsFunc(w[:len(w)-1], func(q string) bool { return slices.Contains(perCallURLQualifiers, q) }):
		return "is named as a URL minted for this call"
	}
	return ""
}

// secretShapedKind reports whether a field's value is text or bytes — the
// only kinds a name can make a credential.
func secretShapedKind(f codegen.SchemaFieldDef) bool {
	switch f.Kind {
	case "string", "bytes":
		return true
	case "map":
		return f.MapValueKind == "string" || f.MapValueKind == "bytes"
	}
	return false
}

// fieldNameWords splits a proto field name into lower-case words, at
// underscores and at lower-to-upper case changes (`accessToken`).
func fieldNameWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	prevLower := false
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.':
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		cur = append(cur, r)
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
	}
	flush()
	return words
}

// secretResponseField walks the message fq names through schemas and
// returns the dotted path of the shallowest secret-shaped field and why,
// or "" when there is none (or fq is not in the schema).
func secretResponseField(schemas map[string][]codegen.SchemaFieldDef, fq string) (path, why string) {
	type node struct {
		fq, prefix string
		depth      int
	}
	seen := map[string]bool{fq: true}
	queue := []node{{fq: fq}}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, f := range schemas[n.fq] {
			p := n.prefix + f.Name
			if sig := secretFieldSignal(f); sig != "" {
				return p, sig
			}
			if f.Secret || n.depth+1 >= secretWalkDepth {
				continue
			}
			next := ""
			switch {
			case f.Kind == "message":
				next = f.TypeName
			case f.Kind == "map" && f.MapValueKind == "message":
				next = f.MapValueTypeName
			}
			if next != "" && !seen[next] {
				seen[next] = true
				queue = append(queue, node{fq: next, prefix: p + ".", depth: n.depth + 1})
			}
		}
	}
	return "", ""
}

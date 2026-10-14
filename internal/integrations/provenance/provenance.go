// Copyright (c) 2025 Reliant Labs

// Package provenance is the vocabulary two halves of "did Reliant cause this
// event?" share: the write ledger (internal/integrations/writeledger), which
// records what an integration action is about to write, and the webhook parser
// (internal/integrations/webhook/github), which records what an arriving event
// says about itself. Matching one against the other is what becomes
// trigger.origin (internal/triggers).
//
// It is pure: no I/O and no dependencies, so the writer and the parser cannot
// disagree about how a body is hashed or a repository is named — the only way
// a correlation silently stops matching.
//
//forge:exclude-contract: pure vocabulary (value types and hash/name functions) shared by the write ledger and the webhook parser; no I/O, state or clock, and nothing to substitute
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The object kinds a correlation names. An issue and a pull request share one
// number space per repository, so a number alone does not say which it is.
const (
	KindComment       = "comment"
	KindReview        = "review"
	KindReviewComment = "review_comment"
	KindIssue         = "issue"
	KindPullRequest   = "pull_request"
	KindBranch        = "branch"
)

// Correlation is what a write intends, or an event carries, to be matched on.
// Every field is optional: a match compares only the fields both sides have.
type Correlation struct {
	// ObjectKind is one of the Kind* constants.
	ObjectKind string `json:"object_kind,omitempty"`
	// Events (write side only) are the event types this write can echo as, e.g.
	// a comment is issue_comment.created, a review pull_request_review.submitted.
	Events []string `json:"events,omitempty"`
	// BodySHA256 is BodyDigest of the text written or received.
	BodySHA256 string `json:"body_sha256,omitempty"`
	// Label is the label added or removed. Labels (write side only) are the
	// labels a creation or update sets, any of which may echo.
	Label  string   `json:"label,omitempty"`
	Labels []string `json:"labels,omitempty"`
	// Assignees (write side only) are the logins a creation or update assigns.
	Assignees []string `json:"assignees,omitempty"`
	// Branch and HeadSHA identify a push or a pull request's head.
	Branch  string `json:"branch,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`
	// ReviewState is a review's verdict, lowercased: commented, approved or
	// changes_requested.
	ReviewState string `json:"review_state,omitempty"`
	// State is the state an update asked for (open, closed).
	State string `json:"state,omitempty"`
}

// Map is c as the JSON object the ledger stores.
func (c Correlation) Map() map[string]any {
	raw, _ := json.Marshal(c)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

// CorrelationFromMap reads a stored correlation back.
func CorrelationFromMap(m map[string]any) Correlation {
	var c Correlation
	if len(m) == 0 {
		return c
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return c
	}
	_ = json.Unmarshal(raw, &c)
	return c
}

// Echo is what an arriving event says about itself, computed by the parser from
// the delivery's raw body. Everything in it is data the sender controls, except
// that the delivery's signature was verified: it is evidence for attributing an
// event, and is never authority for anything.
type Echo struct {
	Correlation
	// EventType is "<event>.<action>", as trigger.payload.event.
	EventType string `json:"event_type,omitempty"`
	// Scope is the repository, ScopeOf form.
	Scope string `json:"scope,omitempty"`
	// Target is the issue or pull request the event is about, TargetOf form, or
	// the branch for a push, BranchTargetOf form.
	Target string `json:"target,omitempty"`
	// ObjectID is the id of the comment, review or review comment, or the
	// issue or pull request number: the object the event is about.
	ObjectID string `json:"object_id,omitempty"`
	// SenderType and SenderLogin are the delivery's `sender`.
	SenderType  string `json:"sender_type,omitempty"`
	SenderLogin string `json:"sender_login,omitempty"`
	// AppSlug is the GitHub App the object was created through
	// (performed_via_github_app.slug), when GitHub says.
	AppSlug string `json:"app_slug,omitempty"`
	// RunTrailer is the `Reliant-Run:` trailer of a push's head commit. It is a
	// CLAIM anyone can write, so it may only ever suppress.
	RunTrailer string `json:"run_trailer,omitempty"`
}

// BodyDigest is the digest of a written or received text: SHA-256 of the text
// with line endings normalized and surrounding space trimmed, which GitHub
// does not preserve exactly. The parser computes it over the RAW body, before
// the recorded payload truncates it, so a long comment still matches.
func BodyDigest(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSpace(stripSignature(strings.TrimSpace(body)))
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// SignaturePrefix opens the one-line footer the GitHub App's bot appends to
// what it writes. The write ledger records a body before the credential that
// signs it is chosen, so it digests the text without the footer; the echo has
// it. Both go through BodyDigest, which ignores a trailing signature, so they
// agree without either side knowing the other's footer.
const SignaturePrefix = "<sub>Posted by Reliant for "

// stripSignature removes a trailing signature paragraph from a trimmed body.
func stripSignature(body string) string {
	i := strings.LastIndex(body, "\n\n")
	if i < 0 {
		if strings.HasPrefix(body, SignaturePrefix) && strings.HasSuffix(body, "</sub>") {
			return ""
		}
		return body
	}
	last := body[i+2:]
	if strings.HasPrefix(last, SignaturePrefix) && strings.HasSuffix(last, "</sub>") && !strings.Contains(last, "\n") {
		return body[:i]
	}
	return body
}

// ScopeOf names a GitHub repository: "github:owner/repo", lowercased because
// GitHub treats repository names case-insensitively.
func ScopeOf(ownerRepo string) string {
	return "github:" + strings.ToLower(strings.TrimSpace(ownerRepo))
}

// TargetOf names an issue or pull request: "github:owner/repo#12".
func TargetOf(scope string, number string) string {
	if number == "" {
		return scope
	}
	return scope + "#" + number
}

// BranchTargetOf names a branch: "github:owner/repo@branch". Branch names are
// case-sensitive and kept as written.
func BranchTargetOf(scope, branch string) string {
	return scope + "@" + branch
}

// IDString renders a provider id (a JSON number, however it was decoded) as the
// string both sides store.
func IDString(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(n)
	case int:
		return strconv.Itoa(n)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case int64:
		return strconv.FormatInt(n, 10)
	case uint64:
		return strconv.FormatUint(n, 10)
	case float64:
		if n == float64(int64(n)) {
			return strconv.FormatInt(int64(n), 10)
		}
		return strconv.FormatFloat(n, 'f', -1, 64)
	case json.Number:
		return n.String()
	case fmt.Stringer:
		return n.String()
	}
	return ""
}

// RunTrailerKey is the commit trailer a Reliant push carries.
const RunTrailerKey = "Reliant-Run"

// RunTrailer returns the value of the Reliant-Run trailer in a commit message:
// a `Reliant-Run: <id>` line in the message's final paragraph, which is where
// git reads trailers from. "" when there is none.
func RunTrailer(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.TrimRight(message, "\n ")
	if message == "" {
		return ""
	}
	paragraph := message
	if i := strings.LastIndex(message, "\n\n"); i >= 0 {
		paragraph = message[i+2:]
	}
	value := ""
	for _, line := range strings.Split(paragraph, "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), RunTrailerKey) {
			value = strings.TrimSpace(rest)
		}
	}
	return value
}

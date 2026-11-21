// Package cloudcred is the PUBLIC seam between forge and a HOST APPLICATION
// that already holds the user's session — the credential-helper protocol.
//
// ── WHY THIS EXISTS: THE DEPENDENCY IS INVERTED ───────────────────────
//
// A user signed in to an application that embeds or launches forge (Reliant)
// must not have to run `forge login` as well. The obvious way to get that —
// teach forge to go and find that application's credential — is the one thing
// forge must never do: internal/cloud's package header forbids importing
// control-plane or reliant, because a user deploying to hosted infra without
// the reliant harness must never have to install reliant first.
//
// So forge publishes a PROTOCOL and the host implements it. When the
// environment variable HelperEnv names a command, forge runs it, writes a
// Request (which control plane, which scopes) to its stdin, and reads a
// Response (a token, or a structured refusal) from its stdout. It is the model
// kubectl's exec credential plugins, git's credential helpers and AWS's
// credential_process already use, for the same reason: the process that holds
// the session mints what the tool needs, at the moment it needs it.
//
// ── WHY A HELPER AND NOT A DEPOSIT ────────────────────────────────────
//
// This package used to let a host DEPOSIT its own token into forge's
// credentials file. That copied a permanent, multi-purpose session credential
// (one that could also connect as the user's daemon) into a second file, and
// forge presented it to deploy endpoints that — correctly — refused it,
// because it did not hold deploy authority. A token may never grant authority
// it does not hold (forge/pkg/accesstoken). A helper fixes both halves: the
// session credential never leaves the host, and what forge receives is a
// short-lived token the host obtained for exactly the scopes forge asked for.
// RemoveLegacyHostDeposits is how a host cleans up what the old seam wrote.
//
// ── THE PROCESS BOUNDARY IS THE CONTRACT ─────────────────────────────
//
// The helper is a separate process even when the host embeds forge in its own
// binary. One mechanism means one thing to test and one thing to document, and
// the same helper serves a forge the host launched directly, a forge an agent
// ran in a shell the host spawned, and a forge the host embeds. Exec is forge's
// half of the protocol; Serve is the host's.
//
// Nothing here reads the environment (forge/pkg never does — see
// pkg/.golangci.yml). The application edge reads HelperEnv and passes the
// parsed command in.
//
//forge:exclude-contract: a pkg/ wire-protocol library: request/response shapes plus the two ends (Exec, Serve) of one subprocess exchange, stdlib only
package cloudcred

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// HelperEnv is the environment variable that names the credential helper.
//
// Its value is either a JSON array of strings — the helper's argv, which is
// what a host sets programmatically, so an executable path containing spaces
// survives on every OS — or a plain string naming a single executable, for a
// human who points it at a script. A plain string is never split on spaces:
// shell-style splitting is exactly the thing that differs between platforms.
const HelperEnv = "FORGE_CREDENTIAL_HELPER"

// ProtocolVersion is the version of the Request/Response shapes. A Response
// carrying a different version is refused rather than half-understood.
const ProtocolVersion = 1

// Request is what forge writes to the helper's stdin.
type Request struct {
	Version int `json:"version"`
	// Endpoint is the control-plane origin the token will be PRESENTED to,
	// normalized (credentials.Normalize). A helper must refuse to answer for
	// an endpoint its session does not belong to: the endpoint comes from a
	// project's KCL, and a repository is not a trusted party.
	Endpoint string `json:"endpoint"`
	// Scopes are what forge would like. A helper may grant fewer — never more
	// than its session holds — and reports what it granted.
	Scopes []string `json:"scopes,omitempty"`
}

// ErrorCode classifies a helper's refusal, so forge can word the failure
// without parsing prose.
type ErrorCode string

const (
	// CodeNoSession: the host holds no session that applies to this endpoint
	// (not signed in, or signed in somewhere else).
	CodeNoSession ErrorCode = "no_session"
	// CodeDenied: a session applies, but it cannot authorize this — for
	// example it was issued without deploy permission. Signing in again or
	// granting the permission is the fix, not retrying.
	CodeDenied ErrorCode = "denied"
	// CodeUnavailable: the helper could not reach whatever mints tokens.
	// Transient; retrying later may succeed.
	CodeUnavailable ErrorCode = "unavailable"
)

// ResponseError is the wire form of a refusal.
type ResponseError struct {
	Code ErrorCode `json:"code"`
	// Message is actionable and safe to print; it never contains a token.
	Message string `json:"message"`
	// Reason is the stable reason the minting server gave, when it gave one
	// (its svcerr.ReasonHeader) — "scope_missing" for a denial over missing
	// authority. forge reports it in its --json documents so a host's UI
	// can route on it without parsing Message. Optional.
	Reason string `json:"reason,omitempty"`
	// Scopes are the scopes a "scope_missing" refusal names (the server's
	// accesstoken.ScopesHeader). Optional.
	Scopes []string `json:"scopes,omitempty"`
}

// Response is what the helper writes to stdout. Exactly one of Token and
// Error is set.
type Response struct {
	Version int    `json:"version"`
	Token   string `json:"token,omitempty"`
	// ExpiresAt is when Token stops being accepted; nil means the helper does
	// not know. forge re-runs the helper rather than present an expired token.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Scopes the token carries, as the issuer reported them. Display only.
	Scopes []string `json:"scopes,omitempty"`
	// Source names where the token came from, for a human ("Reliant session
	// for https://api.example.com"). forge prints it; it must never contain
	// the token.
	Source string         `json:"source,omitempty"`
	Error  *ResponseError `json:"error,omitempty"`
}

// Token is a successful helper answer.
type Token struct {
	Token     string
	ExpiresAt *time.Time
	Scopes    []string
	Source    string
}

// Expired reports whether the token expires within margin of now. A token with
// no known expiry never does.
func (t Token) Expired(now time.Time, margin time.Duration) bool {
	return t.ExpiresAt != nil && !t.ExpiresAt.After(now.Add(margin))
}

// HelperError is a helper's well-formed refusal. Exec returns it (use
// errors.As); a host's mint function returns it from Serve's callback to
// choose the code.
type HelperError struct {
	Code    ErrorCode
	Message string
	// Reason and Scopes carry the minting server's structured refusal, when
	// it gave one; see ResponseError. Optional, and never part of Is.
	Reason string
	Scopes []string
}

func (e *HelperError) Error() string {
	if e.Message == "" {
		return "credential helper: " + string(e.Code)
	}
	return e.Message
}

// Is makes errors.Is(err, &HelperError{Code: X}) match on the code alone.
func (e *HelperError) Is(target error) bool {
	t, ok := target.(*HelperError)
	return ok && t.Message == "" && t.Code == e.Code
}

// ErrNoSession matches (errors.Is) a refusal with CodeNoSession.
var ErrNoSession = &HelperError{Code: CodeNoSession}

// ParseCommand turns HelperEnv's value into an argv. An empty or blank value
// means no helper is configured and returns (nil, nil).
func ParseCommand(value string) ([]string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil, nil
	}
	if !strings.HasPrefix(v, "[") {
		return []string{v}, nil
	}
	var argv []string
	if err := json.Unmarshal([]byte(v), &argv); err != nil {
		return nil, fmt.Errorf("%s is not a JSON array of strings: %w", HelperEnv, err)
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("%s names no executable", HelperEnv)
	}
	return argv, nil
}

// FormatCommand renders argv as a HelperEnv value. Always the JSON-array form,
// so a host never has to think about quoting.
func FormatCommand(argv []string) string {
	raw, _ := json.Marshal(argv) // []string cannot fail to marshal
	return string(raw)
}

// Bounds on what Exec reads from a helper. stdout carries one small JSON
// document; stderr is only ever shown as a diagnostic tail.
const (
	maxStdout = 1 << 20
	maxStderr = 4 << 10
)

// Exec runs the helper argv with req and returns its token.
//
// ctx bounds the run; give it a deadline. The helper inherits this process's
// environment. Errors:
//
//   - *HelperError — the helper answered with a refusal; its Message is the
//     host's own actionable advice and is safe to print.
//   - anything else — the helper could not be run, failed, or answered with
//     something that is not a protocol response.
//
// stdout is NEVER included in an error: on a malformed answer it may still
// hold a token. A failing helper's stderr is included, bounded.
func Exec(ctx context.Context, argv []string, req Request) (Token, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return Token{}, errors.New("cloudcred: no credential helper command")
	}
	req.Version = ProtocolVersion
	in, err := json.Marshal(req)
	if err != nil {
		return Token{}, fmt.Errorf("cloudcred: encode request: %w", err)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // the user's own configured helper
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxStdout, maxStderr
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Token{}, fmt.Errorf("credential helper %s did not answer: %w", argv[0], ctxErr)
	}
	if runErr != nil {
		return Token{}, fmt.Errorf("credential helper %s failed: %w%s", argv[0], runErr, stderrSuffix(stderr.String()))
	}
	if stdout.truncated {
		return Token{}, fmt.Errorf("credential helper %s wrote more than %d bytes to stdout", argv[0], maxStdout)
	}

	var resp Response
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		// Deliberately not %w and not the bytes: a json error can quote the
		// input it choked on, and that input may be a token.
		return Token{}, fmt.Errorf("credential helper %s did not write a protocol response to stdout%s", argv[0], stderrSuffix(stderr.String()))
	}
	return resp.token(argv[0])
}

// token validates a decoded Response.
func (r Response) token(helper string) (Token, error) {
	if r.Version != ProtocolVersion {
		return Token{}, fmt.Errorf("credential helper %s speaks protocol version %d; forge speaks %d", helper, r.Version, ProtocolVersion)
	}
	if r.Error != nil {
		code := r.Error.Code
		switch code {
		case CodeNoSession, CodeDenied, CodeUnavailable:
		default:
			code = CodeUnavailable
		}
		msg := strings.TrimSpace(r.Error.Message)
		if msg == "" {
			msg = fmt.Sprintf("credential helper %s refused (%s)", helper, r.Error.Code)
		}
		return Token{}, &HelperError{Code: code, Message: msg,
			Reason: strings.TrimSpace(r.Error.Reason), Scopes: r.Error.Scopes}
	}
	if strings.TrimSpace(r.Token) == "" {
		return Token{}, fmt.Errorf("credential helper %s answered with neither a token nor an error", helper)
	}
	return Token{Token: r.Token, ExpiresAt: r.ExpiresAt, Scopes: r.Scopes, Source: r.Source}, nil
}

// Serve is the host's half: read one Request from r, call mint, write one
// Response to w.
//
// mint's error decides the answer. A *HelperError (errors.As) is sent with its
// code and message; any other error is sent as CodeUnavailable with its text —
// so a mint function must never put a secret in an error. Serve returns an
// error only when it cannot read the request or write the response; a refusal
// is a successful exchange, and the helper process should exit 0 after it.
func Serve(ctx context.Context, r io.Reader, w io.Writer, mint func(context.Context, Request) (Token, error)) error {
	var req Request
	dec := json.NewDecoder(io.LimitReader(r, maxStdout))
	if err := dec.Decode(&req); err != nil {
		return writeResponse(w, Response{Error: &ResponseError{
			Code:    CodeUnavailable,
			Message: fmt.Sprintf("the credential helper could not read forge's request: %v", err),
		}})
	}
	if req.Version != ProtocolVersion {
		return writeResponse(w, Response{Error: &ResponseError{
			Code:    CodeUnavailable,
			Message: fmt.Sprintf("forge sent protocol version %d; this credential helper speaks %d — upgrade the older of the two", req.Version, ProtocolVersion),
		}})
	}
	if key, err := credentials.Normalize(req.Endpoint); err == nil {
		req.Endpoint = key
	}

	tok, err := mint(ctx, req)
	if err != nil {
		var he *HelperError
		if !errors.As(err, &he) {
			he = &HelperError{Code: CodeUnavailable, Message: err.Error()}
		}
		return writeResponse(w, Response{Error: &ResponseError{
			Code: he.Code, Message: he.Message, Reason: he.Reason, Scopes: he.Scopes,
		}})
	}
	if strings.TrimSpace(tok.Token) == "" {
		return writeResponse(w, Response{Error: &ResponseError{
			Code:    CodeUnavailable,
			Message: "the credential helper produced an empty token",
		}})
	}
	return writeResponse(w, Response{
		Token:     tok.Token,
		ExpiresAt: tok.ExpiresAt,
		Scopes:    tok.Scopes,
		Source:    tok.Source,
	})
}

func writeResponse(w io.Writer, resp Response) error {
	resp.Version = ProtocolVersion
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		return fmt.Errorf("cloudcred: write response: %w", err)
	}
	return nil
}

func stderrSuffix(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "\n  helper stderr: " + s
}

// limitedBuffer keeps the first limit bytes written to it and drops the rest,
// recording that it did. A helper that floods a pipe must not grow forge's
// memory without bound — and must not block on a full pipe either, which is
// why excess bytes are accepted and discarded rather than refused.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); room < len(p) {
		b.truncated = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *limitedBuffer) String() string { return b.buf.String() }

// legacyHostClientID is the client id the retired deposit seam wrote under.
const legacyHostClientID = "host-app"

// RemoveLegacyHostDeposits deletes every credential the retired deposit seam
// stored in the credentials file at path, and reports how many there were.
//
// Those entries are copies of a host's permanent session credential. forge no
// longer reads them, so they are dead weight at best and a second copy of a
// bearer secret at worst. A host that used to deposit calls this (best-effort)
// when it next signs in or starts. Entries any other client wrote — including
// a human's `forge login` — are untouched. A missing file removes nothing.
func RemoveLegacyHostDeposits(path string) (int, error) {
	removed := 0
	err := credentials.Update(path, func(f *credentials.File) error {
		for _, endpoint := range f.Endpoints() {
			existed, err := f.Delete(endpoint, legacyHostClientID)
			if err != nil {
				return err
			}
			if existed {
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// Copyright (c) 2026 Reliant Labs

package gtm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/reliant/internal/featuregate"
)

type service struct {
	store     Store
	gate      Gate
	clock     Clock
	analytics Analytics
	email     Email
	// connections, assets and attachments are the r2 collaborators.
	connections Connections
	assets      AssetStore
	attachments Attachments
	// log is where causes withheld from the caller go.
	log *slog.Logger
	// sendTimeout bounds one Email.Send (EmailSendTimeout; tests shorten it).
	sendTimeout time.Duration
}

// New builds the service over deps. Store and Gate are required; with no
// Gate every call is refused.
func New(deps Deps) Service {
	clock := deps.Clock
	if clock == nil {
		clock = wallClock{}
	}
	return &service{
		store:       deps.Store,
		gate:        deps.Gate,
		clock:       clock,
		analytics:   deps.Analytics,
		email:       deps.Email,
		connections: deps.Connections,
		assets:      deps.Assets,
		attachments: deps.Attachments,
		log:         slog.Default(),
		sendTimeout: EmailSendTimeout,
	}
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (s *service) now() time.Time { return s.clock.Now().UTC() }

// admit is rule 1: the actor is whole, and the gate says yes. Every refusal,
// a gate error included, is the bare ErrNotFound — byte for byte what a
// missing row returns (notFound) — and the reason is only logged.
func (s *service) admit(ctx context.Context, a Actor) error {
	if err := a.validate(); err != nil {
		s.log.DebugContext(ctx, "gtm: refused", "reason", err.Error())
		return ErrNotFound
	}
	if s.gate == nil {
		s.log.WarnContext(ctx, "gtm: refused: no gate configured")
		return ErrNotFound
	}
	ok, err := s.gate.Allowed(ctx, featuregate.GTM, a.UserID, a.OrgID)
	if err != nil {
		s.log.WarnContext(ctx, "gtm: refused: gate check failed", "org_id", a.OrgID, "error", err)
		return ErrNotFound
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// notFound is a missing row: the bare ErrNotFound a gate refusal returns,
// so a caller cannot tell "no GTM" from "no such row". Which row is logged
// only. Any other error passes through.
func (s *service) notFound(ctx context.Context, err error, entity, id string) error {
	if errors.Is(err, ErrNotFound) {
		s.log.DebugContext(ctx, "gtm: not found", "entity", entity, "id", id)
		return ErrNotFound
	}
	return err
}

// GetAccess reports the gate's answer; a refusal or an outage is "no".
func (s *service) GetAccess(ctx context.Context, actor Actor) (Access, error) {
	return Access{Enabled: s.admit(ctx, actor) == nil}, nil
}

// --- paging ---

// page resolves a page size and token to a store limit and offset. The
// limit asks for one row more than the page, to tell whether another follows.
func page(size int, token string, def, maxSize int) (limit, offset int, err error) {
	switch {
	case size < 0 || size > maxSize:
		return 0, 0, invalid("page_size", fmt.Sprintf("0 to %d", maxSize))
	case size == 0:
		size = def
	}
	if token != "" {
		raw, decErr := base64.RawURLEncoding.DecodeString(token)
		n, convErr := strconv.Atoi(strings.TrimPrefix(string(raw), "o:"))
		if decErr != nil || convErr != nil || !strings.HasPrefix(string(raw), "o:") || n < 0 {
			return 0, 0, invalid("page_token", "not a page token")
		}
		offset = n
	}
	return size + 1, offset, nil
}

// nextToken trims the extra row page asked for and returns the token of the
// page after, or "" on the last.
func nextToken[T any](rows []T, limit, offset int) ([]T, string) {
	size := limit - 1
	if len(rows) <= size {
		return rows, ""
	}
	return rows[:size], base64.RawURLEncoding.EncodeToString([]byte("o:" + strconv.Itoa(offset+size)))
}

// --- validation ---

func checkLen(field, v string, minLen, maxLen int) error {
	n := utf8.RuneCountInString(v)
	if minLen > 0 && strings.TrimSpace(v) == "" {
		return invalid(field, "required")
	}
	if n > maxLen {
		return invalid(field, fmt.Sprintf("at most %d characters", maxLen))
	}
	return nil
}

func checkURL(field, v string) error {
	if v == "" {
		return nil
	}
	if len(v) > MaxURLLen {
		return invalid(field, fmt.Sprintf("at most %d characters", MaxURLLen))
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return invalid(field, "an http(s) URL")
	}
	return nil
}

func checkEvidence(ev []Evidence) error {
	if len(ev) > MaxEvidence {
		return invalid("evidence", fmt.Sprintf("at most %d entries", MaxEvidence))
	}
	for _, e := range ev {
		for _, err := range []error{
			checkLen("evidence.kind", e.Kind, 0, 100),
			checkLen("evidence.title", e.Title, 0, MaxTitleLen),
			checkURL("evidence.url", e.URL),
			checkLen("evidence.summary_md", e.SummaryMD, 0, MaxShortTextLen),
		} {
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func checkRecipients(to []string) error {
	if len(to) > MaxEmailRecipients {
		return invalid("email_to", fmt.Sprintf("at most %d recipients", MaxEmailRecipients))
	}
	for _, addr := range to {
		// net/mail accepts invisible and bidi format characters (U+202E,
		// U+200B, U+2066, U+FEFF) that make an address read as another.
		// Refused, and with them anything not ASCII: no IDN in the MVP.
		for _, r := range addr {
			if r > unicode.MaxASCII || unicode.Is(unicode.Cf, r) {
				return invalid("email_to", fmt.Sprintf("%q is not a plain ASCII email address", addr))
			}
		}
		a, err := mail.ParseAddress(addr)
		if err != nil || a.Address != addr {
			return invalid("email_to", fmt.Sprintf("%q is not a bare email address", addr))
		}
	}
	return nil
}

// checkSubject is an email subject's rule: at most maxEmailSubjectLen
// characters, with no format character (unicode.Cf: bidi overrides and
// invisible ones, which make a subject read as other than what is sent) and
// no control character (a line break would end the header).
func checkSubject(subject string) error {
	if err := checkLen("email_subject", subject, 0, maxEmailSubjectLen); err != nil {
		return err
	}
	for _, r := range subject {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) {
			return invalid("email_subject", fmt.Sprintf("no invisible, format or control characters (%U)", r))
		}
	}
	return nil
}

func checkRevision(rev int32) error {
	if rev < 1 {
		return invalid("expected_revision", "required")
	}
	return nil
}

func checkID(field, id string) error {
	if id == "" {
		return invalid(field, "required")
	}
	return nil
}

// --- small helpers ---

func ptr[T any](v T) *T { return &v }

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

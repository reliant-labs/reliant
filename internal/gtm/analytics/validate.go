// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// MaxSQLWindow is the longest window a sql template reads: the control
// plane's range cap for sql panels (forge obsquery.MaxRangeDashboards).
// A funnel reads up to gtm.MaxAnalyticsWindow.
const MaxSQLWindow = 30 * 24 * time.Hour

// name is what an event or property name may be. It repeats
// gtm.ValidAnalyticsName's pattern on purpose: what reaches a statement is
// decided here, so loosening the domain's rule cannot loosen this one.
var name = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// validName reports whether s may be emitted as a name literal.
func validName(s string) bool { return name.MatchString(s) }

// identifierWords are the words of a property name that make it an
// identifier: a breakdown by one would list ids, people or secrets.
var identifierWords = map[string]bool{
	"id": true, "ids": true, "uid": true, "uuid": true, "guid": true, "sid": true,
	"user": true, "users": true, "userid": true, "username": true,
	"session": true, "sessionid": true, "visitor": true, "device": true, "anon": true, "anonymous": true,
	"org": true, "email": true, "mail": true, "phone": true, "ip": true,
	"token": true, "key": true, "secret": true, "password": true,
}

// identifierProperty reports whether a property name looks like it names an
// identifier: any of its words (split on _ . : - and on camelCase) is one of
// identifierWords.
func identifierProperty(p string) bool {
	for _, w := range words(p) {
		if identifierWords[w] {
			return true
		}
	}
	return false
}

// words splits a name on its separators and camelCase humps, lowercased:
// "userId" and "user_id" are both [user id], "HTTPStatus" is [http status].
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '.' || r == ':' || r == '-':
			flush()
			continue
		case unicode.IsUpper(r) && len(cur) > 0:
			prevLower := unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || (unicode.IsUpper(rs[i-1]) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// validate checks q again, whatever the caller did: the domain's rules
// (gtm.AnalyticsQuery.Validate), then this package's own: names, the sql
// templates' shorter window, distinct funnel steps and no identifier
// property.
func validate(orgID string, q gtm.AnalyticsQuery) error {
	if strings.TrimSpace(orgID) == "" {
		return ErrNoOrg
	}
	if err := q.Validate(); err != nil {
		return err
	}
	for _, e := range q.Events {
		if !validName(e) {
			return ErrInvalidName
		}
	}
	switch q.Template {
	case gtm.AnalyticsPropertyBreakdown:
		if !validName(q.Property) {
			return ErrInvalidName
		}
		if identifierProperty(q.Property) {
			return ErrIdentifierProperty
		}
	case gtm.AnalyticsFunnel:
		seen := make(map[string]bool, len(q.Events))
		for _, e := range q.Events {
			if seen[e] {
				return ErrDuplicateStep
			}
			seen[e] = true
		}
	}
	if q.Template != gtm.AnalyticsFunnel && q.To.Sub(q.From) > MaxSQLWindow {
		return ErrWindowTooLong
	}
	return nil
}

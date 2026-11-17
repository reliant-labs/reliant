package obsquery

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/reliant-labs/forge/pkg/observe"
)

// A project's own rules for which product events count a user (Activity),
// beyond the convention every project gets (UserActivityEvidence: no sign-out, no
// account snapshot, no observe.TrackState event). A project declares them in
// KCL (forge.ProjectAnalytics.not_activity, .exclude_users_with and
// .exclude_user_ids), forge generate compiles them into the built-in
// dashboards and the Overview's Users card, and `forge obs users` reads the
// same declaration, so the three still agree.
//
// The rules answer two questions, and only two:
//   - which events are not a user's activity: a row marked
//     Props['forge.kind'] = 'state' when it was emitted (observe.TrackState,
//     observe.WithStateEvents; the convention), or a declared name
//     (NotActivity: history, and emitters you do not own);
//   - which users are not counted at all (staff, test accounts, monitors): a
//     user with a declared marker event in the environment (ExcludeUsersWith;
//     how an email or domain rule is expressed, since an event never carries
//     an email), or a declared user id (ExcludeUserIDs).

// MaxActivityEvents bounds each list of event names in Activity.
const MaxActivityEvents = 50

// MaxExcludedUserIDs bounds Activity.ExcludeUserIDs.
const MaxExcludedUserIDs = 100

// activityEventRE is a declared event name: forge.Funnel's step rule
// (kcl/schema.k _ANALYTICS_EVENT_RE). It admits no quote or backslash, so a
// name is safe inside a SQL string literal.
var activityEventRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// Activity is a project's declared user-activity rules.
type Activity struct {
	// NotActivity are product events that name a user without being
	// something the user did (a state snapshot, a marker written by an
	// emitter older than observe.TrackState): their rows never count a user
	// as active or new.
	NotActivity []string `json:",omitempty"`
	// ExcludeUsersWith are marker events (internal_user): a user with any
	// retained row of one, in the same environment, is left out of every
	// user count, history included.
	ExcludeUsersWith []string `json:",omitempty"`
	// ExcludeUserIDs are users left out of every user count by id, history
	// included, in every environment: the opaque subject a product event
	// carries as its UserId (never an email; ValidExcludedUserID). It is for
	// an account that emits no marker (a synthetic monitor's login, a shared
	// test account); a rule that must hold in one environment only is a
	// marker event (ExcludeUsersWith).
	ExcludeUserIDs []string `json:",omitempty"`
}

// IsZero reports whether a declares no rule.
func (a Activity) IsZero() bool {
	return len(a.NotActivity) == 0 && len(a.ExcludeUsersWith) == 0 && len(a.ExcludeUserIDs) == 0
}

// ValidExcludedUserID reports whether id can be an excluded user's id: an
// event's UserId as observe.ValidAuthUserID admits it (not empty, no '@', at
// most observe.MaxAuthUserIDLen bytes), with no surrounding space and no
// control character.
func ValidExcludedUserID(id string) bool {
	if !observe.ValidAuthUserID(id) || strings.TrimSpace(id) != id {
		return false
	}
	return strings.IndexFunc(id, unicode.IsControl) < 0
}

// Validate checks a's names and sizes.
func (a Activity) Validate() error {
	for _, l := range []struct {
		field string
		names []string
	}{{"not_activity", a.NotActivity}, {"exclude_users_with", a.ExcludeUsersWith}} {
		if len(l.names) > MaxActivityEvents {
			return invalid("%s names %d events: at most %d", l.field, len(l.names), MaxActivityEvents)
		}
		for _, n := range l.names {
			if !activityEventRE.MatchString(n) {
				return invalid("%s: %q is not a product event name", l.field, n)
			}
		}
	}
	if len(a.ExcludeUserIDs) > MaxExcludedUserIDs {
		return invalid("exclude_user_ids names %d users: at most %d", len(a.ExcludeUserIDs), MaxExcludedUserIDs)
	}
	for _, id := range a.ExcludeUserIDs {
		if !ValidExcludedUserID(id) {
			return invalid("exclude_user_ids: %q is not a user id (the opaque subject an event carries: no '@', no surrounding space, at most %d characters)", id, observe.MaxAuthUserIDLen)
		}
	}
	return nil
}

// Merge is the union of a and b, each list sorted and de-duplicated: an
// event any environment declares is a rule for the project.
func (a Activity) Merge(b Activity) Activity {
	return Activity{
		NotActivity:      union(a.NotActivity, b.NotActivity),
		ExcludeUsersWith: union(a.ExcludeUsersWith, b.ExcludeUsersWith),
		ExcludeUserIDs:   union(a.ExcludeUserIDs, b.ExcludeUserIDs),
	}
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string{}, a...), b...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ActiveEvidenceFor is UserActivityEvidence under a's rules, over table (the
// product_events table the statement reads, for the exclusion's subquery:
// "product_events" or "<database>.product_events", each part an identifier,
// else ErrInvalid). With no rule it is UserActivityEvidence itself. A name in a
// that is not an event name, or an id that is not a user id, never reaches the
// SQL (Validate reports it); an id is written as an escaped string literal.
func ActiveEvidenceFor(table string, a Activity) (string, error) {
	if !validTable(table) {
		return "", invalid("table %q is not an identifier or database.identifier", table)
	}
	out := UserActivityEvidence
	if l := sqlList(a.NotActivity); l != "" {
		out += " AND EventName NOT IN (" + l + ")"
	}
	if l := sqlList(a.ExcludeUsersWith); l != "" {
		out += " AND (EnvId, UserId) NOT IN (SELECT EnvId, UserId FROM " + table +
			" WHERE EventName IN (" + l + ") AND UserId != '')"
	}
	if l := userIDList(a.ExcludeUserIDs); l != "" {
		out += " AND UserId NOT IN (" + l + ")"
	}
	return out, nil
}

// NewUsersSourceFor is NewUsersSource under a's rules: its users are first
// seen, and their sign-ups counted, only on rows ActiveEvidenceFor keeps.
func NewUsersSourceFor(table, to, scope string, a Activity) (string, error) {
	evidence, err := ActiveEvidenceFor(table, a)
	if err != nil {
		return "", err
	}
	return newUsersSource(table, to, scope, evidence), nil
}

// validTable reports whether table is an identifier, or two joined by a dot.
func validTable(table string) bool {
	db, name, qualified := strings.Cut(table, ".")
	if !qualified {
		return IsIdentifier(table)
	}
	return IsIdentifier(db) && IsIdentifier(name)
}

// sqlList is names as SQL string literals, comma-separated. A name that is
// not a product event name is skipped (Validate reports it), so no value
// reaches the statement unquoted; "" when none is left.
func sqlList(names []string) string {
	q := make([]string, 0, len(names))
	for _, n := range names {
		if activityEventRE.MatchString(n) {
			q = append(q, "'"+n+"'")
		}
	}
	return strings.Join(q, ", ")
}

// userIDList is ids as escaped SQL string literals, comma-separated. An id
// that is not a user id is skipped (Validate reports it); "" when none is
// left.
func userIDList(ids []string) string {
	q := make([]string, 0, len(ids))
	for _, id := range ids {
		if ValidExcludedUserID(id) {
			q = append(q, chStringLit(id))
		}
	}
	return strings.Join(q, ", ")
}

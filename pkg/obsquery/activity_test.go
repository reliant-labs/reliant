package obsquery

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustEvidence(t *testing.T, table string, a Activity) string {
	t.Helper()
	got, err := ActiveEvidenceFor(table, a)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustNewUsers(t *testing.T, table, to, scope string, a Activity) string {
	t.Helper()
	got, err := NewUsersSourceFor(table, to, scope, a)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestActiveEvidenceForWithoutRulesIsTheConvention(t *testing.T) {
	if got := mustEvidence(t, "product_events", Activity{}); got != UserActivityEvidence {
		t.Errorf("ActiveEvidenceFor(zero) = %q, want UserActivityEvidence", got)
	}
	if !strings.Contains(UserActivityEvidence, "Props['forge.kind'] != 'state'") {
		t.Errorf("UserActivityEvidence %q does not leave observe.TrackState events out", UserActivityEvidence)
	}
	if got := mustNewUsers(t, "product_events", "{to:DateTime64(9)}", "", Activity{}); got != NewUsersSource("product_events", "{to:DateTime64(9)}", "") {
		t.Error("NewUsersSourceFor(zero) differs from NewUsersSource")
	}
}

func TestActiveEvidenceForCompilesTheRules(t *testing.T) {
	a := Activity{NotActivity: []string{"billing_subscription_state", "claimable_account_created"}, ExcludeUsersWith: []string{"internal_user"}}
	got := mustEvidence(t, "t_org.product_events", a)
	for _, want := range []string{
		UserActivityEvidence + " AND ",
		"EventName NOT IN ('billing_subscription_state', 'claimable_account_created')",
		"(EnvId, UserId) NOT IN (SELECT EnvId, UserId FROM t_org.product_events WHERE EventName IN ('internal_user') AND UserId != '')",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ActiveEvidenceFor = %q, want it to contain %q", got, want)
		}
	}
	src := mustNewUsers(t, "t_org.product_events", "{to:DateTime64(9)}", "", a)
	if !strings.Contains(src, "WHERE "+got+" AND Timestamp") {
		t.Errorf("NewUsersSourceFor does not read the rules' evidence:\n%s", src)
	}
}

func TestActivityRefusesNamesThatAreNotEvents(t *testing.T) {
	bad := Activity{NotActivity: []string{"ok_event", "x') OR 1=1 --"}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate(%v) = %v, want ErrInvalid", bad, err)
	}
	// Unvalidated, the bad name never reaches the SQL.
	if got := mustEvidence(t, "product_events", bad); strings.Contains(got, "1=1") || !strings.Contains(got, "'ok_event'") {
		t.Errorf("ActiveEvidenceFor(unvalidated) = %q", got)
	}
	if got := mustEvidence(t, "product_events", Activity{ExcludeUsersWith: []string{"'"}}); got != UserActivityEvidence {
		t.Errorf("a rule with no valid name must add no clause: %q", got)
	}
	long := Activity{ExcludeUsersWith: make([]string, MaxActivityEvents+1)}
	for i := range long.ExcludeUsersWith {
		long.ExcludeUsersWith[i] = "e"
	}
	if err := long.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Validate(%d names) = %v, want ErrInvalid", len(long.ExcludeUsersWith), err)
	}
}

func TestActivityMergeIsTheSortedUnion(t *testing.T) {
	got := Activity{NotActivity: []string{"b", "a"}}.Merge(Activity{NotActivity: []string{"a", "c"}, ExcludeUsersWith: []string{"internal_user", ""}})
	want := Activity{NotActivity: []string{"a", "b", "c"}, ExcludeUsersWith: []string{"internal_user"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Merge = %+v, want %+v", got, want)
	}
	if !(Activity{}).Merge(Activity{}).IsZero() {
		t.Error("the union of no rules has rules")
	}
}

func TestUserActivityCompilesTheDeclaredRules(t *testing.T) {
	q := userActivityQuery()
	q.Activity = Activity{NotActivity: []string{"billing_subscription_state"}, ExcludeUsersWith: []string{"internal_user"}}
	st, err := q.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(st.SQL, "EventName IN ('internal_user')"); n != 6 {
		t.Errorf("the exclusion appears %d times, want 6 (act, seen, nu_cur, nu_prev, nd_cur, nd_prev):\n%s", n, st.SQL)
	}
	q.Activity = Activity{NotActivity: []string{"no quotes'"}}
	if _, err := q.Compile(); !errors.Is(err, ErrInvalid) {
		t.Errorf("Compile with a bad rule = %v, want ErrInvalid", err)
	}
}

// The table is an identifier or database.identifier; anything else is refused,
// so a caller's table can never carry SQL into the statement.
func TestActiveEvidenceForRefusesATableThatIsNotAnIdentifier(t *testing.T) {
	rules := Activity{ExcludeUsersWith: []string{"internal_user"}}
	for _, table := range []string{
		"product_events WHERE 1=1) OR (1", "t_org.product_events; DROP TABLE x", "a.b.c", "", ".product_events",
		"t_org.", "`product_events`", "product events",
	} {
		if got, err := ActiveEvidenceFor(table, rules); !errors.Is(err, ErrInvalid) || got != "" {
			t.Errorf("ActiveEvidenceFor(%q) = %q, %v; want ErrInvalid and no SQL", table, got, err)
		}
		if got, err := NewUsersSourceFor(table, "{to:DateTime64(9)}", "", rules); !errors.Is(err, ErrInvalid) || got != "" {
			t.Errorf("NewUsersSourceFor(%q) = %q, %v; want ErrInvalid and no SQL", table, got, err)
		}
	}
	for _, table := range []string{"product_events", "t_org.product_events", "default.product_events"} {
		if _, err := ActiveEvidenceFor(table, rules); err != nil {
			t.Errorf("ActiveEvidenceFor(%q) = %v, want accepted", table, err)
		}
	}
}

// ActiveEvidence is a published constant: its value is frozen at what it was
// before UserActivityEvidence (semver at major 1), and the new predicate
// extends it rather than replacing it.
func TestDeprecatedActiveEvidenceKeepsItsValue(t *testing.T) {
	const was = "UserId != '' AND EventName NOT IN ('auth_signed_out', 'auth_accounts')"
	if ActiveEvidence != was {
		t.Errorf("ActiveEvidence = %q, want its published value %q", ActiveEvidence, was)
	}
	if !strings.HasPrefix(UserActivityEvidence, ActiveEvidence+" AND ") {
		t.Errorf("UserActivityEvidence %q does not extend ActiveEvidence", UserActivityEvidence)
	}
}

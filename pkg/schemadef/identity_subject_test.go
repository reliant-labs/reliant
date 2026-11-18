package schemadef

import (
	"slices"
	"strings"
	"testing"
)

func TestColumnIdentitySubject(t *testing.T) {
	for _, tc := range []struct {
		comment  string
		wantKind string
		wantOK   bool
	}{
		{"", "", false},
		{"the Zitadel user id", "", false},
		{"forge:identity-subject", IdentitySubjectID, true},
		{"forge:identity-subject — the Zitadel user id (the token's sub)", IdentitySubjectID, true},
		{"forge:identity-subject=id", IdentitySubjectID, true},
		{"forge:identity-subject=email", IdentitySubjectEmail, true},
		{"forge:identity-subject=email — matched by login", IdentitySubjectEmail, true},
		{"login. forge:identity-subject=email", IdentitySubjectEmail, true},
		{"forge:immutable forge:identity-subject=email", IdentitySubjectEmail, true},
		{"forge:identity-subject forge:identity-subject=id", IdentitySubjectID, true},
		// Whole token: a longer marker that merely begins with this one is not it.
		{"forge:identity-subjects", "", false},
		{"forge:identity-subject-x", "", false},
		// A bad argument, or two that disagree, is declared but not usable.
		{"forge:identity-subject=uid", "", false},
		{"forge:identity-subject=Email", "", false},
		{"forge:identity-subject=", "", false},
		{"forge:identity-subject forge:identity-subject=email", "", false},
	} {
		kind, ok := Column{Name: "c", Comment: tc.comment}.IdentitySubject()
		if kind != tc.wantKind || ok != tc.wantOK {
			t.Errorf("IdentitySubject(%q) = (%q, %v), want (%q, %v)", tc.comment, kind, ok, tc.wantKind, tc.wantOK)
		}
	}
}

func TestIdentitySubjectIsAColumnMarker(t *testing.T) {
	if !slices.Contains(KnownColumnMarkers, ColumnMarkerIdentitySubject) {
		t.Fatal("forge:identity-subject is not in KnownColumnMarkers")
	}
	if !slices.Contains(SiteMarkers(SiteColumn), ColumnMarkerIdentitySubject) {
		t.Error("forge:identity-subject is not a column-site marker")
	}
	for _, site := range []MarkerSite{SiteTable, SiteView, SiteConstraint} {
		if slices.Contains(SiteMarkers(site), ColumnMarkerIdentitySubject) {
			t.Errorf("forge:identity-subject is in the %s vocabulary; only a column is a subject", site)
		}
	}
}

func TestCommentMarkerProblemsIdentitySubject(t *testing.T) {
	for _, tc := range []struct {
		site    MarkerSite
		comment string
		want    []string // a substring of each problem, in order; empty for none
	}{
		{SiteColumn, "forge:identity-subject", nil},
		{SiteColumn, "forge:identity-subject=id", nil},
		{SiteColumn, "forge:identity-subject=email — matched by login", nil},
		{SiteColumn, "forge:identity-subject forge:identity-subject", nil},
		{SiteColumn, "forge:identity-subject=uid", []string{"forge:identity-subject=uid names no attribute", "=email for the login email"}},
		{SiteColumn, "forge:identity-subject=", []string{"names no attribute"}},
		// Two bad declarations are one problem, not one per token.
		{SiteColumn, "forge:identity-subject=a forge:identity-subject=b", []string{"forge:identity-subject=a names no attribute"}},
		{SiteColumn, "forge:identity-subject=email forge:identity-subject=id", []string{"declared twice, once as email and once as id"}},
		// Typos are offered the marker.
		{SiteColumn, "forge:identity-subjct", []string{"(did you mean forge:identity-subject?)"}},
		{SiteColumn, "forge:identity-subjects=email", []string{"is not a marker forge reads", "(did you mean forge:identity-subject?)"}},
		// Anywhere but a column, it does nothing.
		{SiteTable, "forge:identity-subject", []string{"is a column marker and does nothing on a table"}},
		{SiteConstraint, "forge:identity-subject", []string{"is a column marker and does nothing on a constraint"}},
	} {
		got := CommentMarkerProblems(tc.site, "external_id", tc.comment)
		if len(tc.want) == 0 {
			if len(got) != 0 {
				t.Errorf("%s %q: unexpected problems %+v", tc.site, tc.comment, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Errorf("%s %q: want 1 problem, got %+v", tc.site, tc.comment, got)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got[0].Problem, w) {
				t.Errorf("%s %q: problem %q does not say %q", tc.site, tc.comment, got[0].Problem, w)
			}
		}
	}
}

// generate refuses a bad declaration through CheckMarkers, by the column.
func TestCheckMarkersRefusesABadIdentitySubject(t *testing.T) {
	good := Table{Name: "users", Columns: []Column{{Name: "external_id", Comment: "forge:identity-subject=email"}}}
	if err := CheckMarkers([]Table{good}); err != nil {
		t.Fatalf("a valid declaration fails CheckMarkers: %v", err)
	}
	bad := Table{Name: "users", Columns: []Column{{Name: "external_id", Comment: "forge:identity-subject=sub"}}}
	err := CheckMarkers([]Table{bad})
	if err == nil || !strings.Contains(err.Error(), "column users.external_id: forge:identity-subject=sub names no attribute") {
		t.Fatalf("CheckMarkers = %v, want the bad argument named on the column", err)
	}
}

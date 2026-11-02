package schemadef

import (
	"reflect"
	"strings"
	"testing"
)

func softDeleteTable(deletedAtComment string) Table {
	return Table{
		Name:   "worktrees",
		PKCols: []string{"id"},
		Columns: []Column{
			{Name: "id", Type: TypeString, IsPK: true, NotNull: true},
			{Name: "branch", Type: TypeString, NotNull: true},
			{Name: "deleted_at", Type: TypeTime, Comment: deletedAtComment},
		},
	}
}

// The default is unchanged: a time-typed deleted_at is soft delete.
func TestDetectConventions_DeletedAtIsSoftDeleteByDefault(t *testing.T) {
	if !DetectConventions(softDeleteTable("")).SoftDelete {
		t.Fatal("a time-typed deleted_at with no declaration must stay soft delete")
	}
	if !DetectConventions(softDeleteTable("when the row was tombstoned")).SoftDelete {
		t.Fatal("prose in the comment is not a declaration")
	}
}

// Declared, deleted_at is an ordinary column. Fails on the old code, which
// read soft delete off the column's name and type alone.
func TestDetectConventions_NoSoftDeleteDeclaration(t *testing.T) {
	for _, comment := range []string{
		"forge:no-soft-delete",
		"forge:no-soft-delete — the archive stamp; archived worktrees stay readable",
		"archive stamp. forge:no-soft-delete",
	} {
		if DetectConventions(softDeleteTable(comment)).SoftDelete {
			t.Errorf("deleted_at commented %q is still soft delete", comment)
		}
	}
	// A longer marker that merely begins with this one is not it.
	if !DetectConventions(softDeleteTable("forge:no-soft-delete-ish")).SoftDelete {
		t.Error("forge:no-soft-delete-ish must not read as forge:no-soft-delete")
	}
}

func TestMarkerTokens(t *testing.T) {
	for comment, want := range map[string][]string{
		"":                                  nil,
		"plain prose":                       nil,
		"forge: is the generator":           nil,
		"forge:fill=ulid":                   {"forge:fill"},
		"forge:immutable forge:owner":       {"forge:immutable", "forge:owner"},
		"forge:ref derived-from=job_id":     {"forge:ref"},
		"forge:go-type=map[string]any":      {"forge:go-type"},
		"see forge:Immutable":               {"forge:Immutable"},
		"https://x/forge:y and xforge:z":    nil,
		"forge:entity key=id — the row":     {"forge:entity"},
		"forge:no-soft-delete — archive at": {"forge:no-soft-delete"},
	} {
		if got := MarkerTokens(comment); !reflect.DeepEqual(got, want) {
			t.Errorf("MarkerTokens(%q) = %q, want %q", comment, got, want)
		}
	}
}

func TestCommentMarkerProblems(t *testing.T) {
	cases := []struct {
		site    MarkerSite
		column  string
		comment string
		want    string // substring of the one problem; "" for none
	}{
		{SiteColumn, "deleted_at", "forge:no-soft-delete", ""},
		{SiteColumn, "total", "forge:immutable forge:fill=handler", ""},
		{SiteColumn, "kind", "forge:go-type=github.com/a/b;b.Kind", ""},
		{SiteConstraint, "", "forge:ref authoritative", ""},
		{SiteTable, "", "forge:append-only — a ledger", ""},
		{SiteTable, "", "forge:entity-name=Widget", ""},
		{SiteView, "", "forge:entity key=id", ""},
		{SiteColumn, "notes", "free-text notes, nothing special", ""},

		{SiteColumn, "total", "forge:immutible", "did you mean forge:immutable?"},
		{SiteColumn, "deleted_at", "forge:no-softdelete", "did you mean forge:no-soft-delete?"},
		{SiteColumn, "archived_at", "forge:no-soft-delete", "only applies to the deleted_at column"},
		{SiteColumn, "id", "forge:append-only", "is a table marker"},
		{SiteTable, "", "forge:immutable", "is a column marker"},
		{SiteColumn, "patient_id", "forge:ref authoritative", "is a constraint marker"},
		{SiteTable, "", "forge:entity", "is a view marker"},
		{SiteConstraint, "", "forge:refactor later", "not a marker forge reads"},
	}
	for _, c := range cases {
		got := CommentMarkerProblems(c.site, c.column, c.comment)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s %q: unexpected problems %+v", c.site, c.comment, got)
			}
			continue
		}
		if len(got) != 1 || !strings.Contains(got[0].Problem, c.want) {
			t.Errorf("%s %q: got %+v, want one problem containing %q", c.site, c.comment, got, c.want)
		}
	}
}

func TestCheckMarkers_NamesEveryProblemWithItsObject(t *testing.T) {
	tables := []Table{
		softDeleteTable("forge:no-soft-delete"),
		{
			Name:    "invoices",
			Comment: "forge:append-only forge:appendonly",
			Columns: []Column{{Name: "total", Comment: "forge:immutible"}},
			ForeignKeys: []ForeignKey{{
				Name: "invoices_customer_id_fkey", Column: "customer_id", Comment: "forge:ref authoritative",
			}},
		},
		{Name: "v", IsView: true, Comment: "forge:no-entity"},
	}
	err := CheckMarkers(tables)
	if err == nil {
		t.Fatal("CheckMarkers accepted markers that do nothing")
	}
	for _, want := range []string{
		"table invoices: forge:appendonly is not a marker",
		"column invoices.total: forge:immutible",
		"view v: forge:no-entity is a table marker",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "worktrees") || strings.Contains(err.Error(), "constraint") {
		t.Errorf("valid declarations reported:\n%v", err)
	}
	if err := CheckMarkers([]Table{softDeleteTable("forge:no-soft-delete")}); err != nil {
		t.Errorf("a valid schema is rejected: %v", err)
	}
}

// forge:closed-set is a constraint marker read on a CHECK: the generate that
// reads the catalog accepts it there, rejects it on a foreign key, rejects
// forge:ref on a CHECK, and rejects a misspelling with a suggestion.
func TestCheckMarkers_ClosedSetOnCheckConstraints(t *testing.T) {
	table := func(checkComment, fkComment string) []Table {
		return []Table{{
			Name:        "access_tokens",
			Checks:      []CheckConstraint{{Name: "access_tokens_scopes", Comment: checkComment}},
			ForeignKeys: []ForeignKey{{Name: "access_tokens_org_fkey", Column: "org_id", Comment: fkComment}},
		}}
	}
	if err := CheckMarkers(table("forge:closed-set accesstoken.AllScopes", "forge:ref authoritative")); err != nil {
		t.Fatalf("closed-set on a CHECK and ref on an FK are both read: %v", err)
	}
	for name, tc := range map[string]struct{ check, fk, want string }{
		"closed-set on a foreign key": {"", "forge:closed-set accesstoken.AllScopes", "constraint access_tokens_org_fkey on access_tokens: forge:closed-set is read on a CHECK constraint"},
		"ref on a check":              {"forge:ref authoritative", "", "constraint access_tokens_scopes on access_tokens: forge:ref is read on a FOREIGN KEY constraint"},
		"misspelled":                  {"forge:closed-sets accesstoken.AllScopes", "", "did you mean forge:closed-set?"},
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckMarkers(table(tc.check, tc.fk))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestColumnSecretAndReadableDeclarations(t *testing.T) {
	secret := func(c string) bool { return Column{Comment: c}.DeclaresSecret() }
	for comment, want := range map[string]bool{
		"forge:secret":                    true,
		"forge:secret — a sealed token":   true,
		"credential. forge:secret":        true,
		"":                                false,
		"a secret column":                 false,
		"forge:secret-ish":                false,
		"forge:readable not forge:secret": true,
	} {
		if got := secret(comment); got != want {
			t.Errorf("DeclaresSecret(%q) = %v, want %v", comment, got, want)
		}
	}

	for comment, want := range map[string]struct {
		reason   string
		declared bool
	}{
		"forge:readable — content hash, not a credential": {"content hash, not a credential", true},
		"forge:readable: public key":                      {"public key", true},
		"forge:readable":                                  {"", true},
		"forge:readable —":                                {"", true},
		"forge:readable forge:secret":                     {"", true},
		"forge:readable-ish because":                      {"", false},
		"no marker here":                                  {"", false},
	} {
		reason, declared := Column{Comment: comment}.ReadableReason()
		if reason != want.reason || declared != want.declared {
			t.Errorf("ReadableReason(%q) = (%q, %v), want (%q, %v)", comment, reason, declared, want.reason, want.declared)
		}
	}
}

// A forge:readable with no reason is rejected where it is declared: the reason
// is the review record of the exception.
func TestCheckMarkers_ReadableNeedsAReason(t *testing.T) {
	tables := func(comment string) []Table {
		return []Table{{Name: "git_credentials", Columns: []Column{{Name: "token_hash", Comment: comment}}}}
	}
	if err := CheckMarkers(tables("forge:readable — content hash of the clone URL")); err != nil {
		t.Errorf("a readable with a reason is rejected: %v", err)
	}
	if err := CheckMarkers(tables("forge:secret")); err != nil {
		t.Errorf("forge:secret on a column is rejected: %v", err)
	}
	err := CheckMarkers(tables("forge:readable"))
	if err == nil || !strings.Contains(err.Error(), "needs a reason") {
		t.Errorf("a readable with no reason: err = %v, want a 'needs a reason' problem", err)
	}
	// Only a column comment carries them.
	for _, m := range []string{ColumnMarkerSecret, ColumnMarkerReadable} {
		if problems := CommentMarkerProblems(SiteTable, "", m+" — x"); len(problems) != 1 {
			t.Errorf("%s on a table comment: %d problems, want 1 (it does nothing there)", m, len(problems))
		}
	}
}

// The dashboard markers are retired: the dashboard role reads what migrations
// grant it. A comment still carrying one fails the generate that reads it, on
// every object, and says how to migrate instead of suggesting a spelling.
func TestCheckMarkers_RetiredDashboardMarkersSayHowToMigrate(t *testing.T) {
	for name, tables := range map[string][]Table{
		"forge:dashboard on a table":     {{Name: "t", Comment: "forge:append-only forge:dashboard"}},
		"forge:dashboard on a view":      {{Name: "v", IsView: true, Comment: "forge:dashboard"}},
		"forge:no-dashboard on a column": {{Name: "t", Columns: []Column{{Name: "c", Comment: "forge:secret forge:no-dashboard"}}}},
	} {
		err := CheckMarkers(tables)
		if err == nil || !strings.Contains(err.Error(), "retired in forge v1.0.13") ||
			!strings.Contains(err.Error(), "GRANT") || !strings.Contains(err.Error(), "migrations/v1.0.13") {
			t.Errorf("%s: err = %v, want the retirement and how to migrate", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "did you mean") {
			t.Errorf("%s: a retired marker is offered a spelling: %v", name, err)
		}
	}
	// Its neighbours are still read.
	if !(Table{Comment: "forge:append-only forge:dashboard"}).AppendOnly() {
		t.Error("a retired marker hides a neighbouring table marker")
	}
}

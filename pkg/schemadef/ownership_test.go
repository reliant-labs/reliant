package schemadef

import (
	"reflect"
	"strings"
	"testing"
)

func ownershipFixture() []Table {
	col := func(names ...string) []Column {
		var cs []Column
		for _, n := range names {
			cs = append(cs, Column{Name: n})
		}
		return cs
	}
	return []Table{
		{Name: "chats", Columns: col("id", "user_id")},
		{Name: "threads", Columns: col("id", "chat_id"), ForeignKeys: []ForeignKey{{Column: "chat_id", RefTable: "chats", RefColumn: "id", Name: "threads_chat_id_fkey"}}},
		{Name: "plans", Comment: "forge:owner=thread_id>chat_id>user_id", Columns: col("id", "thread_id"), ForeignKeys: []ForeignKey{{Column: "thread_id", RefTable: "threads", RefColumn: "id", Name: "plans_thread_id_fkey"}}},
		{Name: "accounts", Comment: "Provider accounts. forge:owner=user:user_id,org:org_id forge:append-only", Columns: col("id", "user_id", "org_id")},
	}
}

func TestResolveOwnership_JoinPath(t *testing.T) {
	tables := ownershipFixture()
	terms, declared, err := ResolveOwnership(tables[2], tables)
	if err != nil || !declared {
		t.Fatalf("ResolveOwnership = %v, %v", declared, err)
	}
	want := []ResolvedOwnerTerm{{
		Kind: "user", Column: "thread_id", Constraint: "plans_thread_id_fkey",
		Hops: []OwnerHop{{Table: "threads", Key: "id", Column: "chat_id"}, {Table: "chats", Key: "id", Column: "user_id"}},
	}}
	if !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %+v\nwant   %+v", terms, want)
	}
}

func TestResolveOwnership_Alternatives(t *testing.T) {
	tables := ownershipFixture()
	terms, declared, err := ResolveOwnership(tables[3], tables)
	if err != nil || !declared {
		t.Fatalf("ResolveOwnership = %v, %v", declared, err)
	}
	want := []ResolvedOwnerTerm{{Kind: "user", Column: "user_id"}, {Kind: "org", Column: "org_id"}}
	if !reflect.DeepEqual(terms, want) {
		t.Fatalf("terms = %+v, want %+v", terms, want)
	}
}

func TestResolveOwnership_Undeclared(t *testing.T) {
	tables := ownershipFixture()
	// The COLUMN form of the marker is not a table declaration.
	tables[0].Comment = "forge:owner"
	if _, declared, err := ResolveOwnership(tables[0], tables); declared || err != nil {
		t.Fatalf("bare forge:owner on a table read as a declaration: %v, %v", declared, err)
	}
}

func TestResolveOwnership_Errors(t *testing.T) {
	for _, tc := range []struct{ comment, want string }{
		{"forge:owner=", "names no owner"},
		{"forge:owner=Org:user_id", "owner kind"},
		{"forge:owner=user_id>", "not a column name"},
		{"forge:owner=nope", "has no column nope"},
		{"forge:owner=id>user_id", "not a single-column foreign key"},
		{"forge:owner=user_id,user_id", "declared twice"},
	} {
		tables := ownershipFixture()
		tables[2].Comment = tc.comment
		_, declared, err := ResolveOwnership(tables[2], tables)
		if !declared || err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: declared=%v err=%v, want an error containing %q", tc.comment, declared, err, tc.want)
		}
	}
}

func TestScopeExemption(t *testing.T) {
	for _, tc := range []struct{ comment, marker, reason string }{
		{"forge:public: the pricing page lists every plan", TableMarkerPublic, "the pricing page lists every plan"},
		{"forge:unscoped — admin service only forge:append-only", TableMarkerUnscoped, "admin service only"},
		{"forge:unscoped", TableMarkerUnscoped, ""},
		{"forge:publicity", "", ""},
	} {
		mk, reason, ok := Table{Comment: tc.comment}.ScopeExemption()
		if mk != tc.marker || reason != tc.reason || ok != (tc.marker != "") {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q)", tc.comment, mk, reason, ok, tc.marker, tc.reason)
		}
	}
}

func TestCheckMarkers_Ownership(t *testing.T) {
	tables := ownershipFixture()
	tables = append(tables, Table{Name: "flags", Comment: "forge:unscoped: admin service only", Columns: []Column{{Name: "id"}}})
	if err := CheckMarkers(tables); err != nil {
		t.Fatalf("valid ownership declarations refused: %v", err)
	}
	for _, tc := range []struct{ comment, want string }{
		{"forge:owner", "must name the owner"},
		{"forge:owner=nope", "has no column nope"},
		{"forge:owner=Bad:x", "owner kind"},
	} {
		bad := ownershipFixture()
		bad[2].Comment = tc.comment
		err := CheckMarkers(bad)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: CheckMarkers = %v, want an error containing %q", tc.comment, err, tc.want)
		}
	}
}

// An exemption the author writes beside another table marker (an
// append-only ledger keeps its marker: COMMENT ON replaces the whole
// comment) must parse as an unscoped reason and as nothing else:
// CheckMarkers reads every forge:* token in the comment.
func TestCheckMarkers_UnscopedBesideAnotherMarker(t *testing.T) {
	for _, c := range []string{
		"forge:unscoped: written only by the billing worker; no caller reads it",
		"forge:append-only — rows are inserted and read, never rewritten or erased. forge:unscoped: written only by the billing worker; no caller reads it",
	} {
		tb := Table{Name: "orders", Comment: c, Columns: []Column{{Name: "id"}}}
		if err := CheckMarkers([]Table{tb}); err != nil {
			t.Errorf("%q: %v", c, err)
		}
		if mk, reason, ok := tb.ScopeExemption(); !ok || mk != TableMarkerUnscoped || reason == "" {
			t.Errorf("%q: ScopeExemption = %q, %q, %v", c, mk, reason, ok)
		}
	}
}

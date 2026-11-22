package codegen

import "testing"

// The generated pages follow the same declaration the store does. A
// soft-delete deleted_at is machinery — reads filter it, so its cell is
// always empty — and stays off the pages. One declared forge:no-soft-delete
// is an archive stamp on rows the list DOES show, so it is a column like any
// other, on the list and the detail page.
func TestAttachEntityMeta_DeletedAtColumnFollowsTheSoftDeleteDeclaration(t *testing.T) {
	fields := []EntityField{
		{Name: "id", ProtoType: "string", Kind: FieldKindScalar},
		{Name: "branch", ProtoType: "string", Kind: FieldKindScalar},
		{Name: "deleted_at", ProtoType: "google.protobuf.Timestamp", Kind: FieldKindScalar},
	}
	has := func(cols []EntityPageField) bool {
		for _, c := range cols {
			if c.Name == "deletedAt" {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		softDelete bool
		want       bool
	}{{softDelete: true, want: false}, {softDelete: false, want: true}} {
		var page PageTemplateData
		AttachEntityMeta(&page, EntityDef{Name: "Worktree", PkField: "id", SoftDelete: tc.softDelete, Fields: fields}, ServiceDef{})
		if got := has(page.Columns); got != tc.want {
			t.Errorf("SoftDelete=%v: deletedAt on the detail columns = %v, want %v", tc.softDelete, got, tc.want)
		}
		if got := has(page.ListColumns); got != tc.want {
			t.Errorf("SoftDelete=%v: deletedAt on the list columns = %v, want %v", tc.softDelete, got, tc.want)
		}
	}
}

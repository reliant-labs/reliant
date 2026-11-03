// File: internal/codegen/read_only_columns.go
//
// READ-ONLY COLUMNS — the columns a client may not write through the
// generated CRUD Update.
//
// `// forge:read-only` and `// forge:computed` promise "readable, not
// client-writable", and the born Create request keeps that promise by
// omitting the field. The AIP-134 Update request cannot: it carries the
// WHOLE entity, read-only fields included. Before this projection the
// generated Update wrote them all back, so a client could set a server-owned
// column (an invoice straight to PAID, past the RPC that owns the state
// machine) and, worse because nothing announces it, a client that merely did
// not echo one reset it — an unset enum to the column DEFAULT, a lifecycle
// timestamp to NULL, a computed total to 0.
//
// # Why the proto, and not a migration declaration
//
// Whether a COLUMN may be rewritten is storage truth and is declared in the
// migration (forge:immutable, projected to ,skipupdate). This is a different
// question — may a CLIENT write it through the API — and its answer is the
// wire marker the author already wrote. It is read from the compiled
// descriptor on every generate (forge_descriptor.go's fieldHasReadOnlyMarker),
// the same reading that already gates generate (FindUnsatisfiableColumns) and
// drives the scaffolded edit form; it is not a birth-only instruction.
//
// Projecting it onto storage instead would be wrong twice over. ,skipupdate
// would take the write away from the custom RPCs that own these columns and
// write them through the same repository (a full-replace Update<Entity> in
// app code would silently stop writing status), and a second declaration in
// the migration could drift from the marker that shapes the Create request
// and the edit form — one field, two answers to "may the client write it".
package codegen

// ReadOnlyColumns returns, in column order, every column of entity whose
// wire field is marked `forge:read-only` or `forge:computed` (EntityField
// .ReadOnly covers both — see ReadOnlyProtoMarkers). The primary key is
// left out: it addresses the row rather than being written by it.
//
// A read-only wire field with no column is not a column anyone can write,
// and a column with no wire field is not one a client can name, so both are
// outside the answer.
func ReadOnlyColumns(entity EntityDef) []string {
	readOnly := make(map[string]bool, len(entity.Fields))
	for _, f := range entity.Fields {
		if f.ReadOnly {
			readOnly[f.Name] = true
		}
	}
	var out []string
	for _, col := range entity.Columns {
		if readOnly[col.Name] && !col.IsPK && col.Name != entity.PkField {
			out = append(out, col.Name)
		}
	}
	return out
}

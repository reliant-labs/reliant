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

// UnmaskableColumns returns, in column order, the columns of entity a CLIENT's
// update_mask may not name although their wire field is not read-only — the
// generated UpdateOp's Unmaskable, which HandleUpdate refuses exactly as it
// refuses ReadOnly:
//
//   - a `forge:immutable` column. A full replace never rewrites it
//     (,skipupdate), so the only client write is Create; a mask naming it
//     would rewrite what the full replace keeps.
//   - a column with no wire field (a SQL-spelled `forge:secret`, an internal
//     stamp). The client cannot send its value, so a mask naming it could
//     only reset it to the zero the request was left holding.
//
// The primary key and the read-only columns are left out: the repository
// refuses the first, ReadOnlyColumns covers the second. A wire field marked
// `// forge:secret` is on the wire, so it stays mask-writable: that is how a
// client rotates a write-only credential. An entity with no wire fields —
// whether its message is empty or not declared at all — lets a client send
// no column, so every non-key, non-read-only column is reported: failing
// closed is the only answer that keeps a mask from writing a column the
// request cannot carry.
//
// These are not passed to crud.Preserve. Whether a full replace writes a
// column is storage's call (,skipupdate) for the immutable ones, and a column
// with no wire field is the app's to set from an op.Entity hook.
func UnmaskableColumns(entity EntityDef) []string {
	wire := make(map[string]bool, len(entity.Fields))
	readOnly := make(map[string]bool, len(entity.Fields))
	for _, f := range entity.Fields {
		wire[f.Name] = true
		if f.ReadOnly {
			readOnly[f.Name] = true
		}
	}
	var out []string
	for _, col := range entity.Columns {
		if col.IsPK || col.Name == entity.PkField || readOnly[col.Name] {
			continue
		}
		if col.Immutable || !wire[col.Name] {
			out = append(out, col.Name)
		}
	}
	return out
}

// immutableColumn reports whether entity declares column `forge:immutable`.
func immutableColumn(entity EntityDef, column string) bool {
	for _, c := range entity.Columns {
		if c.Name == column {
			return c.Immutable
		}
	}
	return false
}

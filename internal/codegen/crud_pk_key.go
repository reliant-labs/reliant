package codegen

import "strings"

// pkg/crud carries an entity's primary key as text: GetOp.ID, DeleteOp.ID,
// ListOp.EntityID (the page cursor) and the crud.Scoped* helpers are all
// string-keyed, which is the key a TEXT or UUID column already has. An
// integer key (BIGSERIAL, an IDENTITY, INTEGER) crosses that seam as its
// decimal text: the ops file formats the wire and row keys with strconv, and
// parses the text back to the column's Go type before calling the ORM
// delegate (db.Get<E>ByID, db.Delete<E>), which takes the projected type.
// The text form is also what the scoped and cursor predicates bind: Postgres
// reads the quoted literal as the column's integer type, so `id = '7'` and
// `id > '10'` compare numbers, not strings.

// pkKeyParseBits is the bit size strconv.ParseInt parses a key of the
// column Go type goType at — 0 for a string key, which needs no parse — and
// false for a key type the generated ops cannot carry. "" is the legacy
// default, a string key.
func pkKeyParseBits(goType string) (bits int, ok bool) {
	switch goType {
	case "", "string":
		return 0, true
	case "int64":
		return 64, true
	case "int32":
		return 32, true
	case "int16":
		return 16, true
	}
	return 0, false
}

// pkKeyText renders expr, a key of Go type goType, as the text pkg/crud
// carries. False when goType is not a key type the ops can carry.
func pkKeyText(expr, goType string) (string, bool) {
	switch goType {
	case "", "string":
		return expr, true
	case "int64":
		return "strconv.FormatInt(" + expr + ", 10)", true
	case "int32", "int16":
		return "strconv.FormatInt(int64(" + expr + "), 10)", true
	case "uint64":
		return "strconv.FormatUint(" + expr + ", 10)", true
	case "uint32":
		return "strconv.FormatUint(uint64(" + expr + "), 10)", true
	}
	return "", false
}

// pkKeyArg is the argument the ops pass the ORM delegate: the text key
// itself for a string column, else the parsed `key` converted to the
// column's type.
func pkKeyArg(goType string) string {
	switch goType {
	case "", "string":
		return "id"
	case "int64":
		return "key"
	}
	return goType + "(key)"
}

// protoScalarKeyGoType is the Go type protoc-gen-go gives a proto scalar
// that can carry a primary key, or "" for any other type.
func protoScalarKeyGoType(protoType string) string {
	switch protoType {
	case "string":
		return "string"
	case "int64", "sint64", "sfixed64":
		return "int64"
	case "int32", "sint32", "sfixed32":
		return "int32"
	case "uint64", "fixed64":
		return "uint64"
	case "uint32", "fixed32":
		return "uint32"
	}
	return ""
}

// requestPkGoType is the Go type of the primary-key field on a get/delete
// request, read off the request message. When the message is not in the
// descriptor, the wire key is assumed to be the column's own type, widened
// to int32 for a smallint (proto has no 16-bit scalar).
func requestPkGoType(svc ServiceDef, cm CRUDMethod) string {
	for _, f := range svc.Messages[cm.Method.InputType] {
		if f.Name == cm.Entity.PkField {
			return protoScalarKeyGoType(f.ProtoType)
		}
	}
	if cm.Entity.PkGoType == "int16" {
		return "int32"
	}
	return cm.Entity.PkGoType
}

// crudPkKeyMismatch explains why the generated op cannot carry cm's key, or
// returns "" when it can: a key column of a type other than text or an
// integer, or a get/delete request whose key field is not a string or an
// integer scalar. Such an RPC gets the Unimplemented stub, which the author
// implements by hand, instead of an op that does not compile.
func crudPkKeyMismatch(svc ServiceDef, cm CRUDMethod) string {
	if cm.Operation == "create" {
		return ""
	}
	if _, ok := pkKeyParseBits(cm.Entity.PkGoType); !ok {
		return "primary key " + cm.Entity.TableName + "." + cm.Entity.PkField + " projects to Go type " +
			cm.Entity.PkGoType + "; the generated CRUD op keys on a text or integer column only"
	}
	if cm.Operation != "get" && cm.Operation != "delete" {
		return ""
	}
	for _, f := range svc.Messages[cm.Method.InputType] {
		if f.Name != cm.Entity.PkField {
			continue
		}
		if f.IsOptional || protoScalarKeyGoType(f.ProtoType) == "" {
			kind := f.ProtoType
			if f.IsOptional {
				kind = "optional " + kind
			}
			return "request " + cm.Method.InputType + " field " + f.Name + " is " + kind +
				"; the generated CRUD op reads a plain string or integer key"
		}
	}
	return ""
}

// usesStrconv reports whether any of the rendered key expressions calls strconv.
func usesStrconv(exprs ...string) bool {
	for _, e := range exprs {
		if strings.Contains(e, "strconv.") {
			return true
		}
	}
	return false
}

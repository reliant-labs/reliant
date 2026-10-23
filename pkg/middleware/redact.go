package middleware

import (
	"reflect"
)

const redactedValue = "[REDACTED]"

// Redact accepts a struct (or pointer to struct) and a set of field names
// to mask. It returns a map[string]any mirroring the struct's exported
// fields, with the named fields replaced by "[REDACTED]".
//
// Non-struct inputs return nil. Unexported fields are silently skipped.
//
// Example:
//
//	type User struct {
//	    ID    string
//	    Email string
//	    Name  string
//	}
//	out := middleware.Redact(User{ID: "1", Email: "a@b.c", Name: "Alice"}, "Email")
//	// out == map[string]any{"ID": "1", "Email": "[REDACTED]", "Name": "Alice"}
func Redact(v any, fields ...string) map[string]any {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return nil
	}

	redactSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		redactSet[f] = true
	}

	rt := rv.Type()
	out := make(map[string]any, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		sf := rt.Field(i)
		if !sf.IsExported() {
			continue
		}
		if redactSet[sf.Name] {
			out[sf.Name] = redactedValue
		} else {
			out[sf.Name] = rv.Field(i).Interface()
		}
	}
	return out
}

package orm

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSON wraps a Go value for storage as a JSON column value. The generated
// ORM code uses it for repeated scalar entity fields (e.g. `repeated
// string tags`): the value is marshalled to JSON text on write, which
// both postgres (a jsonb column accepts an untyped text parameter) and
// sqlite (a TEXT-affinity column) store natively — one generated code
// path, two dialects.
//
// A nil/empty slice round-trips as SQL NULL so the column stays cleanly
// nullable and scans back to the zero value.
//
// It is also how a `?` argument says "this is a JSON document". A slice
// passed through Arg binds as a postgres ARRAY, never as JSON, so a value a
// json/jsonb operand needs is wrapped explicitly — and bound, as a text
// parameter postgres parses with jsonb's input function:
//
//	q.Where("labels @> ?", orm.JSON([]string{"urgent"}))
func JSON(v any) driver.Valuer { return jsonValue{v: v} }

type jsonValue struct{ v any }

// Value implements driver.Valuer.
func (j jsonValue) Value() (driver.Value, error) {
	if j.v == nil {
		return nil, nil
	}
	b, err := json.Marshal(j.v)
	if err != nil {
		return nil, fmt.Errorf("orm: marshal JSON column value: %w", err)
	}
	// json.Marshal of a nil slice/map yields "null" — store SQL NULL
	// instead so the round-trip is nil → NULL → nil.
	if string(b) == "null" {
		return nil, nil
	}
	return string(b), nil
}

// ScanJSON returns a sql.Scanner that unmarshals a JSON column into dst
// (a pointer, e.g. *[]string). NULL / empty values leave dst at its zero
// value. It accepts []byte (postgres jsonb) and string (sqlite TEXT)
// sources.
func ScanJSON(dst any) sql.Scanner { return jsonScanner{dst: dst} }

type jsonScanner struct{ dst any }

// Scan implements sql.Scanner.
func (s jsonScanner) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("orm: cannot scan %T into JSON column destination %T", src, s.dst)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, s.dst); err != nil {
		return fmt.Errorf("orm: unmarshal JSON column into %T: %w", s.dst, err)
	}
	return nil
}

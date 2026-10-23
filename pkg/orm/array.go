package orm

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Array support for generated ORM code.
//
// Postgres stores slice columns as native arrays (TEXT[], BIGINT[]).
// Under Bun (Phase 2) the engine binds slice parameters as native arrays
// itself (the model field carries a `bun:"...,array"` tag), so the
// hand-rolled ArrayValue/Dialect encoder is gone — generated INSERT /
// UPDATE pass the slice straight to Bun.
//
// EmptyIfNil normalizes a nil slice to a non-nil empty slice so it binds
// as `{}` (the conventional NOT NULL DEFAULT '{}') rather than NULL.
// StringArray/Int64Array remain as Scanners that tolerate both the
// postgres text format `{a,b}` and legacy JSON `["a","b"]`.

// EmptyIfNil returns an empty (non-nil) slice when s is nil, else s. Used
// at generated write sites so a nil repeated field binds as an empty
// array literal, not NULL, against `NOT NULL DEFAULT '{}'` columns.
func EmptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// StringArray scans a string-array column from either encoding.
type StringArray []string

// Scan implements sql.Scanner.
func (a *StringArray) Scan(src any) error {
	s, ok := arrayText(src)
	if !ok {
		return fmt.Errorf("orm: cannot scan %T into StringArray", src)
	}
	if s == "" || s == "null" {
		*a = nil
		return nil
	}
	if strings.HasPrefix(s, "[") {
		return json.Unmarshal([]byte(s), a)
	}
	if strings.HasPrefix(s, "{") {
		parts, err := parsePGTextArray(s)
		if err != nil {
			return err
		}
		*a = parts
		return nil
	}
	return fmt.Errorf("orm: unrecognized array encoding %q", s)
}

// Int64Array scans an integer-array column from either encoding.
type Int64Array []int64

// Scan implements sql.Scanner.
func (a *Int64Array) Scan(src any) error {
	s, ok := arrayText(src)
	if !ok {
		return fmt.Errorf("orm: cannot scan %T into Int64Array", src)
	}
	if s == "" || s == "null" {
		*a = nil
		return nil
	}
	if strings.HasPrefix(s, "[") {
		return json.Unmarshal([]byte(s), a)
	}
	if strings.HasPrefix(s, "{") {
		parts, err := parsePGTextArray(s)
		if err != nil {
			return err
		}
		out := make([]int64, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.ParseInt(p, 10, 64)
			if err != nil {
				return fmt.Errorf("orm: array element %q is not an integer: %w", p, err)
			}
			out = append(out, n)
		}
		*a = out
		return nil
	}
	return fmt.Errorf("orm: unrecognized array encoding %q", s)
}

func arrayText(src any) (string, bool) {
	switch v := src.(type) {
	case nil:
		return "", true
	case string:
		return v, true
	case []byte:
		return string(v), true
	}
	return "", false
}

// parsePGTextArray parses the postgres text array format `{a,"b c",d}`.
// Multidimensional arrays are not supported (forge never emits them).
func parsePGTextArray(s string) ([]string, error) {
	if !strings.HasPrefix(s, "{") || !strings.HasSuffix(s, "}") {
		return nil, fmt.Errorf("orm: malformed postgres array %q", s)
	}
	body := s[1 : len(s)-1]
	if body == "" {
		return nil, nil
	}
	var (
		out []string
		cur strings.Builder
	)
	i := 0
	for i < len(body) {
		switch body[i] {
		case '"':
			i++
			for i < len(body) {
				if body[i] == '\\' && i+1 < len(body) {
					cur.WriteByte(body[i+1])
					i += 2
					continue
				}
				if body[i] == '"' {
					i++
					break
				}
				cur.WriteByte(body[i])
				i++
			}
		case ',':
			out = append(out, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(body[i])
			i++
		}
	}
	out = append(out, cur.String())
	for j, p := range out {
		if p == "NULL" {
			out[j] = ""
		}
	}
	return out, nil
}

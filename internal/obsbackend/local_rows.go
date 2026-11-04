package obsbackend

import (
	"fmt"
	"time"

	"github.com/reliant-labs/forge/pkg/obsquery"
)

// RowsOf adapts a decoded runner Result to the cursor pkg/obsquery reads, so the
// evaluator runs over the local runner exactly as it runs over clickhouse-go on
// the control plane.
func RowsOf(r Result) obsquery.Rows { return &resultRows{rows: r.Rows, at: -1} }

type resultRows struct {
	rows [][]any
	at   int
}

func (r *resultRows) Next() bool {
	r.at++
	return r.at < len(r.rows)
}

func (r *resultRows) Err() error   { return nil }
func (r *resultRows) Close() error { return nil }

// Scan reads the current row into dest by each destination's type, the types
// the obsquery Parse* functions and the evaluator scan into.
func (r *resultRows) Scan(dest ...any) error {
	if r.at < 0 || r.at >= len(r.rows) {
		return fmt.Errorf("scan: no current row")
	}
	row := r.rows[r.at]
	if len(dest) != len(row) {
		return Undetermined("the local ClickStack returned %d columns, the reader wants %d", len(row), len(dest))
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *string:
			*p = AsString(row[i])
		case *uint8:
			*p = uint8(AsUint(row[i]))
		case *uint16:
			*p = uint16(AsUint(row[i]))
		case *int32:
			*p = int32(AsInt(row[i]))
		case *int64:
			*p = AsInt(row[i])
		case *uint64:
			*p = AsUint(row[i])
		case *float64:
			*p = AsFloat(row[i])
		case *time.Time:
			*p = AsTime(row[i])
		case *[]string:
			*p = AsStrings(row[i])
		case *[]float64:
			*p = AsFloats(row[i])
		case *map[string]string:
			*p = AsStringMap(row[i])
		default:
			return fmt.Errorf("scan: unsupported destination %T", d)
		}
	}
	return nil
}

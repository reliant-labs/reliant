package orm

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// NullTime is a nullable timestamp scanner that tolerates every
// representation forge's supported engines hand back for a timestamp
// column:
//
//   - time.Time (Postgres via pgx/stdlib; SQLite when the declared type
//     is one the driver recognizes),
//   - string / []byte (SQLite for declared types like TIMESTAMPTZ, which
//     mattn/go-sqlite3 does NOT auto-convert — it only recognizes
//     "timestamp", "datetime" and "date" verbatim),
//   - nil (NULL column).
//
// database/sql's own sql.NullTime rejects the string forms, which made
// generated entity scans fail at runtime against the SQLite test
// harness. Generated *_orm.go scan code uses this type instead.
type NullTime struct {
	Time  time.Time
	Valid bool
}

// nullTimeFormats are tried in order when a timestamp arrives as text.
// The first three are SQLite's canonical storage formats (see
// mattn/go-sqlite3 SQLiteTimestampFormats); RFC 3339 covers values
// written by other tools.
var nullTimeFormats = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02",
}

// Scan implements sql.Scanner.
func (n *NullTime) Scan(value any) error {
	n.Time, n.Valid = time.Time{}, false
	switch v := value.(type) {
	case nil:
		return nil
	case time.Time:
		n.Time, n.Valid = v, true
		return nil
	case []byte:
		return n.parse(string(v))
	case string:
		return n.parse(v)
	default:
		return fmt.Errorf("orm: cannot scan %T into NullTime", value)
	}
}

func (n *NullTime) parse(s string) error {
	if s == "" {
		return nil
	}
	for _, layout := range nullTimeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			n.Time, n.Valid = t, true
			return nil
		}
	}
	return fmt.Errorf("orm: cannot parse %q as a timestamp", s)
}

// Value implements driver.Valuer so NullTime round-trips on writes too.
func (n NullTime) Value() (driver.Value, error) {
	if !n.Valid {
		return nil, nil
	}
	return n.Time, nil
}

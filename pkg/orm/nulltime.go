package orm

import (
	"database/sql/driver"
	"fmt"
	"time"
)

// NullTime is a nullable timestamp scanner that tolerates every
// representation the postgres driver hands back for a timestamp:
//
//   - time.Time (a timestamp/timestamptz/date column, decoded by pgx),
//   - string / []byte (the same value selected as text — a `::text` cast, a
//     `->>` JSON extraction, a text column holding timestamps — in postgres's
//     own output format or RFC 3339),
//   - nil (NULL column).
//
// database/sql's own sql.NullTime rejects the text forms. Generated
// *_orm.go scan code uses this type instead.
type NullTime struct {
	Time  time.Time
	Valid bool
}

// nullTimeFormats are tried in order when a timestamp arrives as text.
// The first two are postgres's ISO DateStyle output for timestamptz — an
// offset of whole hours prints as "+00", any other as "+05:30" — and the
// third its output for timestamp without time zone; RFC 3339 covers values
// written by other tools (JSON documents, mostly).
var nullTimeFormats = []string{
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
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

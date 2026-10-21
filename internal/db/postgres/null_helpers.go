package postgres

import "database/sql"

// ptrToNullString and nullStringToPtr convert between core's optional
// strings and sqlc's sql.NullString, for the stores still on sqlc.

func ptrToNullString(s *string) sql.NullString {
	if s != nil {
		return sql.NullString{String: *s, Valid: true}
	}
	return sql.NullString{Valid: false}
}

func nullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

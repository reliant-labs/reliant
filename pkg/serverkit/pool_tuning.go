package serverkit

import "database/sql"

// ApplyDBPoolTuning applies the four sql.DB pool knobs from t to db.
// A nil db or zero-value field is a no-op for the corresponding
// setting — Go's default is left in place.
//
// The function is exported so projects that dial their own connection
// outside the AutoMigrate hook (e.g. for an out-of-band health check)
// can apply the same tuning without re-implementing the four-line
// loop.
func ApplyDBPoolTuning(db *sql.DB, t DBPoolTuning) {
	if db == nil {
		return
	}
	if t.MaxOpenConns > 0 {
		db.SetMaxOpenConns(t.MaxOpenConns)
	}
	if t.MaxIdleConns > 0 {
		db.SetMaxIdleConns(t.MaxIdleConns)
	}
	if t.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(t.ConnMaxIdleTime)
	}
	if t.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(t.ConnMaxLifetime)
	}
}

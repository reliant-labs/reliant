package dbanalyze

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// WHAT A STATEMENT DID, OBSERVED RATHER THAN INFERRED.
//
// Each measured statement is bracketed by two snapshots taken in its file's
// transaction, and the difference is the measurement:
//
//   - locks: pg_locks rows of this backend (relation locks, granted). A
//     transaction keeps every lock until it ends, so a row that appears was
//     acquired by the statement. A mode already held does not appear twice:
//     the snapshot before the statement says which ones were. (A statement
//     that runs outside a transaction has released its locks by the second
//     snapshot; its lock is reported from its kind.)
//   - rewrites: pg_class.relfilenode of every user relation. A rewrite (and
//     a REINDEX) writes a new file and points the relation at it; nothing
//     else changes it.
//   - scans: pg_stat_xact_user_tables, the current transaction's own
//     counters. A validation scan, a rewrite and an index build all start a
//     sequential scan and count it there (pinned by the package's tests).
//
// Locks on relations that exist in neither snapshot — the transient heaps a
// rewrite builds and swaps in — are dropped, as are system catalogs and
// TOAST relations: what is reported is what a reader of the schema can name.

// relation is one user relation in a catalog snapshot.
type relation struct {
	oid         uint32
	name        string // "t", or "schema.t" outside public
	kind        string // pg_class.relkind
	relfilenode uint32
	reltuples   float64
	table       uint32 // an index's table; 0 for a table
}

func (r relation) isIndex() bool { return r.kind == "i" || r.kind == "I" }

// snapshot is the state one side of a statement is measured from.
type snapshot struct {
	rels  map[uint32]relation
	locks map[lockKey]bool
	scans map[uint32][2]int64 // seq_scan, seq_tup_read
}

type lockKey struct {
	oid  uint32
	mode string
}

const relationsQuery = `
SELECT c.oid, CASE WHEN n.nspname = 'public' THEN c.relname ELSE n.nspname || '.' || c.relname END,
       c.relkind::text, c.relfilenode, c.reltuples, COALESCE(i.indrelid, 0::oid)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_index i ON i.indexrelid = c.oid
WHERE c.relkind IN ('r', 'p', 'm', 'i', 'I')
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
  AND n.nspname NOT LIKE 'pg_temp_%' AND n.nspname NOT LIKE 'pg_toast_temp_%'`

const locksQuery = `
SELECT relation, mode FROM pg_locks
WHERE pid = pg_backend_pid() AND locktype = 'relation' AND granted AND relation IS NOT NULL`

const scansQuery = `SELECT relid, COALESCE(seq_scan, 0), COALESCE(seq_tup_read, 0) FROM pg_stat_xact_user_tables`

// takeSnapshot reads the three catalogs a measurement diffs.
func (s *Session) takeSnapshot(ctx context.Context) (snapshot, error) {
	snap := snapshot{rels: map[uint32]relation{}, locks: map[lockKey]bool{}, scans: map[uint32][2]int64{}}
	rows, err := s.q().QueryContext(ctx, relationsQuery)
	if err != nil {
		return snap, fmt.Errorf("read pg_class: %w", err)
	}
	for rows.Next() {
		var r relation
		if err := rows.Scan(&r.oid, &r.name, &r.kind, &r.relfilenode, &r.reltuples, &r.table); err != nil {
			_ = rows.Close()
			return snap, fmt.Errorf("read pg_class: %w", err)
		}
		snap.rels[r.oid] = r
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("read pg_class: %w", err)
	}

	rows, err = s.q().QueryContext(ctx, locksQuery)
	if err != nil {
		return snap, fmt.Errorf("read pg_locks: %w", err)
	}
	for rows.Next() {
		var k lockKey
		if err := rows.Scan(&k.oid, &k.mode); err != nil {
			_ = rows.Close()
			return snap, fmt.Errorf("read pg_locks: %w", err)
		}
		snap.locks[k] = true
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("read pg_locks: %w", err)
	}

	rows, err = s.q().QueryContext(ctx, scansQuery)
	if err != nil {
		return snap, fmt.Errorf("read pg_stat_xact_user_tables: %w", err)
	}
	for rows.Next() {
		var oid uint32
		var v [2]int64
		if err := rows.Scan(&oid, &v[0], &v[1]); err != nil {
			_ = rows.Close()
			return snap, fmt.Errorf("read pg_stat_xact_user_tables: %w", err)
		}
		snap.scans[oid] = v
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("read pg_stat_xact_user_tables: %w", err)
	}
	return snap, nil
}

type rowsCloser interface {
	Err() error
	Close() error
}

func closeRows(rows rowsCloser) error {
	err := rows.Err()
	if cerr := rows.Close(); err == nil {
		err = cerr
	}
	return err
}

// measurement is the raw difference across one statement, by OID.
type measurement struct {
	duration time.Duration
	// acquired are the lock modes that appeared; held the modes on a
	// relation the statement touched that were already held before it.
	acquired []lockKey
	held     []lockKey
	// rewritten are relations whose relfilenode changed.
	rewritten []uint32
	// created are relations that exist after the statement and not before;
	// dropped the ones that existed before and not after.
	created []uint32
	dropped []uint32
	// scanned is the seq_scan / seq_tup_read delta per table.
	scanned map[uint32][2]int64
	// rels is every relation either snapshot knows, for naming.
	rels map[uint32]relation
	err  error
}

// measure runs query between two snapshots. The statement's own error is
// measurement.err; the returned error is a failure to measure.
func (s *Session) measure(ctx context.Context, query string) (measurement, error) {
	before, err := s.takeSnapshot(ctx)
	if err != nil {
		return measurement{}, err
	}
	start := time.Now()
	execErr := s.Exec(ctx, query)
	m := measurement{duration: time.Since(start), err: execErr, scanned: map[uint32][2]int64{}}
	after, err := s.takeSnapshot(ctx)
	if err != nil {
		return m, err
	}

	m.rels = make(map[uint32]relation, len(after.rels))
	for oid, r := range before.rels {
		m.rels[oid] = r
		if _, ok := after.rels[oid]; !ok {
			m.dropped = append(m.dropped, oid)
		}
	}
	for oid, r := range after.rels {
		// Names and sizes come from before the statement where both know
		// the relation: a rewrite may reset what pg_class says of it.
		b, ok := before.rels[oid]
		if !ok {
			m.rels[oid] = r
			m.created = append(m.created, oid)
		} else if b.relfilenode != r.relfilenode && r.relfilenode != 0 {
			m.rewritten = append(m.rewritten, oid)
		}
	}
	for oid, v := range after.scans {
		b := before.scans[oid]
		if d := [2]int64{v[0] - b[0], v[1] - b[1]}; d[0] > 0 || d[1] > 0 {
			m.scanned[oid] = d
		}
	}
	touched := map[uint32]bool{}
	for k := range after.locks {
		if _, named := m.rels[k.oid]; !named {
			continue
		}
		if !before.locks[k] {
			m.acquired = append(m.acquired, k)
			touched[k.oid] = true
		}
	}
	for _, oid := range m.rewritten {
		touched[oid] = true
	}
	for oid := range m.scanned {
		touched[oid] = true
	}
	for k := range before.locks {
		if _, named := m.rels[k.oid]; named && touched[k.oid] {
			m.held = append(m.held, k)
		}
	}
	sortKeys(m.acquired)
	sortKeys(m.held)
	sort.Slice(m.rewritten, func(i, j int) bool { return m.rewritten[i] < m.rewritten[j] })
	sort.Slice(m.created, func(i, j int) bool { return m.created[i] < m.created[j] })
	sort.Slice(m.dropped, func(i, j int) bool { return m.dropped[i] < m.dropped[j] })
	return m, nil
}

func sortKeys(ks []lockKey) {
	sort.Slice(ks, func(i, j int) bool {
		if ks[i].oid != ks[j].oid {
			return ks[i].oid < ks[j].oid
		}
		return lockStrength[ks[i].mode] < lockStrength[ks[j].mode]
	})
}

// lockStrength orders postgres's table lock modes, weakest first.
var lockStrength = map[string]int{
	"AccessShareLock":          1,
	"RowShareLock":             2,
	"RowExclusiveLock":         3,
	"ShareUpdateExclusiveLock": 4,
	"ShareLock":                5,
	"ShareRowExclusiveLock":    6,
	"ExclusiveLock":            7,
	"AccessExclusiveLock":      8,
}

// blocksWrites reports whether a lock mode conflicts with RowExclusiveLock,
// the lock every INSERT, UPDATE and DELETE takes.
func blocksWrites(mode string) bool { return lockStrength[mode] >= lockStrength["ShareLock"] }

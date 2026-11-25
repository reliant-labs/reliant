package pgtest_test

import (
	"database/sql"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// A fan-out clones one wide template from many goroutines at once, the way a
// project's parallel DB tests do: every clone completes, holds the whole
// schema, and is its own database. Each clone's time is logged, so a run
// shows what one clone costs under concurrency (see BenchmarkCloneTemplate
// for the strategy comparison behind cloneStrategy).
func TestNewFromTemplate_ConcurrentClonesOfAWideTemplate(t *testing.T) {
	testkit.Slow(t, "builds a 200-table template on real postgres and clones it 64 times, 16 at once")
	const (
		tables  = 200
		workers = 16
		perWork = 4
	)
	key := fmt.Sprintf("wide_concurrent_%d", time.Now().UnixNano())
	build := func(db *sql.DB, _ string) error {
		for i := 0; i < tables; i++ {
			for _, stmt := range []string{
				fmt.Sprintf(`CREATE TABLE t%d (id uuid PRIMARY KEY, name text NOT NULL, owner_id uuid,
					created_at timestamptz NOT NULL DEFAULT now(), deleted_at timestamptz)`, i),
				fmt.Sprintf(`CREATE INDEX t%d_owner ON t%d (owner_id)`, i, i),
				fmt.Sprintf(`CREATE INDEX t%d_live ON t%d (created_at) WHERE deleted_at IS NULL`, i, i),
			} {
				if _, err := db.Exec(stmt); err != nil {
					return err
				}
			}
		}
		return nil
	}

	var (
		mu    sync.Mutex
		took  []time.Duration
		names = map[string]bool{}
		wg    sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWork; i++ {
				start := time.Now()
				db, _, cleanup, err := pgtest.NewFromTemplate(key, build)
				d := time.Since(start)
				if err != nil {
					t.Errorf("NewFromTemplate: %v", err)
					return
				}
				var n int
				var name string
				err = db.QueryRow(`SELECT count(*), current_database() FROM pg_tables WHERE schemaname = 'public'`).Scan(&n, &name)
				if err == nil && n != tables {
					err = fmt.Errorf("clone %s has %d tables, want %d", name, n, tables)
				}
				if err == nil {
					_, err = db.Exec(`INSERT INTO t0 (id, name) VALUES (gen_random_uuid(), 'x')`)
				}
				cleanup()
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				took = append(took, d)
				if names[name] {
					t.Errorf("two clones share database %s", name)
				}
				names[name] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(took) == 0 {
		return
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	at := func(q float64) time.Duration { return took[int(q*float64(len(took)-1))].Round(time.Millisecond) }
	t.Logf("%d clones of a %d-table template, %d at once: p50 %s, p95 %s, max %s (the first includes the build)",
		len(took), tables, workers, at(0.5), at(0.95), at(1))
}

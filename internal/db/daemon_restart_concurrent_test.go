package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"
)

// Two writers and an observer: replacement B holds attachment then daemon
// locks. A starts its status statement with the old generation visible and
// waits. B advances the attachment and clears the intent, then A resumes.
// Before the fix A waits on the daemon tuple with an old EXISTS snapshot;
// after it, A waits on the attachment and revalidates its updated generation.
func TestDaemonRestartConcurrentGenerationFence(t *testing.T) {
	for _, replacement := range []string{"replace", "replace-with-new-intent", "release"} {
		t.Run(replacement, func(t *testing.T) {
			repo, raw, cleanup := SetupTestDBWithRawDB(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			require.NoError(t, repo.UpsertDaemon(ctx, &Daemon{ID: "concurrent-drain", UserID: "owner"}))
			at := time.Now().UTC().Truncate(time.Microsecond)
			require.NoError(t, repo.UpsertDaemonAttachment(ctx, &DaemonAttachment{DaemonID: "concurrent-drain", UserID: "owner", Source: DaemonAttachmentSourceInbound, AttachedAt: at, LastStreamActivity: at}))
			const old = `{"incarnation_id":"old","reason":"old intent"}`
			require.NoError(t, repo.SetDaemonRestartStatus(ctx, "owner", "concurrent-drain", at, old))
			blockerPID := make(chan int, 1)
			advance := make(chan struct{})
			replacementDone := make(chan error, 1)
			go func() {
				replacementDone <- repo.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
					var pid int
					if err := repo.ORM().QueryRow(tx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return err
					}
					var id string
					if err := repo.ORM().QueryRow(tx, `SELECT daemon_id FROM daemon_attachment WHERE daemon_id = $1 FOR UPDATE`, "concurrent-drain").Scan(&id); err != nil {
						return err
					}
					if err := repo.ORM().QueryRow(tx, `SELECT id FROM daemons WHERE id = $1 FOR UPDATE`, "concurrent-drain").Scan(&id); err != nil {
						return err
					}
					blockerPID <- pid
					select {
					case <-advance:
					case <-ctx.Done():
						return ctx.Err()
					}
					if replacement != "release" {
						next := at.Add(time.Second)
						if err := repo.UpsertDaemonAttachment(tx, &DaemonAttachment{DaemonID: "concurrent-drain", UserID: "owner", Source: DaemonAttachmentSourceInbound, AttachedAt: next, LastStreamActivity: next}); err != nil {
							return err
						}
						if err := repo.CompareDaemonRestartStatus(tx, "owner", "concurrent-drain", next, old, ""); err != nil {
							return err
						}
						if replacement == "replace-with-new-intent" {
							return repo.SetDaemonRestartStatus(tx, "owner", "concurrent-drain", next, `{"incarnation_id":"replacement","reason":"new intent"}`)
						}
						return nil
					}
					if err := repo.ReleaseDaemonAttachment(tx, "concurrent-drain", at); err != nil {
						return err
					}
					_, err := repo.ORM().Exec(tx, `UPDATE daemons SET restart_status = NULL WHERE id = $1`, "concurrent-drain")
					return err
				})
			}()
			var b int
			select {
			case b = <-blockerPID:
			case <-ctx.Done():
				t.Fatal("replacement did not acquire compatible lock order")
			}
			oldPID := make(chan int, 1)
			oldDone := make(chan error, 1)
			go func() {
				oldDone <- repo.RunTx(ctx, orm.ReadCommitted, func(tx context.Context) error {
					var pid int
					if err := repo.ORM().QueryRow(tx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
						return err
					}
					oldPID <- pid
					return repo.SetDaemonRestartStatus(tx, "owner", "concurrent-drain", at, old)
				})
			}()
			var a int
			select {
			case a = <-oldPID:
			case <-ctx.Done():
				t.Fatal("old writer did not start")
			}
			// Observe the database's lock dependency, never use timing as evidence
			// that A has entered its statement snapshot.
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for {
				var blocked bool
				require.NoError(t, raw.QueryRowContext(ctx, `SELECT $1 = ANY(pg_blocking_pids($2))`, b, a).Scan(&blocked))
				if blocked {
					break
				}
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("old status writer did not block behind replacement")
				}
			}
			close(advance)
			require.NoError(t, <-replacementDone, "attachment writers must not deadlock with status lock order")
			require.ErrorIs(t, <-oldDone, sql.ErrNoRows, "in-flight old generation must not reintroduce intent")
			row, err := repo.GetDaemon(ctx, "concurrent-drain")
			require.NoError(t, err)
			if replacement == "replace-with-new-intent" {
				require.JSONEq(t, `{"incarnation_id":"replacement","reason":"new intent"}`, row.RestartStatus)
			} else {
				require.Empty(t, row.RestartStatus)
			}
		})
	}
}

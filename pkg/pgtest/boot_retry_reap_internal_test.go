//go:build !windows

package pgtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Between StartEmbedded's success and the owner lock (or pool record) the
// server has no owner; the held boot claim is what keeps another process's
// reaper from removing its live data directory in that window.
func TestReapSparesServerBetweenStartAndOwnership(t *testing.T) {
	privatePool(t)
	// A port nothing answers on: an answering one would itself keep the dir.
	port, ln := occupiedBootPort(t)
	_ = ln.Close()
	dir := runtimeDir(port)
	_, _, claim, err := startEmbeddedOnFreshPort(embeddedpostgres.DefaultConfig(), func() (uint32, error) {
		return port, nil
	}, runtimeDir, func(embeddedpostgres.Config) (*Embedded, error) {
		if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
			t.Fatal(err)
		}
		return &Embedded{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Old enough to reap, no owner lock, no owner file, no pool record.
	old := time.Now().Add(-2 * staleInstanceAge)
	_ = os.Chtimes(dir, old, old)
	reapStaleInstances()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the reaper removed a server whose boot claim is still held: %v", err)
	}
	// Proves the claim, not something else, kept it: released, it is reaped.
	claim.release()
	reapStaleInstances()
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unowned, unclaimed stale dir was not reaped (the test would prove nothing): %v", err)
	}
}

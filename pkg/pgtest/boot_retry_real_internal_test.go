package pgtest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// Keep the first candidate occupied through the actual dependency precheck.
// The next attempt extracts/initializes real Postgres into our exclusive claim;
// this catches incompatibilities between that claim and upstream extraction.
func TestEmbeddedBootRealCollisionThenStart(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a real embedded postgres; runs in task test")
	}
	port, ln := occupiedBootPort(t)
	root, err := os.MkdirTemp("", "pgboot-")
	if err != nil {
		t.Fatal(err)
	}
	// A failed Start can be uncertain: don't automatically RemoveAll it.
	// Empty claims are removed by the helper; a successful one is stopped
	// below before its own directory is removed.
	defer func() { _ = os.Remove(root) }()
	dirFor := func(p uint32) string { return filepath.Join(root, fmt.Sprint(p)) }
	choice := serverVersion()
	if choice.err != nil {
		t.Fatal(choice.err)
	}
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.PostgresVersion(choice.version)).
		Username("forge").Password("forge").Database("postgres").
		Locale("C").Encoding("UTF8").
		StartParameters(startParameters(runtime.GOOS)).
		StartTimeout(90 * time.Second)
	if cache := cacheDir(); cache != "" {
		cfg = cfg.CachePath(cache)
	}
	reserves := 0
	ep, gotPort, claim, err := startEmbeddedOnFreshPort(cfg, func() (uint32, error) {
		reserves++
		if reserves == 1 {
			return port, nil
		}
		return freePort()
	}, dirFor, StartEmbedded)
	if err != nil {
		t.Fatal(err)
	}
	claim.release()
	defer func() {
		if err := ep.Stop(); err != nil {
			t.Errorf("stop our postgres: %v", err)
			return
		}
		if err := os.RemoveAll(dirFor(gotPort)); err != nil {
			t.Errorf("remove our stopped postgres directory: %v", err)
		}
	}()
	if reserves < 2 || gotPort == port {
		t.Fatalf("reserves=%d port=%d, want a fresh port after collision on %d", reserves, gotPort, port)
	}
	db, err := openBase(fmt.Sprintf("postgres://forge:forge@localhost:%d/postgres?sslmode=disable", gotPort))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var encoding string
	if err := db.QueryRow("SHOW server_encoding").Scan(&encoding); err != nil || encoding != "UTF8" {
		t.Fatalf("encoding=%q err=%v", encoding, err)
	}
	assertBootListenerAlive(t, ln)
}

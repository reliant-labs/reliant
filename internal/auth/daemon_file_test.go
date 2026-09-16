// Copyright (c) 2025 Reliant Labs
package auth

import (
	"encoding/json"
	"os"
	"testing"
)

// TestDaemonCredentials_OriginsStayIndependent pins the outer key: two origins
// hold two credentials, and writing one does not disturb the other. This is
// what lets one machine hold dev, staging and prod — and several worktrees on
// distinct dynamic localhost ports — at once.
func TestDaemonCredentials_OriginsStayIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const originA = "http://localhost:3123"
	const originB = "http://localhost:8123"

	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_a", ServerURL: originA})
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_b", ServerURL: originB})

	if got := mustRead(t, originA, ""); got.PAT != "rlnt_pat_a" {
		t.Fatalf("origin A pat: got %q, want %q", got.PAT, "rlnt_pat_a")
	}
	if got := mustRead(t, originB, ""); got.PAT != "rlnt_pat_b" {
		t.Fatalf("origin B pat: got %q, want %q", got.PAT, "rlnt_pat_b")
	}
}

// TestDaemonCredentials_TwoAccountsOnOneOriginCoexist is the regression guard
// for the defect this nesting fixes.
//
// Keyed by origin alone, the store held ONE credential per server: a second
// account signing in on the same origin overwrote the first's PAT, and the
// first account's daemon then authenticated as — or failed as — someone else.
// Under the old flat map this test cannot pass, because there is nowhere for
// the second entry to go.
func TestDaemonCredentials_TwoAccountsOnOneOriginCoexist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"

	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_alice", ServerURL: origin, Sub: "user-alice"})
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_bob", ServerURL: origin, Sub: "user-bob"})

	if got := mustRead(t, origin, "user-alice"); got.PAT != "rlnt_pat_alice" {
		t.Fatalf("alice's PAT was clobbered: got %q, want %q", got.PAT, "rlnt_pat_alice")
	}
	if got := mustRead(t, origin, "user-bob"); got.PAT != "rlnt_pat_bob" {
		t.Fatalf("bob's pat: got %q, want %q", got.PAT, "rlnt_pat_bob")
	}
}

// TestDaemonCredentials_DefaultAccountResolves covers the no-flag invocation.
// With several accounts on one origin the lookup must be deterministic, not a
// map-iteration coin flip: the most recent write is the default.
func TestDaemonCredentials_DefaultAccountResolves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"

	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_alice", ServerURL: origin, Sub: "user-alice"})
	if got := mustRead(t, origin, ""); got.PAT != "rlnt_pat_alice" {
		t.Fatalf("lone entry must resolve with no account named: got %q", got.PAT)
	}

	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_bob", ServerURL: origin, Sub: "user-bob"})
	if got := mustRead(t, origin, ""); got.PAT != "rlnt_pat_bob" {
		t.Fatalf("most recent write must become the default: got %q, want %q", got.PAT, "rlnt_pat_bob")
	}

	// Naming an account still wins over the default, in either direction.
	if got := mustRead(t, origin, "user-alice"); got.PAT != "rlnt_pat_alice" {
		t.Fatalf("explicit account must beat the default: got %q", got.PAT)
	}
}

// TestDaemonCredentials_UnknownAccountIsNotSomeoneElse pins the refusal: a
// caller that named an account and has no entry gets nothing, never another
// account's credential. Handing back a neighbouring PAT is precisely the
// split-brain the nesting exists to prevent.
func TestDaemonCredentials_UnknownAccountIsNotSomeoneElse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_alice", ServerURL: origin, Sub: "user-alice"})

	got, err := ReadDaemonCredentials(origin, "user-carol")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for an account with no entry, got %+v", got)
	}
}

// TestDaemonCredentials_EmptySubIsTheDefaultAccount pins that "" and the
// explicit default account name one entry rather than two.
func TestDaemonCredentials_EmptySubIsTheDefaultAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:3123"
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_x", ServerURL: origin})

	if got := mustRead(t, origin, DefaultAccount); got.PAT != "rlnt_pat_x" {
		t.Fatalf("empty sub must be reachable as %s: got %q", DefaultAccount, got.PAT)
	}
}

// TestDeleteDaemonCredentials_RemovesOnlyThatAccount covers logout on a shared
// machine: signing one account out must leave the other signed in.
func TestDeleteDaemonCredentials_RemovesOnlyThatAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_alice", ServerURL: origin, Sub: "user-alice"})
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_bob", ServerURL: origin, Sub: "user-bob"})

	if err := DeleteDaemonCredentials(origin, "user-bob"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	gone, err := ReadDaemonCredentials(origin, "user-bob")
	if err != nil {
		t.Fatalf("read after delete: %v", err)
	}
	if gone != nil {
		t.Fatalf("expected bob's entry to be gone, got %+v", gone)
	}

	// Alice must still be there AND still reachable with no account named —
	// deleting the default must hand the pointer to the survivor rather than
	// leaving it dangling.
	if got := mustRead(t, origin, "user-alice"); got.PAT != "rlnt_pat_alice" {
		t.Fatalf("alice must survive bob's logout: got %q", got.PAT)
	}
	if got := mustRead(t, origin, ""); got.PAT != "rlnt_pat_alice" {
		t.Fatalf("default must fall to the remaining account: got %q", got.PAT)
	}
}

// TestDeleteDaemonCredentials_LastAccountRemovesOrigin verifies the file
// shrinks back to nothing rather than accumulating empty origins.
func TestDeleteDaemonCredentials_LastAccountRemovesOrigin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:3123"
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_x", ServerURL: origin, Sub: "user-x"})

	if err := DeleteDaemonCredentials(origin, "user-x"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	store, err := readStore()
	if err != nil {
		t.Fatalf("readStore: %v", err)
	}
	if _, ok := store.Origins[origin]; ok {
		t.Fatalf("emptied origin must be removed, store = %+v", store.Origins)
	}
	if _, ok := store.DefaultAccounts[origin]; ok {
		t.Fatalf("emptied origin must drop its default pointer, store = %+v", store.DefaultAccounts)
	}
}

// TestReadStore_OldFormatIsDiscarded pins the free format bump. Pre-launch the
// file has been a bare credential object, then a hostname-keyed map, then an
// origin-keyed map; none of those carry forward. A stale entry read as if it
// were the new shape would be worse than none, so anything that is not this
// format reads as empty and the next register rewrites it.
func TestReadStore_OldFormatIsDiscarded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path, err := DaemonCredentialsFilePath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.MkdirAll(dirOf(path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// The previous format: a flat origin → credential map.
	old := `{"http://localhost:8090":{"pat":"rlnt_pat_old","server_url":"http://localhost:8090"}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := ReadDaemonCredentials("http://localhost:8090", "")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != nil {
		t.Fatalf("an old-format entry must not be surfaced as if it were current, got %+v", got)
	}

	// And the store is writable again straight afterwards, in the new shape.
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_new", ServerURL: "http://localhost:8090", Sub: "user-new"})
	if got := mustRead(t, "http://localhost:8090", "user-new"); got.PAT != "rlnt_pat_new" {
		t.Fatalf("post-bump write: got %q", got.PAT)
	}
}

// TestDaemonCredentials_NoDaemonIDField pins that the stable daemon id is NOT
// in this store any more.
//
// It used to live here keyed by origin, which meant every worktree on one
// machine read back the same id and re-asserted it in DaemonRegister — a field
// the gateway trusts verbatim — so the daemons evicted each other on every
// registration. It now lives in the instance's own data directory
// (internal/toolexec/bootstrap.ReadDaemonID). Writing one here would silently
// resurrect the shared-identity bug, so the key must not appear on disk at all.
func TestDaemonCredentials_NoDaemonIDField(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"
	mustWrite(t, &DaemonCredentials{PAT: "rlnt_pat_x", ServerURL: origin, Sub: "user-x"})

	path, err := DaemonCredentialsFilePath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	origins, _ := raw["origins"].(map[string]any)
	accounts, _ := origins[origin].(map[string]any)
	entry, _ := accounts["user-x"].(map[string]any)
	if entry == nil {
		t.Fatalf("expected an entry at origins[%s][user-x], got %s", origin, data)
	}
	if _, present := entry["daemon_id"]; present {
		t.Fatalf("daemon_id must no longer be stored per origin — it is per instance now: %s", data)
	}
}

// TestDaemonCredentials_SubSurvivesRewrite pins the Electron↔Go round-trip for
// `sub`. It is now the store key rather than a passenger field, so a rewrite
// that dropped it would not merely lose a label — it would relocate the entry
// to the default account, and the Electron preflight, unable to prove the
// cached PAT belongs to the signed-in user, would re-mint on every cold launch.
func TestDaemonCredentials_SubSurvivesRewrite(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	const origin = "http://localhost:8090"
	mustWrite(t, &DaemonCredentials{
		PAT:       "rlnt_pat_electron",
		ServerURL: origin,
		Sub:       "user-c2caf4af",
	})

	got := mustRead(t, origin, "user-c2caf4af")
	got.GatewayURL = "http://localhost:29190"
	mustWrite(t, got)

	after := mustRead(t, origin, "user-c2caf4af")
	if after.Sub != "user-c2caf4af" {
		t.Fatalf("sub must survive a rewrite, got %q", after.Sub)
	}
	if after.GatewayURL != "http://localhost:29190" {
		t.Fatalf("gateway_url: got %q", after.GatewayURL)
	}
}

func mustWrite(t *testing.T, creds *DaemonCredentials) {
	t.Helper()
	if err := WriteDaemonCredentials(creds); err != nil {
		t.Fatalf("write %s/%s: %v", creds.ServerURL, creds.Sub, err)
	}
}

func mustRead(t *testing.T, serverURL, sub string) *DaemonCredentials {
	t.Helper()
	creds, err := ReadDaemonCredentials(serverURL, sub)
	if err != nil {
		t.Fatalf("read %s/%s: %v", serverURL, sub, err)
	}
	if creds == nil {
		t.Fatalf("read %s/%s: got nil credentials", serverURL, sub)
	}
	return creds
}

// dirOf is filepath.Dir, named locally so the test's one filesystem concern
// does not pull a second import into a file that is otherwise about the store.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

// Copyright (c) 2025 Reliant Labs
package auth

import "testing"

// RecordDaemonAccount fills in who a stored sign-in acts as and touches
// nothing else: not the PAT, not another account's entry, and not which
// account the origin defaults to — `auth login` records accounts while
// deciding what to remove, and must not move the default as it goes.
func TestRecordDaemonAccount_RecordsWhoWithoutMovingTheDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const origin = "https://reliantapi.com"
	mustWrite(t, &DaemonCredentials{PAT: "rlat_paired", ServerURL: origin})
	mustWrite(t, &DaemonCredentials{PAT: "rlat_bob", ServerURL: origin, Sub: "bob"})

	if err := RecordDaemonAccount(origin, DefaultAccount, "ext-alice", "alice@example.test"); err != nil {
		t.Fatal(err)
	}
	paired := mustRead(t, origin, DefaultAccount)
	if paired.PAT != "rlat_paired" || paired.AccountID != "ext-alice" || paired.AccountEmail != "alice@example.test" {
		t.Fatalf("recorded entry: %+v", paired)
	}
	// An empty email keeps the one recorded.
	if err := RecordDaemonAccount(origin, DefaultAccount, "ext-alice", ""); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, origin, DefaultAccount); got.AccountEmail != "alice@example.test" {
		t.Fatalf("an unknown email erased the recorded one: %+v", got)
	}
	if got := mustRead(t, origin, ""); got.Sub != "bob" {
		t.Fatalf("recording an account moved the default to %q", got.Sub)
	}
	if got := mustRead(t, origin, "bob"); got.AccountID != "" {
		t.Fatalf("another account's entry was touched: %+v", got)
	}
	// No such entry: nothing is invented.
	if err := RecordDaemonAccount(origin, "carol", "ext-carol", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadDaemonCredentials(origin, "carol"); got != nil {
		t.Fatalf("recording an account created an entry: %+v", got)
	}
}

func TestMakeDefaultDaemonAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const origin = "https://reliantapi.com"
	mustWrite(t, &DaemonCredentials{PAT: "rlat_alice", ServerURL: origin, Sub: "alice"})
	mustWrite(t, &DaemonCredentials{PAT: "rlat_bob", ServerURL: origin, Sub: "bob"})

	if err := MakeDefaultDaemonAccount(origin, "alice"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, origin, ""); got.Sub != "alice" {
		t.Fatalf("default = %q, want alice", got.Sub)
	}
	if err := MakeDefaultDaemonAccount(origin, "nobody"); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, origin, ""); got.Sub != "alice" {
		t.Fatalf("a missing account became the default: %q", got.Sub)
	}
}

func TestListDaemonCredentialsForServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	mustWrite(t, &DaemonCredentials{PAT: "rlat_alice", ServerURL: "https://reliantapi.com", Sub: "alice"})
	mustWrite(t, &DaemonCredentials{PAT: "rlat_bob", ServerURL: "https://reliantapi.com/api", Sub: "bob"})
	mustWrite(t, &DaemonCredentials{PAT: "rlat_dev", ServerURL: "http://localhost:3123", Sub: "alice"})

	got, err := ListDaemonCredentialsForServer("https://reliantapi.com/grpc")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Sub != "bob" || got[1].Sub != "alice" {
		t.Fatalf("want bob (the default) then alice, got %+v", got)
	}
	if none, err := ListDaemonCredentialsForServer("https://elsewhere.example"); err != nil || none != nil {
		t.Fatalf("another server: %+v %v", none, err)
	}
}

package envutil

import "testing"

func valueOf(env []string, key string) string { return Lookup(env, key) }

// TestMergeDeclaredWins is the regression for a whole class of unexplainable
// dev-stack failures: a variable the project DECLARES silently losing to a
// same-named one that happened to be exported in the shell. The concrete case
// was a TLS-intercepting proxy exporting SSL_CERT_FILE, which in Go REPLACES
// the trust store — so every host child lost the roots its declared bundle
// supplied and outbound TLS failed for anything the proxy wasn't decrypting.
func TestMergeDeclaredWins(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"SSL_CERT_FILE=/ambient/proxy-ca.pem",
		"EDITOR=vim",
	}
	declared := map[string]string{
		"SSL_CERT_FILE": "/declared/bundle.pem",
		"NEW_VAR":       "added",
		"PATH":          "/declared/should/lose",
	}

	got, conflicts := MergeDeclaredWins(base, declared, nil)

	if v := valueOf(got, "SSL_CERT_FILE"); v != "/declared/bundle.pem" {
		t.Errorf("SSL_CERT_FILE = %q; want the DECLARED value", v)
	}
	if v := valueOf(got, "PATH"); v != "/usr/bin" {
		t.Errorf("PATH = %q; want the shell's — PATH describes the machine, not the app", v)
	}
	if v := valueOf(got, "NEW_VAR"); v != "added" {
		t.Errorf("NEW_VAR = %q; non-conflicting declarations must pass through", v)
	}
	if v := valueOf(got, "EDITOR"); v != "vim" {
		t.Errorf("EDITOR = %q; undeclared shell vars must survive", v)
	}

	// Both conflicts are reported; silence is what made this unexplainable.
	if len(conflicts) != 2 {
		t.Fatalf("conflicts = %+v; want 2 (SSL_CERT_FILE and PATH)", conflicts)
	}
	byKey := map[string]EnvConflict{}
	for _, c := range conflicts {
		byKey[c.Key] = c
	}
	if c := byKey["SSL_CERT_FILE"]; c.ShellWon {
		t.Errorf("SSL_CERT_FILE conflict reported ShellWon; want declared to win")
	}
	if c := byKey["PATH"]; !c.ShellWon {
		t.Errorf("PATH conflict reported ShellWon=false; the shell keeps PATH")
	}
}

// TestMergeDeclaredWinsHomeIsDeclarable pins that a declared HOME is honored.
// A workload declares HOME to sandbox what its tools write under ~ (the demo
// environment's daemon keeps its credentials and worktrees out of the
// developer's ~/.reliant that way). When the shell's HOME won, the process
// wrote into the real home directory instead — the declaration did the
// opposite of what it said. An undeclared HOME still comes from the shell.
func TestMergeDeclaredWinsHomeIsDeclarable(t *testing.T) {
	base := []string{"HOME=/Users/dev", "PATH=/usr/bin"}

	got, conflicts := MergeDeclaredWins(base, map[string]string{"HOME": "/srv/demo/home"}, nil)
	if v := valueOf(got, "HOME"); v != "/srv/demo/home" {
		t.Errorf("HOME = %q; want the DECLARED home", v)
	}
	if len(conflicts) != 1 || conflicts[0].Key != "HOME" || conflicts[0].ShellWon {
		t.Errorf("conflicts = %+v; want one HOME conflict that the declaration won", conflicts)
	}

	got, _ = MergeDeclaredWins(base, map[string]string{"OTHER": "x"}, nil)
	if v := valueOf(got, "HOME"); v != "/Users/dev" {
		t.Errorf("HOME = %q; an env that declares no HOME inherits the shell's", v)
	}

	got, _ = MergeDeclaredWins(base, map[string]string{"HOME": "/srv/demo/home"}, ParseShellWins("HOME"))
	if v := valueOf(got, "HOME"); v != "/Users/dev" {
		t.Errorf("HOME = %q; FORGE_ENV_OVERRIDE=HOME must still restore the shell's", v)
	}
}

// TestMergeDeclaredWinsExplicitOptOut keeps the ad-hoc override one command
// away — the affordance the old base-wins default provided by accident.
func TestMergeDeclaredWinsExplicitOptOut(t *testing.T) {
	base := []string{"DATABASE_URL=postgres://ad-hoc"}
	declared := map[string]string{"DATABASE_URL": "postgres://declared"}

	got, conflicts := MergeDeclaredWins(base, declared, ParseShellWins("DATABASE_URL, OTHER"))
	if v := valueOf(got, "DATABASE_URL"); v != "postgres://ad-hoc" {
		t.Errorf("DATABASE_URL = %q; an explicit opt-out must restore shell precedence", v)
	}
	if len(conflicts) != 1 || !conflicts[0].ShellWon {
		t.Fatalf("conflicts = %+v; want one reported with ShellWon", conflicts)
	}
}

// TestMergeDeclaredWinsIdenticalValueIsNotAConflict keeps the report signal-only.
func TestMergeDeclaredWinsIdenticalValueIsNotAConflict(t *testing.T) {
	base := []string{"LOG_LEVEL=debug"}
	got, conflicts := MergeDeclaredWins(base, map[string]string{"LOG_LEVEL": "debug"}, nil)
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %+v; an identical value is not a conflict", conflicts)
	}
	if v := valueOf(got, "LOG_LEVEL"); v != "debug" {
		t.Errorf("LOG_LEVEL = %q", v)
	}
}

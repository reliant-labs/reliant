package codegen

import (
	"strings"
	"testing"
)

// The scaffolded secrets/<env>.yaml header is READ, and acted on. A dogfood
// run hit `required config field database_url is not set` from the migrate
// job, opened this file, and found a header stating that KCL env vars
// override the store on host launch — so the empty DATABASE_URL slot could
// not be the cause. It was the cause: jobs took an unscoped path that
// injected the blank as a real env var, and the run lost time proving the
// documented rule false.
//
// Both halves of that are now true — empty slots are dropped before layering
// (internal/cli.scopeSecretsToEnvVars), and jobs scope like services — but a
// header is only load-bearing while it matches the code. These pin the claims
// the header makes so it cannot drift back into fiction.
//
// The behaviour itself is pinned next to the code that implements it, in
// internal/cli/secret_empty_slot_test.go. What is checked HERE is that the
// file we hand the developer describes that behaviour.
func TestDevSecretStoreHeader_ExplainsTheEmptySlot(t *testing.T) {
	fields := []ConfigField{{
		Name: "database_url", EnvVar: configDBURLEnvVar, Sensitive: true,
		Description: "Postgres connection string.",
	}}
	got := generateEnvSecretsBody(fields, "pt", configDevEnvName)

	// The header must say an empty slot is dropped rather than injected —
	// that is WHY a fresh clone boots with this file untouched, and without
	// it the blank looks like a value the developer forgot to set.
	if !strings.Contains(got, "EMPTY slot is not a value") {
		t.Errorf("header does not explain that an empty slot is dropped, so a reader\n"+
			"cannot tell a blank slot from an unset one:\n%s", got)
	}

	// It must state the real precedence. The previous wording claimed only
	// that "KCL env vars override this store", which is true but was read as
	// a guarantee that the blank could not matter.
	for _, want := range []string{"project config", "KCL env_vars", "your shell"} {
		if !strings.Contains(got, want) {
			t.Errorf("header omits %q from the precedence chain; the chain is what tells a\n"+
				"developer whether setting a slot will actually take effect:\n%s", want, got)
		}
	}

	// And it must NOT repeat the claim that misled the run: that setting a
	// value here is what the app receives for a KCL-declared name.
	if strings.Contains(got, "so the declaration is what your app receives") {
		t.Error("header still carries the wording that a KCL-declared name is what the app " +
			"receives, without saying a filled slot LOSES to that declaration")
	}
}

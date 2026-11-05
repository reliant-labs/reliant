package config

import (
	"os"
	"testing"
)

// These tests assert the config PRECEDENCE ladder (defaults < file < env <
// flag), so every layer they do not set must genuinely be absent. A developer
// who exports one of these in their own shell — LOG_LEVEL=debug is the common
// one — otherwise supplies the env layer for free, and the assertions about
// the layers BELOW it fail:
//
//	loader_test.go:156: log_level default = "debug", want info
//
// Seven tests failed that way, none of them related to whatever change the
// developer was making. CI never saw it (runners set none of these), which is
// the worst shape for a failure to have: it reproduces only on the machine of
// the person least likely to suspect the test rather than their own work.
//
// Clearing the whole set ONCE here, rather than per test, is deliberate. The
// alternative — a neutralizing line in each test — is a rule every future test
// has to remember, and the failure for forgetting it is this same
// action-at-a-distance bug. TestMain runs before any test in the package, so
// new tests inherit a clean environment without opting in.
//
// A real unset, not t.Setenv(key, ""). The loader reads an empty variable as
// unset (lookupEnv in loader.go, pinned by TestLoadInto_EmptyEnvIsUnset), so
// the two load the same today; but a test that asserts "set but empty" (the
// required-field error names that case) needs the clean state to be the
// absent one.
//
// Tests that WANT an env layer still call t.Setenv normally; it sets the var
// for the test and restores the (now unset) state afterwards.
func TestMain(m *testing.M) {
	// Every env var bound by a fixture in this package's tests
	// (loader_test, filelayer_test, block_validate_test, scoped_flags_test,
	// semantic_test), plus the config-file path variables filelayer_test reads.
	for _, key := range []string{
		"ADMIN_TOKEN",
		"ALLOWED_REGISTRIES",
		"API_KEY",
		"BACKOFFS",
		"CORS_CREDS",
		"CORS_ORIGINS",
		"DB_HOST",
		"DB_PASSWORD",
		"DB_PORT",
		"DISTRACTOR",
		"ENABLED",
		"ENDPOINTS",
		"EXTRA_PORTS",
		"LABELS",
		"LOG_FORMAT",
		"LOG_LEVEL",
		"MAX_BYTES",
		"PORT",
		"RATIO",
		"REQUIRED_HOSTS",
		"RUNTIME_ENV",
		"TIMEOUT",
		"TLS_CERT",
		"TLS_KEY",
		"TOKEN",
		"TRADER_MAX_PER_TICK",
		"FILELAYER_CONFIG",
		"PROJ_CONFIG",
	} {
		if err := os.Unsetenv(key); err != nil {
			panic("config test setup: unset " + key + ": " + err.Error())
		}
	}

	os.Exit(m.Run())
}

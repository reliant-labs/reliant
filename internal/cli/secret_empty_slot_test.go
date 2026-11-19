package cli

import (
	"testing"

	"github.com/reliant-labs/forge/internal/hostlaunch"
)

// A scaffolded secrets/<env>.yaml ships every DECLARED slot present and
// BLANK — that is what `forge secret ensure` writes, and what an untouched
// project has on disk. So `DATABASE_URL: ""` is the common state, not an
// unusual one.
//
// Injected verbatim that blank is not "no value": it is a real env var bound
// to the empty string, sitting in a layer that outranks project config. The
// measured failure: `forge env up` on a fresh project brought postgres up and
// then killed the migrate job with
//
//	Error: load config: required config field database_url is not set (env: DATABASE_URL)
//
// while deploy/kcl/dev/main.k composed the DSN from the port it had just
// allocated, and `forge env config dev` printed that DSN for the migrate job.
// Every readback said the value was set; the process received "".
//
// The rule these pin: a blank slot means "this machine holds no value for
// this name", so the declaration below it wins. An empty value that is
// genuinely meaningful belongs in KCL, where it is visible in review.
func TestScopeSecrets_EmptySlotDoesNotClobberTheDeclaration(t *testing.T) {
	store := map[string]string{
		"DATABASE_URL": "", // scaffolded, never set
		"JWT_SECRET":   "", // scaffolded, never set
	}
	vars := []KCLEnvVar{
		{Name: "DATABASE_URL", SecretRef: "app-secrets", SecretKey: "database_url"},
	}

	scoped := scopeSecretsToEnvVars(store, vars)
	if v, present := scoped["DATABASE_URL"]; present {
		t.Fatalf("empty slot was injected as %q; it must not reach the process at all", v)
	}

	// End to end through the real layering: the KCL declaration must survive.
	const declared = "postgres://postgres:postgres@localhost:5435/app?sslmode=disable"
	final := hostlaunch.LayerHostEnv(
		[]string{"PATH=/usr/bin"},
		map[string]string{"DATABASE_URL": declared},
		scoped,
		nil,
	)
	if got := lookupEnv(final, "DATABASE_URL"); got != declared {
		t.Errorf("DATABASE_URL = %q, want the declared DSN %q", got, declared)
	}
}

// The other direction, which must NOT change: a slot with a real value is
// exactly what the secret store is for, and it still wins over project config.
func TestScopeSecrets_RealValueStillReachesTheProcess(t *testing.T) {
	store := map[string]string{"DATABASE_URL": "postgres://real/db"}
	vars := []KCLEnvVar{{Name: "DATABASE_URL", SecretRef: "app-secrets"}}

	scoped := scopeSecretsToEnvVars(store, vars)
	if scoped["DATABASE_URL"] != "postgres://real/db" {
		t.Fatalf("scoped = %v, want the real value", scoped)
	}
	final := hostlaunch.LayerHostEnv(
		[]string{"PATH=/usr/bin"},
		map[string]string{"DATABASE_URL": "postgres://from-project-config"},
		scoped,
		nil,
	)
	if got := lookupEnv(final, "DATABASE_URL"); got != "postgres://real/db" {
		t.Errorf("DATABASE_URL = %q, want the secret to beat project config", got)
	}
}

// A job receives only the secrets it DECLARES. Handing it the whole store was
// the same wholesale injection the dotenv provider was removed for: a value
// went live without ever being declared in KCL.
func TestScopeSecrets_UndeclaredSecretNeverReachesAJob(t *testing.T) {
	store := map[string]string{
		"DATABASE_URL":  "postgres://real/db",
		"STRIPE_SECRET": "sk_live_do_not_leak",
	}
	vars := []KCLEnvVar{{Name: "DATABASE_URL", SecretRef: "app-secrets"}}

	scoped := scopeSecretsToEnvVars(store, vars)
	if _, leaked := scoped["STRIPE_SECRET"]; leaked {
		t.Error("a secret the job never declared was injected into it")
	}
	if scoped["DATABASE_URL"] == "" {
		t.Error("the declared secret did not reach the job")
	}
}

// lookupEnv reads one KEY=VALUE out of a composed environment.
func lookupEnv(env []string, key string) string {
	for _, kv := range env {
		if len(kv) > len(key) && kv[:len(key)] == key && kv[len(key)] == '=' {
			return kv[len(key)+1:]
		}
	}
	return ""
}

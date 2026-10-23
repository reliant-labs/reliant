package cli

import "testing"

// svcWithSecretRefs builds a host service declaring the given env vars as
// secret refs.
func svcWithSecretRefs(name string, envNames ...string) WorkloadEntity {
	w := hostWL(name)
	for _, n := range envNames {
		withSecretRef(n, "app-secrets", "")(&w)
	}
	return w
}

// TestScopeSecretsToService_OnlyDeclaredKeysReachAService is the
// regression test for the bypass this whole change exists to close.
//
// Injection used to hand every host service the ENTIRE secret store, so a
// value added to the store went live in every process without appearing
// in KCL. That made "add a line to the untracked file" cheaper than
// declaring it, which is how non-secret config drifted out of version
// control. A service must now receive only what it declares.
func TestScopeSecretsToService_OnlyDeclaredKeysReachAService(t *testing.T) {
	store := map[string]string{
		"STRIPE_SECRET_KEY": "sk_test",
		"GITHUB_SECRET":     "ghs",
		"UNDECLARED_EXTRA":  "should-not-leak",
	}
	svc := svcWithSecretRefs("admin-server", "STRIPE_SECRET_KEY")

	got := scopeSecretsToService(store, &svc)

	if len(got) != 1 {
		t.Fatalf("service received %d secrets, want exactly its 1 declared: %v", len(got), got)
	}
	if got["STRIPE_SECRET_KEY"] != "sk_test" {
		t.Fatalf("declared secret missing or wrong: %v", got)
	}
	if _, leaked := got["UNDECLARED_EXTRA"]; leaked {
		t.Fatal("a secret the service never declared was injected — the bypass is back")
	}
}

// Two services in one env must not see each other's credentials. This is
// the per-service scoping the flat store cannot express on its own.
func TestScopeSecretsToService_ServicesAreIsolated(t *testing.T) {
	store := map[string]string{
		"STRIPE_SECRET_KEY": "sk_test",
		"WORKER_ONLY_TOKEN": "wtok",
	}
	admin := svcWithSecretRefs("admin-server", "STRIPE_SECRET_KEY")
	worker := svcWithSecretRefs("worker", "WORKER_ONLY_TOKEN")

	adminEnv := scopeSecretsToService(store, &admin)
	workerEnv := scopeSecretsToService(store, &worker)

	if _, ok := adminEnv["WORKER_ONLY_TOKEN"]; ok {
		t.Fatal("admin-server can read the worker's secret")
	}
	if _, ok := workerEnv["STRIPE_SECRET_KEY"]; ok {
		t.Fatal("worker can read admin-server's secret")
	}
}

// A service that declares no secret_ref gets nothing, even when the store
// is full — declaring is the ONLY way a value becomes visible.
func TestScopeSecretsToService_NoDeclarationsGetsNothing(t *testing.T) {
	store := map[string]string{"STRIPE_SECRET_KEY": "sk_test"}
	svc := svcWithSecretRefs("frontend") // declares none

	if got := scopeSecretsToService(store, &svc); len(got) != 0 {
		t.Fatalf("service with no declarations received %v, want nothing", got)
	}
}

// A declared ref with no value in the store simply doesn't appear —
// `forge env up` fails earlier via ValidateDeclaredRefs, which reports
// every miss at once with the fix command.
func TestScopeSecretsToService_MissingValueIsOmittedNotFabricated(t *testing.T) {
	store := map[string]string{"PRESENT": "v"}
	svc := svcWithSecretRefs("api", "PRESENT", "ABSENT")

	got := scopeSecretsToService(store, &svc)
	if _, ok := got["ABSENT"]; ok {
		t.Fatal("a missing secret was fabricated into the env")
	}
	if got["PRESENT"] != "v" {
		t.Fatalf("present secret not injected: %v", got)
	}
}

// An empty store returns nil so the caller's legacy secrets_file fallback
// still triggers for projects that declare no provider at all.
func TestScopeSecretsToService_EmptyStoreReturnsNil(t *testing.T) {
	svc := svcWithSecretRefs("api", "ANY")
	if got := scopeSecretsToService(nil, &svc); got != nil {
		t.Fatalf("empty store should return nil (preserves secrets_file fallback), got %v", got)
	}
}

package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func verifyEnv(shadow bool) *KCLEntities {
	e := storeEnv()
	e.RequiredSecrets = []ExternalSecretEntity{{
		Name: "control-plane-secrets", Namespace: "control-plane-prod",
		Keys: []string{"github_app_private_key", "webhook", "dsn"},
		Source: &StoreSourceEntity{
			RenderedStoreSource: release.RenderedStoreSource{
				Store: release.RenderedStore{Type: release.StoreOpenBao},
				Data: map[string]string{
					"github_app_private_key": "GITHUB_APP_PRIVATE_KEY",
					"webhook":                "GITHUB_WEBHOOK_SECRET",
				},
			},
			Templates: map[string]string{"dsn": "postgres://{{ .webhook }}"},
			Shadow:    shadow,
		},
	}}
	return e
}

func secretWith(data map[string]string, ann map[string]string, owners int) kubeSecret {
	d := map[string][]byte{}
	for k, v := range data {
		d[k] = []byte(v)
	}
	return kubeSecret{Data: d, Annotations: ann, OwnerRefs: owners}
}

func verifyOut(t *testing.T, secret string, opts secretsVerifyOptions) (string, error) {
	t.Helper()
	return run(t, func(o *bytes.Buffer) error {
		return runSecretsVerify(context.Background(), "prod", secret, opts, o)
	})
}

func TestVerify_ShadowMatchesPrintsOnlyVerdicts(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(true))
	live := map[string]string{"github_app_private_key": canary, "webhook": "whsec_0123456789", "dsn": "postgres://x"}
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(live, nil, 0))
	f.kube.put("control-plane-prod", "control-plane-secrets-eso-shadow", secretWith(live, nil, 0))

	out, err := verifyOut(t, "", secretsVerifyOptions{})
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	for _, key := range []string{"github_app_private_key", "webhook", "dsn"} {
		if !regexpLine(out, key, "match") {
			t.Errorf("no match line for %s in:\n%s", key, out)
		}
	}
	if !strings.Contains(out, "all 3 key(s) match") {
		t.Errorf("output = %q", out)
	}
}

func regexpLine(out, key, verdict string) bool {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 && f[0] == key && f[1] == verdict {
			return true
		}
	}
	return false
}

func TestVerify_AnyDifferenceStopsTheMigration(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(true))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(
		map[string]string{"github_app_private_key": canary, "webhook": "same-value-1", "dsn": "d"}, nil, 0))
	f.kube.put("control-plane-prod", "control-plane-secrets-eso-shadow", secretWith(
		map[string]string{"github_app_private_key": canary + "X", "webhook": "same-value-1"}, nil, 0))

	out, err := verifyOut(t, "control-plane-secrets", secretsVerifyOptions{})
	if err == nil || !strings.Contains(err.Error(), "not a match") {
		t.Fatalf("err = %v, want a failure", err)
	}
	if !regexpLine(out, "github_app_private_key", "no-match") {
		t.Errorf("a changed value was not flagged:\n%s", out)
	}
	if !regexpLine(out, "webhook", "match") {
		t.Errorf("an equal value was not matched:\n%s", out)
	}
	// A key present on one side only says WHICH side lacks it.
	if !strings.Contains(out, "missing from the shadow Secret") {
		t.Errorf("missing side not named:\n%s", out)
	}
	if !strings.Contains(out, "stop the migration") {
		t.Errorf("output = %q", out)
	}
}

func TestVerify_NoShadowSaysHowToMakeOne(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(true))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"webhook": "w"}, nil, 0))
	_, err := verifyOut(t, "", secretsVerifyOptions{})
	if err == nil || !strings.Contains(err.Error(), "shadow = True") {
		t.Errorf("err = %v", err)
	}
}

func TestVerify_AMissingLiveSecretIsANoMatchNotACrash(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(true))
	f.kube.put("control-plane-prod", "control-plane-secrets-eso-shadow", secretWith(map[string]string{"webhook": "w", "github_app_private_key": "k", "dsn": "d"}, nil, 0))
	out, err := verifyOut(t, "", secretsVerifyOptions{})
	if err == nil || !strings.Contains(out, "missing from the live Secret") {
		t.Errorf("err = %v out = %q", err, out)
	}
}

func TestVerify_AgainstGSMComparesTheDataKeysAndSkipsTemplates(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(
		map[string]string{"github_app_private_key": canary, "webhook": "live-webhook-value", "dsn": "d"}, nil, 0))
	f.gsm.secrets["acme-prod/GITHUB_APP_PRIVATE_KEY"] = []string{"old", canary}
	f.gsm.secrets["acme-prod/gh-webhook-prod"] = []string{"a-different-webhook"}

	out, err := verifyOut(t, "", secretsVerifyOptions{Against: "gsm:acme-prod", Map: []string{"webhook=gh-webhook-prod"}})
	if err == nil {
		t.Fatalf("a differing GSM copy verified:\n%s", out)
	}
	if !regexpLine(out, "github_app_private_key", "match") {
		t.Errorf("the newest GSM version should match:\n%s", out)
	}
	if !regexpLine(out, "webhook", "no-match") {
		t.Errorf("webhook should differ:\n%s", out)
	}
	if !regexpLine(out, "dsn", "skipped") {
		t.Errorf("a templated key should be skipped, not compared:\n%s", out)
	}
}

func TestVerify_AgainstGSMMissingSecretIsNamed(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"github_app_private_key": "k", "webhook": "w"}, nil, 0))
	out, err := verifyOut(t, "", secretsVerifyOptions{Against: "gsm:acme-prod"})
	if err == nil || !strings.Contains(out, "missing from the GSM secret GITHUB_APP_PRIVATE_KEY") {
		t.Errorf("err = %v out = %q", err, out)
	}
}

// With an explicit --map the Secret needs no store-backed ExternalSecret: this
// is the escrow check made before the Secret is migrated onto a store.
func TestVerify_AgainstGSMWithAnExplicitMapNeedsNoDeclaredExternalSecret(t *testing.T) {
	e := storeEnv()
	e.RequiredSecrets = nil
	f := installStoreFixture(t, e)
	f.kube.put("control-plane-prod", "control-plane-openbao-unseal", secretWith(map[string]string{"static-key": canary, "pg": "live-pg"}, nil, 0))
	f.gsm.secrets["acme-prod/openbao-unseal-prod"] = []string{canary}
	f.gsm.secrets["acme-prod/openbao-pg-prod"] = []string{"a-different-pg"}

	out, err := verifyOut(t, "control-plane-prod/control-plane-openbao-unseal", secretsVerifyOptions{
		Against: "gsm:acme-prod", Map: []string{"static-key=openbao-unseal-prod", "pg=openbao-pg-prod"},
	})
	if err == nil {
		t.Fatalf("a differing GSM copy verified:\n%s", out)
	}
	if strings.Contains(err.Error(), "declares no store-backed") {
		t.Fatalf("an explicit --map must not need a declared ExternalSecret: %v", err)
	}
	if !regexpLine(out, "static-key", "match") || regexpLine(out, "static-key", "no-match") {
		t.Errorf("static-key should match:\n%s", out)
	}
	if !regexpLine(out, "pg", "no-match") {
		t.Errorf("pg should differ:\n%s", out)
	}
	assertNoLeak(t, "verify with an explicit map", out, errText(err))

	// All matching exits clean.
	f.gsm.secrets["acme-prod/openbao-pg-prod"] = []string{"live-pg"}
	out, err = verifyOut(t, "control-plane-prod/control-plane-openbao-unseal", secretsVerifyOptions{
		Against: "gsm:acme-prod", Map: []string{"static-key=openbao-unseal-prod", "pg=openbao-pg-prod"},
	})
	if err != nil || !strings.Contains(out, "all 2 key(s) match") {
		t.Errorf("err=%v out=%q", err, out)
	}
}

// The relaxation is only for an explicit --against gsm: --map naming a Secret:
// everything else still reports that nothing is declared.
func TestVerify_WithoutAnExplicitMapAnUndeclaredSecretStillErrors(t *testing.T) {
	e := storeEnv()
	e.RequiredSecrets = nil
	installStoreFixture(t, e)
	for name, c := range map[string]struct {
		secret string
		opts   secretsVerifyOptions
	}{
		"shadow mode":      {"ns/s", secretsVerifyOptions{Map: []string{"k=id"}}},
		"gsm without map":  {"ns/s", secretsVerifyOptions{Against: "gsm:acme-prod"}},
		"gsm without name": {"", secretsVerifyOptions{Against: "gsm:acme-prod", Map: []string{"k=id"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyOut(t, c.secret, c.opts); err == nil || !strings.Contains(err.Error(), "nothing to verify") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

func TestVerify_FlagAndSelectionErrors(t *testing.T) {
	installStoreFixture(t, verifyEnv(true))
	if _, err := verifyOut(t, "", secretsVerifyOptions{Against: "s3:bucket"}); err == nil || !strings.Contains(err.Error(), "gsm:<project>") {
		t.Errorf("--against s3: err = %v", err)
	}
	if _, err := verifyOut(t, "", secretsVerifyOptions{Against: "gsm:p", Map: []string{"nokey"}}); err == nil || !strings.Contains(err.Error(), "key=gsm-secret-id") {
		t.Errorf("bad --map: err = %v", err)
	}
	if _, err := verifyOut(t, "nope", secretsVerifyOptions{}); err == nil || !strings.Contains(err.Error(), "no store-backed ExternalSecret") {
		t.Errorf("unknown secret: err = %v", err)
	}
	e := storeEnv()
	e.RequiredSecrets = nil
	installStoreFixture(t, e)
	if _, err := verifyOut(t, "", secretsVerifyOptions{}); err == nil || !strings.Contains(err.Error(), "nothing to verify") {
		t.Errorf("no sources: err = %v", err)
	}
}

func TestSameValue(t *testing.T) {
	if !sameValue([]byte("a"), []byte("a")) || sameValue([]byte("a"), []byte("b")) || sameValue(nil, []byte("b")) {
		t.Error("sameValue disagrees with byte equality")
	}
	if !sameValue(nil, nil) {
		t.Error("two empty values should compare equal")
	}
}

// --- adopt -------------------------------------------------------------------

func TestAdopt_StripsTheStaleAnnotationAndLeavesTheData(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(
		map[string]string{"webhook": canary},
		map[string]string{lastAppliedAnnotation: `{"data":{"webhook":"` + canary + `"}}`, "keep": "me"}, 0))

	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-prod/control-plane-secrets", "", false, o)
	})
	if err != nil {
		t.Fatalf("adopt: %v\n%s", err, out)
	}
	got := f.kube.secrets["control-plane-prod/control-plane-secrets"]
	if _, stale := got.Annotations[lastAppliedAnnotation]; stale {
		t.Error("the last-applied annotation is still there")
	}
	if got.Annotations["keep"] != "me" {
		t.Errorf("another annotation was removed: %v", got.Annotations)
	}
	if string(got.Data["webhook"]) != canary {
		t.Error("adopt changed the Secret's data")
	}
	if len(f.kube.patched) != 1 {
		t.Errorf("patches = %v, want exactly one", f.kube.patched)
	}
	if !strings.Contains(out, "stripped") {
		t.Errorf("output = %q", out)
	}
}

func TestAdopt_IsIdempotent(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"webhook": "w"}, nil, 0))
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-secrets", "", false, o)
	})
	if err != nil || !strings.Contains(out, "nothing to strip") || len(f.kube.patched) != 0 {
		t.Errorf("err = %v out = %q patched = %v", err, out, f.kube.patched)
	}
}

func TestAdopt_RefusesASecretSomethingElseOwns(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"webhook": "w"},
		map[string]string{lastAppliedAnnotation: "{}"}, 1))
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-secrets", "", false, o)
	})
	if err == nil || !strings.Contains(err.Error(), "ownerReference") {
		t.Fatalf("err = %v", err)
	}
	if len(f.kube.patched) != 0 {
		t.Error("patched a Secret it refused")
	}
}

func TestAdopt_RefusesWhileShadowing(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(true))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"webhook": "w"},
		map[string]string{lastAppliedAnnotation: "{}"}, 0))
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-secrets", "", false, o)
	})
	if err == nil || !strings.Contains(err.Error(), "shadow = True") || len(f.kube.patched) != 0 {
		t.Errorf("err = %v patched = %v", err, f.kube.patched)
	}
}

func TestAdopt_DryRunPatchesNothing(t *testing.T) {
	f := installStoreFixture(t, verifyEnv(false))
	f.kube.put("control-plane-prod", "control-plane-secrets", secretWith(map[string]string{"webhook": "w"},
		map[string]string{lastAppliedAnnotation: "{}"}, 0))
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-secrets", "", true, o)
	})
	if err != nil || !strings.Contains(out, "would strip") || len(f.kube.patched) != 0 {
		t.Errorf("err = %v out = %q patched = %v", err, out, f.kube.patched)
	}
}

func TestAdopt_AmbiguousNameNeedsANamespace(t *testing.T) {
	e := verifyEnv(false)
	e.RequiredSecrets = append(e.RequiredSecrets, ExternalSecretEntity{
		Name: "control-plane-secrets", Namespace: "other", Keys: []string{"k"},
		Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{
			Store: release.RenderedStore{Type: release.StoreOpenBao}, Data: map[string]string{"k": "K"},
		}},
	})
	installStoreFixture(t, e)
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretsAdopt(context.Background(), "prod", "control-plane-secrets", "", false, o)
	})
	if err == nil || !strings.Contains(err.Error(), "<namespace>/<name>") {
		t.Errorf("err = %v", err)
	}
}

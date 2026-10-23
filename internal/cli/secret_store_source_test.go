package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

func run(t *testing.T, fn func(out *bytes.Buffer) error) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := fn(&out)
	return out.String(), err
}

// --- set ---------------------------------------------------------------------

func TestSecretSet_OpenBaoStore_SignsInAsTheWriterAndWrites(t *testing.T) {
	f := installStoreFixture(t, storeEnv())

	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader(canary+"\n"), o)
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !strings.Contains(out, "API_KEY: created v1") || !strings.Contains(out, "self-run OpenBao") {
		t.Errorf("output = %q", out)
	}
	if got := f.bao.kv["secret/data/tenants/acme/prod/API_KEY"]; len(got) != 1 || got[0] != canary {
		t.Errorf("stored = %v, want the value with the trailing newline trimmed", got)
	}
	// The token is for the writer's own account and audience, minted once.
	if len(f.kube.tokens) != 1 || f.kube.tokens[0] != "openbao/forge-secret-writer aud=https://openbao.openbao.svc:8200/c/hub" {
		t.Errorf("tokens minted = %v", f.kube.tokens)
	}
}

func TestSecretSet_OpenBaoStore_RotationIsANewVersion(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	for i := 0; i < 2; i++ {
		if _, err := run(t, func(o *bytes.Buffer) error {
			return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v3"), o)
	})
	if !strings.Contains(out, "rotated to v3") {
		t.Errorf("output = %q, want a rotation to v3", out)
	}
	if len(f.bao.kv["secret/data/tenants/acme/prod/API_KEY"]) != 3 {
		t.Errorf("versions = %v", f.bao.kv)
	}
}

func TestSecretSet_HostedStore_GoesThroughTheControlPlane(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	if _, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "STRIPE_SECRET_KEY", "", strings.NewReader(canary), o)
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.hosted.sets) != 1 || f.hosted.sets[0] != "STRIPE_SECRET_KEY cas=false" {
		t.Errorf("hosted writes = %v, want one unconditional write", f.hosted.sets)
	}
	if len(f.kube.tokens) != 0 {
		t.Error("a hosted write minted a kube token")
	}
}

func TestSecretSet_GSMStore_UsesADCAndCreatesTheContainer(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "tls-cert-prod", "", strings.NewReader(canary), o)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Google Secret Manager project acme-prod") {
		t.Errorf("output = %q", out)
	}
	if got := f.gsm.secrets["acme-prod/tls-cert-prod"]; len(got) != 1 {
		t.Errorf("gsm = %v", f.gsm.secrets)
	}
}

func TestSecretSet_AmbiguousNameNeedsAStoreFlag(t *testing.T) {
	e := storeEnv()
	e.RequiredSecrets = append(e.RequiredSecrets, ExternalSecretEntity{
		Name: "dup", Namespace: "app", Keys: []string{"k"},
		Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{
			Store: release.RenderedStore{Type: release.StoreHosted}, Data: map[string]string{"k": "API_KEY"},
		}},
	})
	f := installStoreFixture(t, e)

	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "more than one store") || !strings.Contains(err.Error(), "--store") {
		t.Fatalf("err = %v, want an ambiguity naming --store", err)
	}
	if len(f.bao.kv) != 0 || len(f.hosted.sets) != 0 {
		t.Error("an ambiguous set wrote somewhere")
	}
	if _, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSetWith(context.Background(), "prod", "API_KEY", storeOptions{Store: "hosted"}, "", strings.NewReader("v"), o)
	}); err != nil {
		t.Fatalf("--store hosted: %v", err)
	}
	if len(f.hosted.sets) != 1 || len(f.bao.kv) != 0 {
		t.Errorf("--store hosted wrote hosted=%v bao=%v", f.hosted.sets, f.bao.kv)
	}
}

func TestSecretSet_RejectsAnUnknownStoreFlag(t *testing.T) {
	installStoreFixture(t, storeEnv())
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSetWith(context.Background(), "prod", "API_KEY", storeOptions{Store: "vault"}, "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "not a store kind") {
		t.Errorf("err = %v", err)
	}
}

func TestSecretSet_NameNoStoreDeclaresFallsThroughToTheProvider(t *testing.T) {
	installStoreFixture(t, storeEnv())
	handled, err := runStoreSourceSet(context.Background(), "prod", "UNRELATED", storeEnv(), storeOptions{}, "", strings.NewReader("v"), &bytes.Buffer{})
	if handled || err != nil {
		t.Errorf("handled=%v err=%v, want the provider path", handled, err)
	}
}

func TestSecretSet_RefusesAnEmptyValueBeforeContactingAnyStore(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("\n"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "empty value") {
		t.Fatalf("err = %v", err)
	}
	if len(f.kube.tokens) != 0 {
		t.Error("an empty value minted a token")
	}
}

func TestSecretSet_ABadNameIsRefusedPerStore(t *testing.T) {
	installStoreFixture(t, storeEnv())
	// '-' is legal in a Secret Manager id and illegal in an OpenBao name.
	if _, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "tls-cert-prod", "", strings.NewReader("v"), o)
	}); err != nil {
		t.Errorf("a GSM id with '-' was refused: %v", err)
	}
	e := storeEnv()
	e.RequiredSecrets[0].Source.Data["api-key"] = "API-KEY"
	installStoreFixture(t, e)
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API-KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "not a valid secret name") {
		t.Errorf("err = %v, want the OpenBao name rule", err)
	}
}

func TestSecretSet_OpenBao_NeedsTheComponentAndAKubeContext(t *testing.T) {
	e := storeEnv()
	e.OpenBao = nil
	installStoreFixture(t, e)
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "declares no Bundle.openbao") {
		t.Errorf("err = %v", err)
	}
}

func TestSecretSet_OpenBao_WriterAccountMissingPointsAtDeploy(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.kube.tokenErr = errors.New(`serviceaccounts "forge-secret-writer" not found`)
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "forge env deploy") {
		t.Errorf("err = %v", err)
	}
}

// --- list --------------------------------------------------------------------

func TestSecretList_ShowsEachStoresNamesAndVersions(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.bao.kv["secret/data/tenants/acme/prod/API_KEY"] = []string{"a", "b"}
	f.hosted.stored["STRIPE_SECRET_KEY"] = 4
	f.gsm.secrets["acme-prod/tls-cert-prod"] = []string{"x"}

	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]secretListEntry{}
	for _, s := range report.Secrets {
		got[s.Name] = s
	}
	for name, want := range map[string]uint32{"API_KEY": 2, "STRIPE_SECRET_KEY": 4, "tls-cert-prod": 1} {
		if e := got[name]; !e.Present || e.Version != want {
			t.Errorf("%s = %+v, want present at v%d", name, e, want)
		}
	}
	if e := got["DB_PASSWORD"]; e.Present || e.Presence != secretPresenceMissing {
		t.Errorf("DB_PASSWORD = %+v, want missing", e)
	}
	if report.OK || report.MissingCount != 1 || report.Missing[0] != "DB_PASSWORD" {
		t.Errorf("missing = %v ok=%v", report.Missing, report.OK)
	}
	// The report is attributed to the ExternalSecret that reads each name.
	if d := got["API_KEY"].DeclaredBy; len(d) != 1 || d[0].SecretName != "app-secrets" || d[0].SecretKey != "api-key" || d[0].Kind != "store:openbao" {
		t.Errorf("API_KEY declared_by = %+v", d)
	}
}

func TestSecretList_ADeletedCurrentVersionIsNotPresent(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.gsm.secrets["acme-prod/tls-cert-prod"] = []string{"x"}
	f.gsm.disabled["acme-prod/tls-cert-prod"] = true
	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range report.Secrets {
		if s.Name == "tls-cert-prod" && s.Present {
			t.Error("a disabled GSM version counts as present")
		}
	}
}

func TestSecretList_AnEnvWithoutSourcesIsUnchanged(t *testing.T) {
	e := storeEnv()
	e.RequiredSecrets = nil
	installStoreFixture(t, e)
	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if report.Provider != "none" || len(report.Secrets) != 0 {
		t.Errorf("report = %+v, want the provider-only answer", report)
	}
}

// --- import ------------------------------------------------------------------

func seedAppSecrets(f *storeFixture) {
	f.kube.put("control-plane-prod", "control-plane-secrets", kubeSecret{
		Data: map[string][]byte{"github_app_private_key": []byte(canary), "other": []byte("not imported")},
	})
}

func importEnv() *KCLEntities {
	e := storeEnv()
	e.RequiredSecrets = append(e.RequiredSecrets, ExternalSecretEntity{
		Name: "control-plane-secrets", Namespace: "control-plane-prod", Keys: []string{"github_app_private_key"},
		Source: &StoreSourceEntity{RenderedStoreSource: release.RenderedStoreSource{
			Store: release.RenderedStore{Type: release.StoreOpenBao},
			Data:  map[string]string{"github_app_private_key": "GITHUB_APP_PRIVATE_KEY"},
		}},
	})
	return e
}

func TestSecretImport_CopiesFromTheClusterCreateOnly(t *testing.T) {
	f := installStoreFixture(t, importEnv())
	seedAppSecrets(f)

	opts := importOptions{From: "control-plane-prod/control-plane-secrets", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"}}
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
	})
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if !strings.Contains(out, "GITHUB_APP_PRIVATE_KEY: created v1") {
		t.Errorf("output = %q", out)
	}
	if got := f.bao.kv["secret/data/tenants/acme/prod/GITHUB_APP_PRIVATE_KEY"]; len(got) != 1 || got[0] != canary {
		t.Errorf("stored = %v, want the cluster's bytes exactly", got)
	}
	if _, ok := f.bao.kv["secret/data/tenants/acme/prod/other"]; ok {
		t.Error("a key outside --map was imported")
	}
}

// A re-run reports and leaves the value: it can never overwrite one that has
// been rotated since. This is the property the whole migration leans on.
func TestSecretImport_ReRunNeverOverwrites(t *testing.T) {
	f := installStoreFixture(t, importEnv())
	seedAppSecrets(f)
	opts := importOptions{From: "control-plane-prod/control-plane-secrets", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"}}
	imp := func() string {
		out, err := run(t, func(o *bytes.Buffer) error {
			return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
		})
		if err != nil {
			t.Fatalf("import: %v\n%s", err, out)
		}
		return out
	}
	imp()
	// A person rotates it, then the import runs again.
	if _, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "GITHUB_APP_PRIVATE_KEY", "", strings.NewReader("rotated"), o)
	}); err != nil {
		t.Fatal(err)
	}
	out := imp()
	if !strings.Contains(out, "exists in") || !strings.Contains(out, "left unchanged") {
		t.Errorf("output = %q, want a no-op report", out)
	}
	got := f.bao.kv["secret/data/tenants/acme/prod/GITHUB_APP_PRIVATE_KEY"]
	if len(got) != 2 || got[1] != "rotated" {
		t.Errorf("versions = %v, want the rotated value untouched", got)
	}
}

func TestSecretImport_HostedStoreSendsCASZero(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.kube.put("billing", "stripe", kubeSecret{Data: map[string][]byte{"key": []byte(canary)}})
	opts := importOptions{From: "billing/stripe", Map: []string{"key=STRIPE_SECRET_KEY"}}
	run1 := func() string {
		out, err := run(t, func(o *bytes.Buffer) error {
			return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
		})
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		return out
	}
	run1()
	out := run1()
	if len(f.hosted.sets) != 2 || f.hosted.sets[0] != "STRIPE_SECRET_KEY cas=true" {
		t.Errorf("writes = %v, want create-only writes", f.hosted.sets)
	}
	if !strings.Contains(out, "left unchanged") || f.hosted.stored["STRIPE_SECRET_KEY"] != 1 {
		t.Errorf("second import: %q stored=%v", out, f.hosted.stored)
	}
}

func TestSecretImport_ToBootstrapWritesTheGSMContainer(t *testing.T) {
	e := importEnv()
	e.OpenBao.BootstrapNames = []string{"openbao-unseal-prod", "openbao-pg-password-prod"}
	f := installStoreFixture(t, e)
	f.kube.put("control-plane-prod", "control-plane-openbao-unseal", kubeSecret{Data: map[string][]byte{"static-key": []byte(canary)}})

	opts := importOptions{
		ToBootstrap: true, From: "control-plane-prod/control-plane-openbao-unseal",
		Map: []string{"static-key=openbao-unseal-prod"},
	}
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
	})
	if err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	if got := f.gsm.secrets["acme-prod/openbao-unseal-prod"]; len(got) != 1 || got[0] != canary {
		t.Errorf("gsm = %v", f.gsm.secrets)
	}
	if len(f.gsm.created) != 1 {
		t.Errorf("the container was not created: %v", f.gsm.created)
	}
	// Create-only against GSM: a second run leaves version 1.
	out, _ = run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
	})
	if !strings.Contains(out, "left unchanged") || len(f.gsm.secrets["acme-prod/openbao-unseal-prod"]) != 1 {
		t.Errorf("re-run: %q gsm=%v", out, f.gsm.secrets)
	}
}

func TestSecretImport_ToBootstrapRefusesANameThatIsNotABootstrapSecret(t *testing.T) {
	e := importEnv()
	e.OpenBao.BootstrapNames = []string{"openbao-unseal-prod"}
	f := installStoreFixture(t, e)
	f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"k": []byte("v")}})
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{ToBootstrap: true, From: "ns/s", Map: []string{"k=random-thing"}}, strings.NewReader(""), o)
	})
	if err == nil || !strings.Contains(err.Error(), "not a BootstrapSecret") {
		t.Errorf("err = %v", err)
	}
	if len(f.gsm.secrets) != 0 {
		t.Error("wrote to GSM anyway")
	}
}

func TestSecretImport_ToBootstrapNeedsAProject(t *testing.T) {
	e := importEnv()
	e.BootstrapSource = &BootstrapSourceEntity{Type: "out_of_band"}
	f := installStoreFixture(t, e)
	f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"k": []byte("v")}})
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{ToBootstrap: true, From: "ns/s", Map: []string{"k=x"}}, strings.NewReader(""), o)
	})
	if err == nil || !strings.Contains(err.Error(), "--project") {
		t.Errorf("err = %v", err)
	}
	// --project names it without the render.
	f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"k": []byte("v")}})
	if _, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{ToBootstrap: true, Project: "other-proj", From: "ns/s", Map: []string{"k=x"}}, strings.NewReader(""), o)
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.gsm.secrets["other-proj/x"]) != 1 {
		t.Errorf("gsm = %v", f.gsm.secrets)
	}
}

// Resolution happens for every name before anything is written, so a typo in
// the second name does not leave the first half-imported.
func TestSecretImport_ResolvesEveryDestinationBeforeWriting(t *testing.T) {
	f := installStoreFixture(t, importEnv())
	f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"a": []byte("1"), "b": []byte("2")}})
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{From: "ns/s", Map: []string{"a=API_KEY", "b=NOT_DECLARED"}}, strings.NewReader(""), o)
	})
	if err == nil || !strings.Contains(err.Error(), "NOT_DECLARED") {
		t.Fatalf("err = %v", err)
	}
	if len(f.bao.kv) != 0 {
		t.Errorf("API_KEY was written before the typo was found: %v", f.bao.kv)
	}
}

func TestSecretImport_FromStdinTakesOneName(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "API_KEY", importOptions{}, strings.NewReader(canary+"\n"), o)
	})
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := f.bao.kv["secret/data/tenants/acme/prod/API_KEY"]; len(got) != 1 || got[0] != canary {
		t.Errorf("stored = %v", got)
	}
}

func TestSecretImport_SourceErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		opts importOptions
		name string
		want string
	}{
		"nothing given":      {importOptions{}, "", "nothing to import"},
		"map without from":   {importOptions{Map: []string{"a=B"}}, "API_KEY", "--map only applies with --from"},
		"from without map":   {importOptions{From: "ns/s"}, "", "needs --map"},
		"bad from":           {importOptions{From: "nope", Map: []string{"a=B"}}, "", "<namespace>/<secret>"},
		"bad map":            {importOptions{From: "ns/s", Map: []string{"a"}}, "", "key=NAME"},
		"name plus from":     {importOptions{From: "ns/s", Map: []string{"a=B"}}, "API_KEY", "stdin form"},
		"name filled twice":  {importOptions{From: "ns/s", Map: []string{"a=B", "c=B"}}, "", "twice"},
		"missing secret key": {importOptions{From: "ns/s", Map: []string{"zzz=API_KEY"}}, "", "no value for key"},
		"unknown store flag": {importOptions{From: "ns/s", Map: []string{"a=API_KEY"}, storeOptions: storeOptions{Store: "x"}}, "", "not a store kind"},
	} {
		t.Run(name, func(t *testing.T) {
			f := installStoreFixture(t, storeEnv())
			f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"a": []byte("1")}})
			_, err := run(t, func(o *bytes.Buffer) error {
				return runSecretImport(context.Background(), "prod", tc.name, tc.opts, strings.NewReader("v"), o)
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSecretImport_BinaryAndEmptyValuesAreRefused(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.kube.put("ns", "s", kubeSecret{Data: map[string][]byte{"bin": {0xff, 0xfe, 0x00}, "empty": {}}})
	for key, want := range map[string]string{"bin": "not text", "empty": "no value for key"} {
		_, err := run(t, func(o *bytes.Buffer) error {
			return runSecretImport(context.Background(), "prod", "", importOptions{From: "ns/s", Map: []string{key + "=API_KEY"}}, strings.NewReader(""), o)
		})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", key, err, want)
		}
	}
	if len(f.bao.kv) != 0 {
		t.Error("an unusable value was written")
	}
}

func TestSecretImport_DryRunWritesNothingAndMintsNoToken(t *testing.T) {
	f := installStoreFixture(t, importEnv())
	seedAppSecrets(f)
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{
			DryRun: true, From: "control-plane-prod/control-plane-secrets", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"},
		}, strings.NewReader(""), o)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would create") || len(f.bao.kv) != 0 || len(f.kube.tokens) != 0 {
		t.Errorf("dry run: out=%q kv=%v tokens=%v", out, f.bao.kv, f.kube.tokens)
	}
}

// A dry run makes the same existence check the real create-only write does,
// from metadata alone, so it never promises "would create" where the real run
// would refuse.
func TestSecretImport_DryRunToBootstrapReportsAContainerThatAlreadyHoldsAValue(t *testing.T) {
	e := importEnv()
	e.OpenBao.BootstrapNames = []string{"openbao-unseal-prod", "openbao-pg-password-prod"}
	f := installStoreFixture(t, e)
	f.kube.put("control-plane-prod", "control-plane-openbao", kubeSecret{Data: map[string][]byte{
		"unseal": []byte(canary), "pg": []byte(canary + "-pg"),
	}})
	f.gsm.secrets["acme-prod/openbao-unseal-prod"] = []string{"already-escrowed"}

	opts := importOptions{
		DryRun: true, ToBootstrap: true, From: "control-plane-prod/control-plane-openbao",
		Map: []string{"unseal=openbao-unseal-prod", "pg=openbao-pg-password-prod"},
	}
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
	})
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	if l := lineOf(out, "openbao-unseal-prod"); !strings.Contains(l, "exists") || strings.Contains(l, "would create") {
		t.Errorf("a container with a value must be reported as existing:\n%s", out)
	}
	if !strings.Contains(lineOf(out, "openbao-pg-password-prod"), "would create") {
		t.Errorf("an absent container would be created:\n%s", out)
	}
	if f.gsm.accessed != 0 {
		t.Errorf("a dry run read %d payload(s); it may read metadata only", f.gsm.accessed)
	}
	if len(f.gsm.created) != 0 || len(f.gsm.secrets["acme-prod/openbao-unseal-prod"]) != 1 {
		t.Errorf("a dry run wrote: created=%v gsm=%v", f.gsm.created, f.gsm.secrets)
	}
	assertNoLeak(t, "dry run", out)

	// And the real run then agrees with what the dry run reported.
	opts.DryRun = false
	real, _ := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", opts, strings.NewReader(""), o)
	})
	if !strings.Contains(lineOf(real, "openbao-unseal-prod"), "left unchanged") || !strings.Contains(lineOf(real, "openbao-pg-password-prod"), "created") {
		t.Errorf("the real run disagrees with the dry run:\n%s", real)
	}
}

// A container that exists with no usable version is writable, so the dry run
// reports it as a create.
func TestSecretImport_DryRunToBootstrapAContainerWithNoUsableVersionWouldBeCreated(t *testing.T) {
	e := importEnv()
	e.OpenBao.BootstrapNames = []string{"openbao-unseal-prod"}
	f := installStoreFixture(t, e)
	f.kube.put("control-plane-prod", "control-plane-openbao", kubeSecret{Data: map[string][]byte{"unseal": []byte(canary)}})
	f.gsm.secrets["acme-prod/openbao-unseal-prod"] = []string{"disabled-one"}
	f.gsm.disabled["acme-prod/openbao-unseal-prod"] = true

	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{
			DryRun: true, ToBootstrap: true, From: "control-plane-prod/control-plane-openbao", Map: []string{"unseal=openbao-unseal-prod"},
		}, strings.NewReader(""), o)
	})
	if err != nil || !strings.Contains(out, "would create") {
		t.Errorf("err=%v out=%q", err, out)
	}
}

func lineOf(out, needle string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	return ""
}

func TestSecretImport_AFailedWriteIsReportedAndNamed(t *testing.T) {
	f := installStoreFixture(t, importEnv())
	seedAppSecrets(f)
	f.bao.failKV = 500
	out, err := run(t, func(o *bytes.Buffer) error {
		return runSecretImport(context.Background(), "prod", "", importOptions{
			From: "control-plane-prod/control-plane-secrets", Map: []string{"github_app_private_key=GITHUB_APP_PRIVATE_KEY"},
		}, strings.NewReader(""), o)
	})
	if err == nil || !strings.Contains(err.Error(), "GITHUB_APP_PRIVATE_KEY") || !strings.Contains(out, "FAILED") {
		t.Errorf("err = %v out = %q", err, out)
	}
}

// --- the OpenBao transport ---------------------------------------------------

func TestOpenBaoSource_ReusesOneTokenAcrossWrites(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	for _, n := range []string{"API_KEY", "DB_PASSWORD"} {
		if _, err := run(t, func(o *bytes.Buffer) error {
			return runSecretSet(context.Background(), "prod", n, "", strings.NewReader("v"), o)
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Each command opens its own store; within one, a single sign-in serves
	// every call.
	src, err := newOpenBaoSource(storeEnv(), storeEnv().OpenBao, f.kube, f.bao, "ctx-hub")
	if err != nil {
		t.Fatal(err)
	}
	f.kube.tokens = nil
	for _, n := range []string{"A", "B", "C"} {
		if _, err := src.put(context.Background(), n, "v", false); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.kube.tokens) != 1 {
		t.Errorf("minted %d tokens for three writes, want 1", len(f.kube.tokens))
	}
}

func TestOpenBaoSource_AWriterPolicyViolationIsAClearError(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	// Tighten the fake's policy to nothing: a real deployment whose converge Job
	// has not yet applied the writer policy.
	f.bao.rules = nil
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v, want the 403 named", err)
	}
}

func TestOpenBaoSource_ALoginRefusalNamesTheRole(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	f.bao.saJWT = "something-else"
	_, err := run(t, func(o *bytes.Buffer) error {
		return runSecretSet(context.Background(), "prod", "API_KEY", "", strings.NewReader("v"), o)
	})
	if err == nil || !strings.Contains(err.Error(), writerRole) || !strings.Contains(err.Error(), "converge Job") {
		t.Errorf("err = %v", err)
	}
}

func TestSecretListStore_ANotYetCreatedHostedEnvHoldsNothing(t *testing.T) {
	f := installStoreFixture(t, storeEnv())
	newHostedSecretWriter = func(context.Context, string, *KCLEntities, bool) (hostedSecretWriter, cloud.Endpoint, error) {
		return nil, cloud.Endpoint{}, errHostedEnvNotFound
	}
	_ = f
	st, err := storeSourceListStore(context.Background(), "prod", storeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if st.present["STRIPE_SECRET_KEY"] {
		t.Error("an env the control plane has not seen reported a secret")
	}
}

package secrets

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/linter/finding"
)

const envFile = "deploy/kcl/prod/main.k"

func env(r Render, docs ...Doc) Env {
	return Env{Name: "prod", File: envFile, Render: r, Docs: docs}
}

// storeES is a store-backed ExternalSecret declaration.
func storeES(name, ns string, keys []string, data map[string]string, pin map[string]int, immutable ...string) ExternalSecret {
	return ExternalSecret{Name: name, Namespace: ns, Keys: keys, Source: &Source{
		Store: Store{Type: "openbao"}, Data: data, Pin: pin, Immutable: immutable,
	}}
}

func byRule(fs []finding.Finding, rule string) []finding.Finding {
	var out []finding.Finding
	for _, f := range fs {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

func wantOne(t *testing.T, fs []finding.Finding, rule string, sev finding.Severity, contains ...string) finding.Finding {
	t.Helper()
	got := byRule(fs, rule)
	if len(got) != 1 {
		t.Fatalf("%s: %d findings, want 1: %+v", rule, len(got), fs)
	}
	f := got[0]
	if f.Severity != sev {
		t.Errorf("%s: severity %s, want %s", rule, f.Severity, sev)
	}
	for _, s := range contains {
		if !strings.Contains(f.Message+" "+f.Remediation, s) {
			t.Errorf("%s: finding lacks %q:\n%s\n%s", rule, s, f.Message, f.Remediation)
		}
	}
	// Every finding names where it is and how to waive it.
	if f.File == "" || f.Line < 1 {
		t.Errorf("%s: no file:line (%q:%d)", rule, f.File, f.Line)
	}
	if !strings.Contains(f.Remediation, "forge:lint-disable-next-line "+rule) || !strings.Contains(f.Remediation, "lint.rules."+rule) {
		t.Errorf("%s: remediation names no escape:\n%s", rule, f.Remediation)
	}
	return f
}

func wantNone(t *testing.T, fs []finding.Finding, rule string) {
	t.Helper()
	if got := byRule(fs, rule); len(got) != 0 {
		t.Errorf("%s: unexpected findings %+v", rule, got)
	}
}

func es(name, ns string, spec map[string]any) Doc {
	return Doc{
		"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec":     spec,
	}
}

func goodSpec() map[string]any {
	return map[string]any{
		"refreshInterval": "1h",
		"secretStoreRef":  map[string]any{"name": "reliant-openbao", "kind": "SecretStore"},
		"target":          map[string]any{"name": "app", "creationPolicy": "Orphan", "deletionPolicy": "Retain"},
		"data":            []any{map[string]any{"secretKey": "password"}},
	}
}

var adopted = Render{RequiredSecrets: []ExternalSecret{storeES("app", "ns", []string{"password"}, map[string]string{"password": "APP_PW"}, nil)}}

func TestAnEnvOutsideThePlatformSecretsModelIsNotJudged(t *testing.T) {
	// Out-of-band only, with a mount nothing declares and a PushSecret: none
	// of it is this lane's business until the project adopts the model.
	r := Render{RequiredSecrets: []ExternalSecret{{Name: "a", Namespace: "ns", Keys: []string{"k"}}}}
	e := env(r, Doc{"apiVersion": "external-secrets.io/v1alpha1", "kind": "PushSecret", "metadata": map[string]any{"name": "p"}})
	e.Demands = []Demand{{Workload: "Deployment/x", Namespace: "ns", Secret: "missing", Whole: true}}
	if e.Adopted() {
		t.Fatal("an out-of-band-only env must not count as adopted")
	}
	if fs := Check(e, nil); len(fs) != 0 {
		t.Errorf("findings for an env that adopted nothing: %+v", fs)
	}
}

func TestClusterKinds(t *testing.T) {
	for kind, wantPhrase := range map[string]string{
		"ClusterSecretStore":    "every namespace",
		"ClusterExternalSecret": "namespace it selects",
		"PushSecret":            "out to a store",
		"ClusterPushSecret":     "out to a store",
	} {
		t.Run(kind, func(t *testing.T) {
			d := Doc{"apiVersion": "external-secrets.io/v1alpha1", "kind": kind, "metadata": map[string]any{"name": "x"}}
			fs := Check(env(adopted, d), nil)
			wantOne(t, fs, RuleClusterKind, finding.SeverityError, kind, wantPhrase, "SecretStore")
		})
	}
	t.Run("a generator, and the CRD of a cluster kind", func(t *testing.T) {
		gen := Doc{"apiVersion": "generators.external-secrets.io/v1alpha1", "kind": "Password", "metadata": map[string]any{"name": "g"}}
		crd := Doc{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
			"metadata": map[string]any{"name": "clustersecretstores.external-secrets.io"},
			"spec":     map[string]any{"group": "external-secrets.io", "names": map[string]any{"kind": "ClusterSecretStore"}}}
		fs := byRule(Check(env(adopted, gen, crd), nil), RuleClusterKind)
		if len(fs) != 2 {
			t.Fatalf("want 2 findings (generator + CRD), got %+v", fs)
		}
	})
	t.Run("a namespaced SecretStore and ExternalSecret are fine", func(t *testing.T) {
		ss := Doc{"apiVersion": "external-secrets.io/v1", "kind": "SecretStore", "metadata": map[string]any{"name": "s", "namespace": "ns"}}
		wantNone(t, Check(env(adopted, ss, es("app", "ns", goodSpec())), nil), RuleClusterKind)
	})
}

func TestESPolicy(t *testing.T) {
	cases := map[string]struct {
		mut  func(map[string]any)
		want string
	}{
		"creation policy unset": {func(s map[string]any) { delete(s["target"].(map[string]any), "creationPolicy") }, "default is Owner"},
		"creation policy Owner": {func(s map[string]any) { s["target"].(map[string]any)["creationPolicy"] = "Owner" }, `"Owner"`},
		"deletion policy":       {func(s map[string]any) { s["target"].(map[string]any)["deletionPolicy"] = "Delete" }, "must be Retain"},
		"cluster store":         {func(s map[string]any) { s["secretStoreRef"].(map[string]any)["kind"] = "ClusterSecretStore" }, "namespaced SecretStore"},
		"dataFrom":              {func(s map[string]any) { s["dataFrom"] = []any{map[string]any{"extract": map[string]any{"key": "x"}}} }, "dataFrom"},
		"foreign target":        {func(s map[string]any) { s["target"].(map[string]any)["name"] = "other" }, "another declaration owns"},
		"fast refresh":          {func(s map[string]any) { s["refreshInterval"] = "30s" }, "under 5m0s"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := goodSpec()
			tc.mut(spec)
			fs := Check(env(adopted, es("app", "ns", spec)), nil)
			wantOne(t, fs, RuleESPolicy, finding.SeverityError, tc.want)
		})
	}
	t.Run("Merge with Retain, explicit keys, a slow refresh: clean", func(t *testing.T) {
		spec := goodSpec()
		spec["target"].(map[string]any)["creationPolicy"] = "Merge"
		wantNone(t, Check(env(adopted, es("app", "ns", spec)), nil), RuleESPolicy)
	})
}

func TestKeyCoverage(t *testing.T) {
	declared := Render{RequiredSecrets: []ExternalSecret{
		storeES("app", "ns", []string{"password", "url"}, map[string]string{"password": "PW", "url": "URL"}, nil),
		{Name: "oob", Namespace: "ns", Keys: []string{"token"}, OutOfBandReason: "cert-manager reads it before the store is up"},
	}}
	run := func(r Render, ds ...Demand) []finding.Finding {
		e := env(r)
		e.Demands = ds
		return Check(e, nil)
	}
	t.Run("a Secret nothing produces", func(t *testing.T) {
		fs := run(declared, Demand{Workload: "Deployment/api", Namespace: "ns", Secret: "ghost", Keys: []string{"k"}})
		wantOne(t, fs, RuleKeyCoverage, finding.SeverityError, "ghost", "Deployment/api", "CreateContainerConfigError")
	})
	t.Run("a Secret declared in another namespace says so", func(t *testing.T) {
		fs := run(declared, Demand{Workload: "Deployment/api", Namespace: "other", Secret: "app", Keys: []string{"password"}})
		wantOne(t, fs, RuleKeyCoverage, finding.SeverityError, "exists in namespace ns, not other")
	})
	t.Run("a key the declaration drops", func(t *testing.T) {
		fs := run(declared, Demand{Workload: "Deployment/api", Namespace: "ns", Secret: "app", Keys: []string{"password", "extra"}})
		wantOne(t, fs, RuleKeyCoverage, finding.SeverityError, "extra", "rebuilds the Secret from the declared keys only")
	})
	t.Run("covered, in either kind of declaration", func(t *testing.T) {
		fs := run(declared,
			Demand{Workload: "Deployment/api", Namespace: "ns", Secret: "app", Keys: []string{"password", "url"}},
			Demand{Workload: "Deployment/api", Namespace: "ns", Secret: "oob", Whole: true})
		wantNone(t, fs, RuleKeyCoverage)
	})
	t.Run("a Secret a control plane, a rendered Secret and cert-manager supply", func(t *testing.T) {
		r := declared
		r.ControlPlaneSecrets = []ProvidedSecret{{Name: "cp", Namespace: "ns", Keys: []string{"token"}}}
		cert := Doc{"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": map[string]any{"name": "c", "namespace": "ns"}, "spec": map[string]any{"secretName": "tls"}}
		rendered := Doc{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "plain", "namespace": "ns"}, "data": map[string]any{"a": "b"}}
		e := env(r, cert, rendered)
		e.Demands = []Demand{
			{Workload: "Deployment/x", Namespace: "ns", Secret: "cp", Keys: []string{"token"}},
			{Workload: "Deployment/x", Namespace: "ns", Secret: "tls", Whole: true},
			{Workload: "Deployment/x", Namespace: "ns", Secret: "plain", Keys: []string{"a"}},
		}
		wantNone(t, Check(e, nil), RuleKeyCoverage)
	})
	t.Run("a key the rendered Secret does not carry", func(t *testing.T) {
		rendered := Doc{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "plain", "namespace": "ns"}, "data": map[string]any{"a": "b"}}
		e := env(declared, rendered)
		e.Demands = []Demand{{Workload: "Deployment/x", Namespace: "ns", Secret: "plain", Keys: []string{"nope"}}}
		wantOne(t, Check(e, nil), RuleKeyCoverage, finding.SeverityError, "nope")
	})
	t.Run("a CloudNativePG cluster supplies its credential Secrets", func(t *testing.T) {
		cnpg := Doc{"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "metadata": map[string]any{"name": "db", "namespace": "ns"}}
		e := env(declared, cnpg)
		e.Demands = []Demand{{Workload: "Deployment/x", Namespace: "ns", Secret: "db-app", Keys: []string{"password"}}}
		wantNone(t, Check(e, nil), RuleKeyCoverage)
	})
	t.Run("a Secret the default namespace resolves to", func(t *testing.T) {
		r := declared
		r.ClusterTarget = &struct {
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
		}{"ctx", "ns"}
		wantNone(t, run(r, Demand{Workload: "Deployment/x", Secret: "app", Keys: []string{"password"}}), RuleKeyCoverage)
	})
	t.Run("a forge component's own pods, under a GCP bootstrap source", func(t *testing.T) {
		r := declared
		r.BootstrapSource = &BootstrapSource{Type: "gcpsm", Project: "p"}
		d := Demand{Workload: "StatefulSet/openbao", Namespace: "openbao", Secret: "openbao-unseal", Keys: []string{"static-key"}, Component: true}
		wantNone(t, run(r, d), RuleKeyCoverage)
		r.BootstrapSource = nil
		wantOne(t, run(r, d), RuleKeyCoverage, finding.SeverityError, "openbao-unseal")
	})
	t.Run("a file provider mints its Secrets at deploy", func(t *testing.T) {
		r := declared
		r.SecretProvider = &SecretProvider{Type: "file"}
		wantNone(t, run(r, Demand{Workload: "Deployment/x", Namespace: "ns", Secret: "ghost", Keys: []string{"k"}}), RuleKeyCoverage)
	})
}

// P10: the same tool answers which declared keys and Secrets nothing reads.
func TestUnreferencedKeysAreReportedAsInfo(t *testing.T) {
	r := Render{RequiredSecrets: []ExternalSecret{
		storeES("used", "ns", []string{"a", "b"}, map[string]string{"a": "A", "b": "B"}, nil),
		storeES("orphan", "ns", []string{"c"}, map[string]string{"c": "C"}, nil),
	}}
	e := env(r)
	e.Demands = []Demand{{Workload: "Deployment/x", Namespace: "ns", Secret: "used", Keys: []string{"a"}}}
	got := byRule(Check(e, nil), RuleUnreferenced)
	if len(got) != 2 {
		t.Fatalf("want an unread key and an orphan Secret, got %+v", got)
	}
	for _, f := range got {
		if f.Severity != finding.SeverityInfo {
			t.Errorf("unreferenced is advisory, got %s", f.Severity)
		}
	}
	if !strings.Contains(got[0].Message+got[1].Message, "key(s) b") || !strings.Contains(got[0].Message+got[1].Message, "ns/orphan") {
		t.Errorf("messages name neither the key nor the orphan: %+v", got)
	}
}

func TestImmutablePins(t *testing.T) {
	mk := func(pin map[string]int) Render {
		return Render{RequiredSecrets: []ExternalSecret{storeES("vault", "ns", []string{"key"}, map[string]string{"key": "VAULT_KEY"}, pin, "VAULT_KEY")}}
	}
	t.Run("unpinned floats", func(t *testing.T) {
		wantOne(t, Check(env(mk(nil)), nil), RuleImmutablePin, finding.SeverityError, "VAULT_KEY", "without a pin", "pin = {")
	})
	t.Run("pinned above 1", func(t *testing.T) {
		wantOne(t, Check(env(mk(map[string]int{"VAULT_KEY": 2})), nil), RuleImmutablePin, finding.SeverityError, "pins version 2")
	})
	t.Run("pinned at 1", func(t *testing.T) {
		wantNone(t, Check(env(mk(map[string]int{"VAULT_KEY": 1})), nil), RuleImmutablePin)
	})
	t.Run("another source reading the same immutable name must pin it too", func(t *testing.T) {
		r := mk(map[string]int{"VAULT_KEY": 1})
		r.RequiredSecrets = append(r.RequiredSecrets, storeES("copy", "ns2", []string{"key"}, map[string]string{"key": "VAULT_KEY"}, nil))
		fs := Check(env(r), nil)
		f := wantOne(t, fs, RuleImmutablePin, finding.SeverityError, "ns2/copy")
		if strings.Contains(f.Message, "ns/vault") {
			t.Errorf("blamed the source that pins correctly: %s", f.Message)
		}
	})
	t.Run("a different store's same-named value is another value", func(t *testing.T) {
		r := mk(map[string]int{"VAULT_KEY": 1})
		other := storeES("copy", "ns2", []string{"key"}, map[string]string{"key": "VAULT_KEY"}, nil)
		other.Source.Store = Store{Type: "gcpsm", Project: "p"}
		r.RequiredSecrets = append(r.RequiredSecrets, other)
		wantNone(t, Check(env(r), nil), RuleImmutablePin)
	})
}

func TestOutOfBandReasonQuality(t *testing.T) {
	r := func(reason string) Render {
		rr := adopted
		rr.RequiredSecrets = append(append([]ExternalSecret(nil), adopted.RequiredSecrets...),
			ExternalSecret{Name: "cloudflare-api-token", Namespace: "cert-manager", Keys: []string{"api-token"}, OutOfBandReason: reason})
		return rr
	}
	weak := []string{"manual", "todo", "TBD", "provisioned out of band", "manually provisioned", "n/a", "created by hand with kubectl", "cloudflare api token", "temporary"}
	for _, reason := range weak {
		t.Run("weak: "+reason, func(t *testing.T) {
			wantOne(t, Check(env(r(reason)), nil), RuleOutOfBandReason, finding.SeverityError, "cloudflare-api-token", "Say why")
		})
	}
	good := []string{
		"cert-manager DNS-01 reads it before the store is up; moves to the store in wave W5",
		"the Zitadel masterkey is escrowed in GSM and never synced",
	}
	for _, reason := range good {
		t.Run("good: "+reason, func(t *testing.T) {
			wantNone(t, Check(env(r(reason)), nil), RuleOutOfBandReason)
		})
	}
	t.Run("a store-backed one needs none", func(t *testing.T) {
		wantNone(t, Check(env(adopted), nil), RuleOutOfBandReason)
	})
}

func accessCM(rules ...map[string]any) Doc {
	doc := map[string]any{
		"kv": map[string]any{"mount": "secret"},
		"policies": []any{map[string]any{"name": "consumer-deployer", "rules": func() []any {
			var out []any
			for _, r := range rules {
				out = append(out, r)
			}
			return out
		}()}},
	}
	raw, _ := json.Marshal(doc)
	return Doc{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "openbao-access-abc"}, "data": map[string]any{"access.json": string(raw)}}
}

func TestOpenBaoWildcardRead(t *testing.T) {
	glob := func(reason string) map[string]any {
		return map[string]any{"path": "secret/data/envs/*", "capabilities": []any{"read"}, "reason": reason}
	}
	t.Run("no reason", func(t *testing.T) {
		fs := Check(env(adopted, accessCM(glob(""))), nil)
		wantOne(t, fs, RuleWildcardRead, finding.SeverityError, "secret/data/envs/*", "states no reason", "wildcard_read_reason")
	})
	t.Run("a placeholder reason", func(t *testing.T) {
		wantOne(t, Check(env(adopted, accessCM(glob("todo"))), nil), RuleWildcardRead, finding.SeverityError, "which is too short")
	})
	t.Run("a stated reason", func(t *testing.T) {
		wantNone(t, Check(env(adopted, accessCM(glob("deployer reads every hosted env until per-env grants land in SC1"))), nil), RuleWildcardRead)
	})
	t.Run("a wildcard that cannot read a value", func(t *testing.T) {
		wantNone(t, Check(env(adopted, accessCM(
			map[string]any{"path": "secret/metadata/envs/*", "capabilities": []any{"read", "list"}},
			map[string]any{"path": "secret/data/envs/*", "capabilities": []any{"create", "update"}},
			map[string]any{"path": "sys/policies/acl/env-*", "capabilities": []any{"read"}},
			map[string]any{"path": "secret/data/envs/acme/prod/NAME", "capabilities": []any{"read"}},
		)), nil), RuleWildcardRead)
	})
	t.Run("a glob in place of the data segment", func(t *testing.T) {
		wantOne(t, Check(env(adopted, accessCM(map[string]any{"path": "secret/+/envs/x", "capabilities": []any{"read"}})), nil), RuleWildcardRead, finding.SeverityError)
	})
}

func TestEscrowInCluster(t *testing.T) {
	withBao := func(r Render) Render {
		r.OpenBao = json.RawMessage(`{"name":"openbao"}`)
		return r
	}
	t.Run("out of band by default warns", func(t *testing.T) {
		wantOne(t, Check(env(withBao(Render{})), nil), RuleEscrow, finding.SeverityWarning, "bootstrap_source", "GCPSecretManager")
	})
	t.Run("out of band stated warns", func(t *testing.T) {
		wantOne(t, Check(env(withBao(Render{BootstrapSource: &BootstrapSource{Type: "out_of_band"}})), nil), RuleEscrow, finding.SeverityWarning)
	})
	t.Run("escrowed in GSM", func(t *testing.T) {
		wantNone(t, Check(env(withBao(Render{BootstrapSource: &BootstrapSource{Type: "gcpsm", Project: "p"}})), nil), RuleEscrow)
	})
	t.Run("a local env holds nothing worth escrowing", func(t *testing.T) {
		wantNone(t, Check(env(withBao(Render{Lifecycle: "local"})), nil), RuleEscrow)
	})
	t.Run("no OpenBao here", func(t *testing.T) {
		hosted := Render{RequiredSecrets: []ExternalSecret{storeES("app", "ns", []string{"k"}, map[string]string{"k": "N"}, nil)}}
		hosted.RequiredSecrets[0].Source.Store = Store{Type: "hosted"}
		wantNone(t, Check(env(hosted), nil), RuleEscrow)
	})
	t.Run("a reader of an OpenBaoStore implies the component", func(t *testing.T) {
		wantOne(t, Check(env(adopted), nil), RuleEscrow, finding.SeverityWarning)
	})
}

func TestSingleOwner(t *testing.T) {
	t.Run("a declaration and a rendered Secret", func(t *testing.T) {
		sec := Doc{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "app", "namespace": "ns"}}
		fs := Check(env(adopted, sec), nil)
		wantOne(t, fs, RuleSingleOwner, finding.SeverityError, "ns/app", "2 owners", "forge.ExternalSecret (openbao store)", "a Secret object in the render")
	})
	t.Run("a declaration and a hand-written ExternalSecret object", func(t *testing.T) {
		fs := Check(env(adopted, es("hand", "ns", func() map[string]any {
			s := goodSpec()
			s["target"].(map[string]any)["name"] = "app"
			return s
		}())), nil)
		wantOne(t, fs, RuleSingleOwner, finding.SeverityError, "an ExternalSecret object hand")
	})
	t.Run("the ESO object forge rendered for a declaration is the same owner", func(t *testing.T) {
		wantNone(t, Check(env(adopted, es("app", "ns", goodSpec())), nil), RuleSingleOwner)
	})
	t.Run("a shadow beside its Secret is one declaration", func(t *testing.T) {
		spec := goodSpec()
		spec["target"].(map[string]any)["name"] = "app-eso-shadow"
		wantNone(t, Check(env(adopted, es("app-eso-shadow", "ns", spec)), nil), RuleSingleOwner)
	})
	t.Run("a declaration and a ControlPlaneSecret", func(t *testing.T) {
		r := adopted
		r.ControlPlaneSecrets = []ProvidedSecret{{Name: "app", Namespace: "ns"}}
		wantOne(t, Check(env(r), nil), RuleSingleOwner, finding.SeverityError, "forge.ControlPlaneSecret")
	})
	t.Run("two namespaces, one name, are two Secrets", func(t *testing.T) {
		r := adopted
		r.RequiredSecrets = append(append([]ExternalSecret(nil), adopted.RequiredSecrets...),
			ExternalSecret{Name: "app", Namespace: "other", Keys: []string{"k"}, OutOfBandReason: "provisioned by the cert-manager bootstrap"})
		wantNone(t, Check(env(r), nil), RuleSingleOwner)
	})
}

func TestEverythingIsAnchoredAtItsDeclaration(t *testing.T) {
	loc := NewLocator([]SourceFile{
		{Path: "deploy/kcl/prod/main.k", Text: "import forge\n\n# name = \"ghost\" in a comment\nrequired_secrets = [\n    forge.ExternalSecret {\n        name = \"app\"\n        namespace = \"ns\"\n    }\n]\n"},
		{Path: "deploy/kcl/lib/secrets.k", Text: "x = 1\nforge.ExternalSecret {\n    name = \"cloud\"\n    out_of_band_reason = \"manual\"\n}\n"},
	})
	r := adopted
	r.RequiredSecrets = append(append([]ExternalSecret(nil), adopted.RequiredSecrets...),
		ExternalSecret{Name: "cloud", Namespace: "cert-manager", Keys: []string{"k"}, OutOfBandReason: "manual"})
	f := wantOne(t, Check(env(r), loc), RuleOutOfBandReason, finding.SeverityError)
	if f.File != "deploy/kcl/lib/secrets.k" || f.Line != 3 {
		t.Errorf("anchored at %s:%d, want deploy/kcl/lib/secrets.k:3 (the declaration, not the comment)", f.File, f.Line)
	}
	// What it cannot find anchors at the env's main.k.
	e := env(adopted)
	e.Demands = []Demand{{Workload: "Deployment/x", Namespace: "ns", Secret: "nowhere-declared", Whole: true}}
	g := wantOne(t, Check(e, loc), RuleKeyCoverage, finding.SeverityError)
	if g.File != envFile || g.Line != 1 {
		t.Errorf("an undeclared Secret anchored at %s:%d, want %s:1", g.File, g.Line, envFile)
	}
}

func TestMentionsPlatformSecrets(t *testing.T) {
	yes := []SourceFile{{Path: "a.k", Text: "source = forge.StoreSource {}"}}
	no := []SourceFile{{Path: "a.k", Text: "# forge.StoreSource would go here\nrequired_secrets = [forge.ExternalSecret {name = \"x\"}]"}}
	if !MentionsPlatformSecrets(yes) || MentionsPlatformSecrets(no) {
		t.Errorf("yes=%v no=%v", MentionsPlatformSecrets(yes), MentionsPlatformSecrets(no))
	}
}

func TestParseRenderAcceptsTheWholeDocumentOrTheOutput(t *testing.T) {
	const body = `{"required_secrets":[{"name":"a","namespace":"n","keys":["k"],"out_of_band_reason":"why","source":{"store":{"type":"hosted"},"data":{"k":"NAME"},"pin":{"NAME":1}}}],"bootstrap_source":{"type":"gcpsm","project":"p"}}`
	for _, raw := range []string{body, `{"output":` + body + `}`} {
		r, err := ParseRender([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.RequiredSecrets) != 1 || r.RequiredSecrets[0].Source.Pin["NAME"] != 1 || r.BootstrapSource.Project != "p" {
			t.Errorf("decoded %+v", r)
		}
	}
}

func TestEveryRuleIsListed(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range Rules {
		seen[r] = true
	}
	for _, r := range []string{RuleClusterKind, RuleESPolicy, RuleKeyCoverage, RuleUnreferenced, RuleImmutablePin, RuleOutOfBandReason, RuleWildcardRead, RuleEscrow, RuleSingleOwner} {
		if !seen[r] {
			t.Errorf("rule %s missing from Rules", r)
		}
	}
}

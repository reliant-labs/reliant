package openbao

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// prodBootstrap mirrors the names control-plane's provisioning created in
// reliant-labs-475814 (deploy/kcl/lib/openbao_bootstrap.k).
func prodBootstrap() *Bootstrap {
	return &Bootstrap{
		Project:              "reliant-labs-475814",
		ReaderServiceAccount: "openbao-bootstrap-reader",
		ReaderGSA:            "openbao-bootstrap-reader@reliant-labs-475814.iam.gserviceaccount.com",
		EscrowGSA:            "openbao-escrow-writer@reliant-labs-475814.iam.gserviceaccount.com",
		ESONamespace:         "external-secrets",
		SealSecretID:         "openbao-unseal-prod",
		PasswordSecretID:     "openbao-pg-password-prod",
		EscrowSecretID:       "openbao-recovery-prod",
	}
}

func bootstrapPostgresSpec() Spec {
	s := postgresSpec()
	s.Bootstrap = prodBootstrap()
	return s
}

func bootstrapRaftSpec() Spec {
	s := raftSpec()
	b := prodBootstrap()
	b.PasswordSecretID = ""
	s.Bootstrap = b
	return s
}

func find(t *testing.T, objs []*unstructured.Unstructured, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, o := range objs {
		if o.GetKind() == kind && o.GetName() == name {
			return o
		}
	}
	t.Fatalf("no %s %q among the rendered objects", kind, name)
	return nil
}

func has(objs []*unstructured.Unstructured, kind, name string) bool {
	for _, o := range objs {
		if o.GetKind() == kind && o.GetName() == name {
			return true
		}
	}
	return false
}

func nested(t *testing.T, o *unstructured.Unstructured, path ...string) any {
	t.Helper()
	v, found, err := unstructured.NestedFieldNoCopy(o.Object, path...)
	if err != nil || !found {
		t.Fatalf("%s/%s has no %s (err %v)", o.GetKind(), o.GetName(), strings.Join(path, "."), err)
	}
	return v
}

func TestBootstrap_RendersTheDeliveryObjects(t *testing.T) {
	objs, err := Render(bootstrapPostgresSpec())
	if err != nil {
		t.Fatal(err)
	}

	// The reader service account is bound to the reader GSA, and nothing mounts a token.
	sa := find(t, objs, "ServiceAccount", "openbao-bootstrap-reader")
	if got := sa.GetAnnotations()[AnnotationGCPServiceAccount]; got != prodBootstrap().ReaderGSA {
		t.Errorf("reader service account is federated to %q, want %q", got, prodBootstrap().ReaderGSA)
	}
	if v := nested(t, sa, "automountServiceAccountToken"); v != false {
		t.Errorf("the reader service account automounts a token: %v", v)
	}

	// ESO's controller may mint a token for THAT account and no other.
	role := find(t, objs, "Role", "openbao-bootstrap-token")
	rules := nested(t, role, "rules").([]any)
	if len(rules) != 1 {
		t.Fatalf("token Role has %d rules, want 1", len(rules))
	}
	rule := rules[0].(map[string]any)
	if got := rule["resourceNames"].([]any); len(got) != 1 || got[0] != "openbao-bootstrap-reader" {
		t.Errorf("token Role resourceNames = %v", got)
	}
	if got := rule["verbs"].([]any); len(got) != 1 || got[0] != "create" {
		t.Errorf("token Role verbs = %v", got)
	}
	rb := find(t, objs, "RoleBinding", "openbao-bootstrap-token")
	subj := nested(t, rb, "subjects").([]any)[0].(map[string]any)
	if subj["name"] != ESOServiceAccount || subj["namespace"] != "external-secrets" {
		t.Errorf("token RoleBinding subject = %v", subj)
	}

	// The store reads Secret Manager over Workload Identity: no key, no secretRef.
	store := find(t, objs, "SecretStore", "openbao-bootstrap")
	gcp := nested(t, store, "spec", "provider", "gcpsm").(map[string]any)
	if gcp["projectID"] != "reliant-labs-475814" {
		t.Errorf("projectID = %v", gcp["projectID"])
	}
	auth := gcp["auth"].(map[string]any)
	if _, ok := auth["secretRef"]; ok || len(auth) != 1 {
		t.Errorf("auth must be Workload Identity alone, got %v", auth)
	}
	if got := auth["workloadIdentity"].(map[string]any)["serviceAccountRef"].(map[string]any)["name"]; got != "openbao-bootstrap-reader" {
		t.Errorf("serviceAccountRef = %v", got)
	}

	// The seal key is read at version 1 and never "latest".
	seal := find(t, objs, "ExternalSecret", "openbao-unseal")
	d := nested(t, seal, "spec", "data").([]any)[0].(map[string]any)
	ref := d["remoteRef"].(map[string]any)
	if d["secretKey"] != StaticKeyKey || ref["key"] != "openbao-unseal-prod" || ref["version"] != SealSecretVersion {
		t.Errorf("seal ExternalSecret reads %v -> %v", ref, d["secretKey"])
	}

	// The DSN is composed around the password, escaped, through the proxy.
	dsn := find(t, objs, "ExternalSecret", "openbao-storage")
	tpl := nested(t, dsn, "spec", "target", "template", "data").(map[string]any)[ConnectionURLKey].(string)
	if want := "postgres://openbao:{{ .password | urlquery }}@127.0.0.1:5432/openbao?sslmode=disable"; tpl != want {
		t.Errorf("DSN template = %q, want %q", tpl, want)
	}
	pw := nested(t, dsn, "spec", "data").([]any)[0].(map[string]any)
	if pw["remoteRef"].(map[string]any)["key"] != "openbao-pg-password-prod" {
		t.Errorf("password remoteRef = %v", pw["remoteRef"])
	}
}

// The admission policy forge installs beside ESO refuses an ExternalSecret that
// is not Orphan/Merge + Retain, names another target, or refreshes faster than 5m.
func TestBootstrap_ExternalSecretsPassTheAdmissionPolicy(t *testing.T) {
	objs, err := Render(bootstrapPostgresSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openbao-unseal", "openbao-storage"} {
		es := find(t, objs, "ExternalSecret", name)
		if got := nested(t, es, "spec", "target", "creationPolicy"); got != "Orphan" {
			t.Errorf("%s creationPolicy = %v", name, got)
		}
		if got := nested(t, es, "spec", "target", "deletionPolicy"); got != "Retain" {
			t.Errorf("%s deletionPolicy = %v (a store outage or a prune must never delete the Secret the store runs on)", name, got)
		}
		if got := nested(t, es, "spec", "target", "name"); got != name {
			t.Errorf("%s writes Secret %v", name, got)
		}
		if got := nested(t, es, "spec", "refreshInterval"); got != BootstrapRefresh {
			t.Errorf("%s refresh = %v", name, got)
		}
		if _, found, _ := unstructured.NestedFieldNoCopy(es.Object, "spec", "dataFrom"); found {
			t.Errorf("%s uses dataFrom", name)
		}
		if kind := nested(t, es, "spec", "secretStoreRef", "kind"); kind != "SecretStore" {
			t.Errorf("%s references a %v", name, kind)
		}
	}
}

func TestBootstrap_RaftHasNoPasswordDelivery(t *testing.T) {
	objs, err := Render(bootstrapRaftSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !has(objs, "ExternalSecret", "openbao-unseal") {
		t.Error("the seal key is delivered for Raft too")
	}
	if has(objs, "ExternalSecret", "openbao-storage") {
		t.Error("Raft storage has no DSN to deliver")
	}
}

func TestBootstrap_AbsentRendersNone(t *testing.T) {
	for _, s := range []Spec{raftSpec(), postgresSpec()} {
		objs, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range objs {
			if o.GetKind() == "SecretStore" || o.GetKind() == "ExternalSecret" {
				t.Errorf("no bootstrap source declared, but %s/%s was rendered", o.GetKind(), o.GetName())
			}
			if _, ok := o.GetAnnotations()[AnnotationGCPServiceAccount]; ok && o.GetName() != "openbao" {
				t.Errorf("%s/%s carries a Workload Identity binding without a bootstrap source", o.GetKind(), o.GetName())
			}
		}
	}
}

// The escrow identity is the converge Job's own service account: not the
// server's (which carries the Cloud SQL GSA) and not the reader's.
func TestBootstrap_EscrowRunsAsTheConvergeAccount(t *testing.T) {
	s := bootstrapPostgresSpec()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	conv := find(t, objs, "ServiceAccount", ConvergeServiceAccount(s))
	if got := conv.GetAnnotations()[AnnotationGCPServiceAccount]; got != s.Bootstrap.EscrowGSA {
		t.Errorf("converge SA is federated to %q, want the escrow GSA %q", got, s.Bootstrap.EscrowGSA)
	}
	server := find(t, objs, "ServiceAccount", "openbao")
	if got := server.GetAnnotations()[AnnotationGCPServiceAccount]; got != s.Storage.Postgres.GCPServiceAccount {
		t.Errorf("the server SA carries %q, want the Cloud SQL GSA", got)
	}
	reader := find(t, objs, "ServiceAccount", "openbao-bootstrap-reader")
	if reader.GetAnnotations()[AnnotationGCPServiceAccount] == conv.GetAnnotations()[AnnotationGCPServiceAccount] {
		t.Error("the reader and the escrow writer share a Google identity")
	}

	for _, phase := range []string{"openbao-converge", "openbao-widen"} {
		job := jobNamed(t, objs, phase)
		env := jobEnv(job)
		for k, want := range map[string]string{
			"OPENBAO_ESCROW_PROJECT":  "reliant-labs-475814",
			"OPENBAO_ESCROW_SECRET":   "openbao-recovery-prod",
			"OPENBAO_ESCROW_LEDGER":   "openbao-escrow-ledger",
			"OPENBAO_ESCROW_IDENTITY": s.Bootstrap.EscrowGSA,
		} {
			if env[k] != want {
				t.Errorf("%s Job env %s = %q, want %q", phase, k, env[k], want)
			}
		}
	}
}

func jobNamed(t *testing.T, objs []*unstructured.Unstructured, jobName string) *unstructured.Unstructured {
	t.Helper()
	for _, o := range objs {
		if o.GetKind() == "Job" && o.GetLabels()[LabelJobName] == jobName {
			return o
		}
	}
	t.Fatalf("no Job %q", jobName)
	return nil
}

func jobEnv(job *unstructured.Unstructured) map[string]string {
	out := map[string]string{}
	for k, v := range containerEnv(job.Object) {
		out[k] = v
	}
	return out
}

// The escrow hook is mounted, its ConfigMap name covers the script (so a changed
// script is a changed Job), and the Job may write only the ledger it reads.
func TestBootstrap_EscrowHookIsMountedAndContentAddressed(t *testing.T) {
	s := bootstrapPostgresSpec()
	objs, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	want := EscrowConfigMap(s.Name)
	cm := find(t, objs, "ConfigMap", want)
	if got := nested(t, cm, "data").(map[string]any)["escrow.sh"]; got != EscrowScript {
		t.Error("the hook ConfigMap does not carry the embedded escrow script")
	}
	if !strings.HasSuffix(want, sum(EscrowScript)[:10]) {
		t.Errorf("hook ConfigMap %q is not addressed by its content", want)
	}

	job := jobNamed(t, objs, "openbao-converge")
	raw, _ := json.Marshal(job.Object)
	if !strings.Contains(string(raw), want) || !strings.Contains(string(raw), `"mountPath":"/escrow"`) {
		t.Errorf("the converge Job does not mount the escrow hook %s at /escrow", want)
	}

	// The ledger is one of the ConfigMaps the Job may touch, by name.
	role := find(t, objs, "Role", "openbao-converge")
	var named []string
	for _, r := range nested(t, role, "rules").([]any) {
		rule := r.(map[string]any)
		if res, _ := rule["resources"].([]any); len(res) == 1 && res[0] == "configmaps" {
			for _, n := range asStrings(rule["resourceNames"]) {
				named = append(named, n)
			}
		}
	}
	if !contains(named, s.EscrowLedger()) {
		t.Errorf("the converge Role cannot update the escrow ledger %q (it may touch %v)", s.EscrowLedger(), named)
	}

	// Changing the script changes the hook, and therefore the Job.
	prev := EscrowScript
	t.Cleanup(func() { EscrowScript = prev })
	EscrowScript += "\n# changed\n"
	objs2, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	if find(t, objs2, "Job", jobNamed(t, objs2, "openbao-converge").GetName()).GetName() == job.GetName() {
		t.Error("changing escrow.sh did not change the converge Job's name")
	}
}

func asStrings(v any) []string {
	var out []string
	items, _ := v.([]any)
	for _, x := range items {
		out = append(out, x.(string))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// A default-deny policy would leave the escrow Job unable to authenticate, so
// the Job may reach the GKE metadata server, and only under a bootstrap source.
func TestBootstrap_ConvergeEgressReachesTheMetadataServer(t *testing.T) {
	cidrs := func(spec Spec) []string {
		objs, err := Render(spec)
		if err != nil {
			t.Fatal(err)
		}
		np := find(t, objs, "NetworkPolicy", "openbao-converge")
		var out []string
		for _, r := range nested(t, np, "spec", "egress").([]any) {
			for _, to := range r.(map[string]any)["to"].([]any) {
				if ip, ok := to.(map[string]any)["ipBlock"].(map[string]any); ok {
					out = append(out, ip["cidr"].(string))
				}
			}
		}
		return out
	}
	with := cidrs(bootstrapPostgresSpec())
	for _, want := range []string{"169.254.169.252/32", "169.254.169.254/32"} {
		if !contains(with, want) {
			t.Errorf("with a bootstrap source the converge Job cannot reach %s (egress: %v)", want, with)
		}
	}
	without := cidrs(postgresSpec())
	for _, c := range without {
		if strings.HasPrefix(c, "169.254.") {
			t.Errorf("without a bootstrap source the converge Job may reach %s", c)
		}
	}
}

func TestBootstrap_ValidateRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		mut  func(*Spec)
		want string
	}{
		"reader is escrow": {func(s *Spec) { s.Bootstrap.EscrowGSA = s.Bootstrap.ReaderGSA }, "must not be the one that writes the escrow"},
		"bad reader gsa":   {func(s *Spec) { s.Bootstrap.ReaderGSA = "reader" }, "must be a Google service account email"},
		"bad escrow gsa":   {func(s *Spec) { s.Bootstrap.EscrowGSA = "" }, "must be a Google service account email"},
		"bad project":      {func(s *Spec) { s.Bootstrap.Project = "P" }, "not a Google Cloud project id"},
		"reader collides with converge": {func(s *Spec) {
			s.Bootstrap.ReaderServiceAccount = "openbao-converge"
		}, "collides with a service account the component already uses"},
		"reader collides with server": {func(s *Spec) { s.Bootstrap.ReaderServiceAccount = "openbao" }, "collides"},
		"duplicate secret ids": {func(s *Spec) {
			s.Bootstrap.EscrowSecretID = s.Bootstrap.SealSecretID
		}, "each bootstrap value is its own secret"},
		"bad secret id":    {func(s *Spec) { s.Bootstrap.SealSecretID = "a/b" }, "not a Secret Manager secret id"},
		"missing password": {func(s *Spec) { s.Bootstrap.PasswordSecretID = "" }, "passwordSecretID"},
		"hook not ours":    {func(s *Spec) { s.Hooks.Escrow = "mine" }, "a bootstrap source owns the escrow hook"},
		"bad eso ns":       {func(s *Spec) { s.Bootstrap.ESONamespace = "Bad NS" }, "esoNamespace"},
	} {
		t.Run(name, func(t *testing.T) {
			s := bootstrapPostgresSpec()
			tc.mut(&s)
			_, err := Render(s)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("password under raft", func(t *testing.T) {
		s := bootstrapRaftSpec()
		s.Bootstrap.PasswordSecretID = "openbao-pg-password-prod"
		_, err := Render(s)
		if err == nil || !strings.Contains(err.Error(), "only Postgres storage has a password") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("hostile role", func(t *testing.T) {
		s := bootstrapPostgresSpec()
		s.Storage.Postgres.Role = "openbao@evil"
		_, err := Render(s)
		if err == nil || !strings.Contains(err.Error(), "cannot carry unescaped") {
			t.Fatalf("a role that would change the DSN's shape must be refused: %v", err)
		}
	})
}

func TestBootstrap_Deterministic(t *testing.T) {
	a, err := Render(bootstrapPostgresSpec())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(bootstrapPostgresSpec())
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Error("the same spec rendered two different object sets")
	}
}

func TestBootstrap_EveryObjectIsOwnedAndManaged(t *testing.T) {
	objs, err := Render(bootstrapPostgresSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if o.GetLabels()[LabelOwner] == "" || o.GetLabels()[LabelManagedBy] != "forge" {
			t.Errorf("%s/%s is not stamped with the component owner: %v", o.GetKind(), o.GetName(), o.GetLabels())
		}
	}
}

// The hook runs in the converge image's bash, where a stray apostrophe inside a
// ${VAR:-default} swallows the rest of the file. Syntax is checked at unit
// level so it does not wait for the docker smoke.
func TestBootstrap_EscrowScriptParses(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	for name, script := range map[string]string{"escrow.sh": EscrowScript, "converge.sh": ConvergeScript} {
		// No file operand: bash reads the script from stdin. /dev/stdin is
		// not a path every bash has (Git for Windows' does not).
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse: %v\n%s", name, err, out)
		}
	}
	for _, want := range []string{"set -euo pipefail", "versions.add", "secretVersionAdder"} {
		if !strings.Contains(EscrowScript, want) {
			t.Errorf("escrow.sh lost %q", want)
		}
	}
	// The sink is write-only: it never reads the container or lists versions.
	for _, forbidden := range []string{":access", "/versions/latest", "versions?"} {
		if strings.Contains(EscrowScript, forbidden) {
			t.Errorf("escrow.sh reads from Secret Manager (%q), but its identity holds versions.add only", forbidden)
		}
	}
	for _, leak := range []string{"echo \"$token\"", "echo \"$uid\"", "cat \"$INIT_FILE\"", "set -x"} {
		if strings.Contains(EscrowScript, leak) {
			t.Errorf("escrow.sh can print a credential or recovery material (%q)", leak)
		}
	}
}

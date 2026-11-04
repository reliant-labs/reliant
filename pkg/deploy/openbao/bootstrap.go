package openbao

import (
	_ "embed"
	"fmt"
	"regexp"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EscrowScript is the escrow sink the converge Job runs when the bootstrap
// source is Google Secret Manager. It is embedded so the hook ConfigMap's name
// (and with it the Job's content address) covers it.
//
//go:embed escrow.sh
var EscrowScript string

// What the bootstrap source hands the component, and takes from it.
const (
	// AnnotationGCPServiceAccount binds a Kubernetes service account to a
	// Google service account under GKE Workload Identity.
	AnnotationGCPServiceAccount = "iam.gke.io/gcp-service-account"
	// DefaultESONamespace and ESOServiceAccount are the chart defaults the
	// External Secrets Operator runs as when the Bundle does not declare it.
	DefaultESONamespace = "external-secrets"
	ESOServiceAccount   = "external-secrets"
	// SealSecretVersion is the Secret Manager version the seal key is read at.
	// The key is written once and never rotated in place (a store is sealed
	// under the key it was initialised with), so reading version 1 and never
	// "latest" makes an extra version, from a lost generation race or a stray
	// `versions add`, unable to change what the store unseals with.
	SealSecretVersion = "1"
	// BootstrapRefresh is how often ESO re-reads the bootstrap values.
	BootstrapRefresh = "1h"
)

var (
	gsaRe      = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.iam\.gserviceaccount\.com$`)
	gcpProject = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	gsmIDRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,254}$`)
	// dsnPart is what may appear unescaped in the DSN literal the template
	// carries; the password is the only part that is not a literal.
	dsnPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

// ReaderServiceAccount is the service account, in the store's namespace, that
// the ESO `gcpsm` SecretStore logs in as. It is bound by Workload Identity to a
// Google service account that can read the seal key and the Postgres password
// and nothing else.
func (s Spec) ReaderServiceAccount() string {
	if s.Bootstrap != nil && s.Bootstrap.ReaderServiceAccount != "" {
		return s.Bootstrap.ReaderServiceAccount
	}
	return s.Name + "-bootstrap-reader"
}

// BootstrapStore is the name of the namespaced SecretStore over Secret Manager.
func (s Spec) BootstrapStore() string { return s.Name + "-bootstrap" }

// EscrowLedger is the ConfigMap that records which recovery Secret has been
// escrowed. The escrow identity can add versions but not read them, so "is the
// escrow empty" is answered here, in the cluster.
func (s Spec) EscrowLedger() string { return s.Name + "-escrow-ledger" }

// EscrowConfigMap is the content-addressed ConfigMap holding the escrow hook.
func EscrowConfigMap(name string) string { return name + "-escrow-" + sum(EscrowScript)[:10] }

func (s Spec) bootstrapStorageDSN() string {
	p := s.Storage.Postgres
	host, port, mode := "127.0.0.1", 5432, "disable" // the proxy is the TLS tunnel
	if p.CloudSQLInstance == "" {
		host, port, mode = p.Host, p.Port, "require"
	}
	return fmt.Sprintf("postgres://%s:{{ .password | urlquery }}@%s:%d/%s?sslmode=%s", p.Role, host, port, p.Database, mode)
}

// validateBootstrap refuses a bootstrap spec the renderer could not turn into
// working objects. Every message names the escape.
func (s Spec) validateBootstrap() []error {
	b := s.Bootstrap
	if b == nil {
		return nil
	}
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf("bootstrap: "+format, a...)) }

	if !gcpProject.MatchString(b.Project) {
		bad("project %q is not a Google Cloud project id (6-30 lowercase letters, digits and hyphens, starting with a letter)", b.Project)
	}
	for field, gsa := range map[string]string{"readerGSA": b.ReaderGSA, "escrowGSA": b.EscrowGSA} {
		if !gsaRe.MatchString(gsa) {
			bad("%s %q must be a Google service account email (name@project.iam.gserviceaccount.com)", field, gsa)
		}
	}
	if b.ReaderGSA != "" && b.ReaderGSA == b.EscrowGSA {
		bad("readerGSA and escrowGSA are both %q. The identity that reads the seal key must not be the one that writes the escrow: one may read, the other may only add a version", b.ReaderGSA)
	}
	if !dnsLabel.MatchString(b.ReaderServiceAccount) {
		bad("readerServiceAccount %q must be an RFC-1123 label", b.ReaderServiceAccount)
	}
	if b.ReaderServiceAccount == ConvergeServiceAccount(s) || b.ReaderServiceAccount == s.Name {
		bad("readerServiceAccount %q collides with a service account the component already uses (%s, %s)", b.ReaderServiceAccount, s.Name, ConvergeServiceAccount(s))
	}
	cluster := map[string]string{"clusterProject": b.ClusterProject, "clusterLocation": b.ClusterLocation, "clusterName": b.ClusterName}
	if set := b.ClusterProject != "" || b.ClusterLocation != "" || b.ClusterName != ""; set {
		for field, v := range cluster {
			if v == "" {
				bad("%s is empty but another of clusterProject / clusterLocation / clusterName is set: Workload Identity needs all three, or none (ESO then asks the metadata server)", field)
			}
		}
		if b.ClusterProject != "" && !gcpProject.MatchString(b.ClusterProject) {
			bad("clusterProject %q is not a Google Cloud project id", b.ClusterProject)
		}
	}
	if !dnsLabel.MatchString(b.ESONamespace) {
		bad("esoNamespace %q must be an RFC-1123 label", b.ESONamespace)
	}
	ids := map[string]string{"sealSecretID": b.SealSecretID, "escrowSecretID": b.EscrowSecretID}
	if s.Storage.Type == "postgres" {
		ids["passwordSecretID"] = b.PasswordSecretID
	} else if b.PasswordSecretID != "" {
		bad("passwordSecretID %q is set but storage is %s: only Postgres storage has a password to deliver", b.PasswordSecretID, s.Storage.Type)
	}
	seen := map[string]string{}
	for field, id := range ids {
		if !gsmIDRe.MatchString(id) {
			bad("%s %q is not a Secret Manager secret id (letters, digits, '-' and '_')", field, id)
			continue
		}
		if other, dup := seen[id]; dup {
			bad("%s and %s are both %q: each bootstrap value is its own secret", field, other, id)
		}
		seen[id] = field
	}
	if p := s.Storage.Postgres; s.Storage.Type == "postgres" && p != nil {
		for field, v := range map[string]string{"role": p.Role, "database": p.Database} {
			if !dsnPart.MatchString(v) {
				bad("storage.postgres.%s %q has characters the delivered DSN cannot carry unescaped (letters, digits, '_', '-', '.')", field, v)
			}
		}
		if p.Host != "" && !dsnPart.MatchString(p.Host) {
			bad("storage.postgres.host %q is not a hostname the delivered DSN can carry", p.Host)
		}
	}
	if s.Hooks.Escrow != "" && s.Hooks.Escrow != EscrowConfigMap(s.Name) {
		bad("hooks.escrow is %q, but a bootstrap source owns the escrow hook (%s). Drop hooks.escrow", s.Hooks.Escrow, EscrowConfigMap(s.Name))
	}
	return errs
}

// bootstrapObjects are the objects that deliver the bootstrap values into the
// namespace and escrow the recovery output out of it:
//
//   - the reader service account (Workload Identity -> the reader GSA), and the
//     one grant ESO's controller needs: a token for THAT account;
//   - a SecretStore over Secret Manager that logs in as it;
//   - an ExternalSecret per value: the seal key (read at version 1, forever)
//     and, for Postgres storage, the DSN composed around the password;
//   - the escrow hook the converge Job mounts.
//
// ESO writes the Secrets with creationPolicy Orphan and deletionPolicy Retain,
// so a Secret Manager outage, or a Flux prune of these objects, never removes
// the Secret the running store reads.
func (s Spec) bootstrapObjects() []any {
	b := s.Bootstrap
	ksa := s.ReaderServiceAccount()
	labels := toAnyMap(s.labels())
	meta := func(name string) map[string]any {
		return map[string]any{"name": name, "namespace": s.Namespace, "labels": labels}
	}

	reader := s.serviceAccount(ksa, map[string]string{AnnotationGCPServiceAccount: b.ReaderGSA})
	// ESO requests the token itself (TokenRequest); nothing mounts one.
	reader.AutomountServiceAccountToken = new(false)

	tokenName := s.Name + "-bootstrap-token"
	role := &rbacv1.Role{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: s.meta(tokenName),
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"serviceaccounts/token"}, ResourceNames: []string{ksa}, Verbs: []string{"create"},
		}},
	}
	binding := &rbacv1.RoleBinding{
		TypeMeta:   metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: s.meta(tokenName),
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: ESOServiceAccount, Namespace: b.ESONamespace}},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: tokenName},
	}

	wi := map[string]any{"serviceAccountRef": map[string]any{"name": ksa}}
	for k, v := range map[string]string{"clusterProjectID": b.ClusterProject, "clusterLocation": b.ClusterLocation, "clusterName": b.ClusterName} {
		if v != "" {
			wi[k] = v
		}
	}
	store := map[string]any{
		"apiVersion": "external-secrets.io/v1", "kind": "SecretStore", "metadata": meta(s.BootstrapStore()),
		"spec": map[string]any{"provider": map[string]any{"gcpsm": map[string]any{
			"projectID": b.Project,
			"auth":      map[string]any{"workloadIdentity": wi},
		}}},
	}

	externalSecret := func(name string, target map[string]any, data map[string]any) map[string]any {
		target["name"], target["creationPolicy"], target["deletionPolicy"] = name, "Orphan", "Retain"
		return map[string]any{
			"apiVersion": "external-secrets.io/v1", "kind": "ExternalSecret", "metadata": meta(name),
			"spec": map[string]any{
				"refreshInterval": BootstrapRefresh,
				"secretStoreRef":  map[string]any{"name": s.BootstrapStore(), "kind": "SecretStore"},
				"target":          target,
				"data":            []any{data},
			},
		}
	}
	objs := []any{
		reader, role, binding, store,
		externalSecret(s.UnsealSecret(), map[string]any{}, map[string]any{
			"secretKey": StaticKeyKey,
			"remoteRef": map[string]any{"key": b.SealSecretID, "version": SealSecretVersion},
		}),
	}
	if s.Storage.Type == "postgres" {
		objs = append(objs, externalSecret(s.StorageSecret(),
			map[string]any{"template": map[string]any{
				"engineVersion": "v2",
				"data":          map[string]any{ConnectionURLKey: s.bootstrapStorageDSN()},
			}},
			map[string]any{"secretKey": "password", "remoteRef": map[string]any{"key": b.PasswordSecretID}}))
	}
	objs = append(objs, s.configMap(s.Hooks.Escrow, map[string]string{"escrow.sh": EscrowScript}))
	return objs
}

// escrowEnv are the settings escrow.sh reads. The converge Job already carries
// the namespace and the recovery Secret's name.
func (s Spec) escrowEnv() [][2]string {
	return [][2]string{
		{"OPENBAO_ESCROW_PROJECT", s.Bootstrap.Project},
		{"OPENBAO_ESCROW_SECRET", s.Bootstrap.EscrowSecretID},
		{"OPENBAO_ESCROW_LEDGER", s.EscrowLedger()},
		{"OPENBAO_ESCROW_IDENTITY", s.Bootstrap.EscrowGSA},
	}
}

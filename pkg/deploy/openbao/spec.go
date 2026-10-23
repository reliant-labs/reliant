// Package openbao renders forge.OpenBao, the secret store an env runs itself.
//
// kcl/lib/openbao.k validates the declaration and emits ONE forge.dev/v1alpha1
// record of kind OpenBao whose spec is [Spec]; [Render] expands it into the
// Kubernetes objects, the same way pkg/deploy expands a ManagedDatabase. Both
// executors (`forge env deploy` and a Flux bundle build) call it, so it is PURE
// and DETERMINISTIC: same spec, byte-identical objects.
//
// # What it renders
//
//   - the server: a StatefulSet (Raft) or Deployment (Postgres), its Services,
//     ServiceAccount, config, PodDisruptionBudget, NetworkPolicy and, for
//     Postgres on Cloud SQL, the cloud-sql-proxy sidecar;
//   - TLS: a cert-manager self-signed CA Issuer and Certificate, unless the
//     spec brings its own Secret;
//   - two content-addressed Jobs that apply the access document ([AccessDoc],
//     derived with pkg/secretaccess) by running the embedded converge.sh:
//     a pre-rollout WIDEN Job and a post-rollout CONVERGE Job.
//
// # Seams other components fill in
//
//   - SF7 delivers the Secret [BootstrapSecretName] (the static seal key, and
//     the Postgres password) and may mount an escrow hook at [EscrowHookPath];
//   - SF8 is [Spec.Public] (public.go): the Gateway-side objects, the proxy
//     NetworkPolicy rule and the X-Forwarded-For stanza. It needs neither
//     [Spec.IngressFrom] nor [Spec.ExtraDNSNames]: the Gateway re-encrypts to the
//     Service's own name, so the certificate never carries the public host;
//   - SF5 reads the server URL, CA ConfigMap, mount and audience from the
//     resolved `openbao` block KCL emits beside the record.
//
//forge:exclude-contract: pkg/ library: pure renderer over a declared spec, one implementation
package openbao

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Record identity.
const (
	APIVersion = "forge.dev/v1alpha1"
	Kind       = "OpenBao"
)

// Labels forge stamps on what it renders.
const (
	LabelName      = "app.kubernetes.io/name"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPartOf    = "app.kubernetes.io/part-of"
	LabelEnv       = "forge.dev/env"
	LabelJobName   = "forge.dev/job-name"
	LabelSpecHash  = "forge.dev/spec-hash"
	// LabelOwner marks the env that owns the component. A second owner on the
	// same cluster is refused (by kind and label, never by name).
	LabelOwner = "forge.dev/component-owner"
	// LabelComponent is "openbao" on every object of this component.
	LabelComponent = "forge.dev/component"
	// AnnotationDeployPhase places a Job relative to the workloads
	// (pkg/release.AnnotationDeployPhase).
	AnnotationDeployPhase = "forge.dev/deploy-phase"
)

// Ports.
const (
	// ClusterPort is the Raft / HA forwarding port.
	ClusterPort = 8201
)

// Paths inside the pods and the converge Job.
const (
	// EscrowHookPath is where a component may mount an executable that is run
	// with the init output file as $1, after the in-cluster recovery Secret is
	// written and before anything is converged. A non-zero exit fails the run.
	EscrowHookPath = "/escrow/escrow.sh"
	// GenerateHookPath is run after the access document is applied, when
	// present. It is the seam for minting generated store values inside the
	// cluster.
	GenerateHookPath = "/generate/generate.sh"
)

// Spec is the `spec` of the OpenBao record kcl/lib/openbao.k emits.
type Spec struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Image     string `json:"image"`
	// KubectlImage runs the converge Jobs: kubectl, curl and jq. Empty =
	// [DefaultKubectlImage].
	KubectlImage string `json:"kubectlImage,omitempty"`
	// CloudSQLProxyImage is the sidecar for Postgres storage on Cloud SQL.
	// Empty = [DefaultCloudSQLProxyImage].
	CloudSQLProxyImage string `json:"cloudSQLProxyImage,omitempty"`
	Replicas           int    `json:"replicas"`
	// Owner is "<project>.<env>", stamped as [LabelOwner].
	Owner string `json:"owner"`

	Storage   Storage    `json:"storage"`
	Listeners []Listener `json:"listeners"`
	TLS       TLS        `json:"tls"`
	Seal      Seal       `json:"seal"`
	// RecoverySecret is the in-cluster Secret the converge Job writes the
	// `operator init` output to, before it converges anything.
	RecoverySecret string `json:"recoverySecret"`
	KV             KV     `json:"kv"`
	HA             HA     `json:"ha"`
	Audit          Audit  `json:"audit"`

	Consumers []Consumer `json:"consumers,omitempty"`
	Writer    Writer     `json:"writer"`
	Access    Access     `json:"access"`

	// Hooks are ConfigMaps of scripts the converge Job runs at two points:
	// the escrow hook right after init, the generate hook after the document
	// is applied. Empty = none. The names should be content-addressed so a
	// changed hook changes the Job.
	Hooks Hooks `json:"hooks,omitempty"`
	// Bootstrap delivers the values the store needs before it can serve a read
	// (the seal key, the Postgres password) from Google Secret Manager through
	// ESO, and escrows the `operator init` output back to it. Nil = the Secrets
	// are provided by something else (a prerequisite, or the env's file store).
	Bootstrap *Bootstrap `json:"bootstrap,omitempty"`
	// IngressNamespaces are namespaces whose pods may reach the API port:
	// ESO's, and every namespace that holds a SecretStore for this store.
	IngressNamespaces []string `json:"ingressNamespaces,omitempty"`
	// CANamespaces are the namespaces (beside OpenBao's own) that get the CA
	// published as a ConfigMap, so a SecretStore there can name it. They must
	// exist when the converge Job runs.
	CANamespaces []string `json:"caNamespaces,omitempty"`
	// IngressFrom are extra NetworkPolicy peers (the Gateway, for `public`).
	IngressFrom []Peer `json:"ingressFrom,omitempty"`
	// ExtraDNSNames are added to the server certificate (the public host).
	ExtraDNSNames []string `json:"extraDNSNames,omitempty"`
	// Public publishes the store on a Gateway (public.go). Nil = not published.
	Public *Public `json:"public,omitempty"`
}

// Bootstrap is the bootstrap source when it is Google Secret Manager. See
// bootstrap.go for what it renders.
type Bootstrap struct {
	// Project is the Google project holding the secrets.
	Project string `json:"project"`
	// ReaderServiceAccount is the Kubernetes service account, in the store's
	// namespace, that the ESO `gcpsm` SecretStore logs in as; ReaderGSA is the
	// Google service account it is federated to (secretAccessor on the seal
	// key and the Postgres password only).
	ReaderServiceAccount string `json:"readerServiceAccount"`
	ReaderGSA            string `json:"readerGSA"`
	// EscrowGSA is the Google service account the converge Job's service
	// account is federated to: versions.add on the recovery escrow only.
	EscrowGSA string `json:"escrowGSA"`
	// ClusterProject, ClusterLocation and ClusterName say which GKE cluster the
	// store runs on, for Workload Identity. All empty = ESO asks the GKE
	// metadata server, which a NetworkPolicy may block; the project can differ
	// from Project, where the secrets are.
	ClusterProject  string `json:"clusterProject,omitempty"`
	ClusterLocation string `json:"clusterLocation,omitempty"`
	ClusterName     string `json:"clusterName,omitempty"`
	// ESONamespace is where the External Secrets Operator runs; it is granted
	// a token for ReaderServiceAccount and nothing else.
	ESONamespace string `json:"esoNamespace"`
	// SealSecretID, PasswordSecretID and EscrowSecretID are the secret ids of
	// the seal key, the Postgres password (Postgres storage only) and the
	// recovery escrow.
	SealSecretID     string `json:"sealSecretID"`
	PasswordSecretID string `json:"passwordSecretID,omitempty"`
	EscrowSecretID   string `json:"escrowSecretID"`
}

// Hooks names the ConfigMaps holding the converge Job's optional hooks.
// ConfigMap keys are the files: escrow.sh and generate.sh.
type Hooks struct {
	Escrow   string `json:"escrow,omitempty"`
	Generate string `json:"generate,omitempty"`
}

// Storage is a union: exactly one of Raft or Postgres is set, named by Type.
type Storage struct {
	Type     string    `json:"type"`
	Raft     *Raft     `json:"raft,omitempty"`
	Postgres *Postgres `json:"postgres,omitempty"`
}

// Raft is integrated storage on PersistentVolumeClaims.
type Raft struct {
	SizeGiB      int       `json:"sizeGiB"`
	StorageClass string    `json:"storageClass,omitempty"`
	Snapshot     *Snapshot `json:"snapshot,omitempty"`
}

// Snapshot is a Raft snapshot CronJob to a bucket.
type Snapshot struct {
	Bucket   string `json:"bucket"`
	Schedule string `json:"schedule"`
	Keep     int    `json:"keep"`
}

// Postgres is PostgreSQL storage, on Cloud SQL (via the proxy) or on a Host.
type Postgres struct {
	CloudSQLInstance  string `json:"cloudSQLInstance,omitempty"`
	Host              string `json:"host,omitempty"`
	Port              int    `json:"port"`
	Database          string `json:"database"`
	Role              string `json:"role"`
	GCPServiceAccount string `json:"gcpServiceAccount,omitempty"`
	// PrivateIP makes the proxy dial the instance's private address (an
	// instance with no public IP).
	PrivateIP   bool `json:"privateIP,omitempty"`
	MaxParallel int  `json:"maxParallel"`
}

// Listener is one API listener.
type Listener struct {
	Port               int    `json:"port"`
	TLS                bool   `json:"tls"`
	TransitionalReason string `json:"transitionalReason,omitempty"`
}

// TLS says where the listeners' certificate comes from.
type TLS struct {
	// Secret is a BYO kubernetes.io/tls Secret. Empty = cert-manager issues
	// one from a namespaced self-signed CA.
	Secret string `json:"secret,omitempty"`
	// CAConfigMap names the ConfigMap holding the CA that signed the
	// certificate. For the self-signed default the converge Job publishes it.
	CAConfigMap string `json:"caConfigMap"`
	Duration    string `json:"duration"`
}

// Seal is the static auto-unseal.
type Seal struct {
	KeyID string `json:"keyID"`
}

// KV is the KV-v2 mount.
type KV struct {
	Mount       string `json:"mount"`
	MaxVersions int    `json:"maxVersions"`
}

// HA is availability.
type HA struct {
	PDB           bool   `json:"pdb"`
	ZoneSpread    bool   `json:"zoneSpread"`
	PriorityClass string `json:"priorityClass,omitempty"`
}

// Audit is the audit devices.
type Audit struct {
	Stdout   bool   `json:"stdout"`
	File     bool   `json:"file"`
	FilePath string `json:"filePath"`
}

// Consumer is a static principal with exact grants.
type Consumer struct {
	Name      string  `json:"name"`
	Workload  string  `json:"workload"`
	Namespace string  `json:"namespace"`
	Grants    []Grant `json:"grants,omitempty"`
	Delegated bool    `json:"delegated,omitempty"`
}

// Grant is one ACL rule.
type Grant struct {
	Path               string   `json:"path"`
	Capabilities       []string `json:"capabilities"`
	WildcardReadReason string   `json:"wildcardReadReason,omitempty"`
}

// Writer is how a person writes values.
type Writer struct {
	Type string `json:"type"`
	// ServiceAccount is the SA forge renders for Type "service_account".
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// Role is the ManagedIdentity role for Type "oidc" (recorded; the OIDC
	// wiring is not rendered here).
	Role string `json:"role,omitempty"`
}

// Peer is one NetworkPolicy ingress peer, as the NetworkPolicy API spells it.
type Peer struct {
	NamespaceLabels map[string]string `json:"namespaceLabels,omitempty"`
	PodLabels       map[string]string `json:"podLabels,omitempty"`
}

// Access is everything pkg/secretaccess needs, plus the self-cluster identity.
type Access struct {
	Org            string          `json:"org"`
	Env            string          `json:"env"`
	Root           string          `json:"root,omitempty"`
	ServiceAccount string          `json:"serviceAccount,omitempty"`
	SelfCluster    string          `json:"selfCluster"`
	Clusters       []AccessCluster `json:"clusters"`
	Bindings       []Binding       `json:"bindings,omitempty"`
}

// AccessCluster is a cluster whose service-account tokens may log in.
type AccessCluster struct {
	ID       string `json:"id"`
	Audience string `json:"audience"`
	// Exactly one of OIDCDiscoveryURL / PublicKeys, or neither for the self
	// cluster, whose keys the converge Job reads from its own API server.
	OIDCDiscoveryURL string   `json:"oidcDiscoveryURL,omitempty"`
	BoundIssuer      string   `json:"boundIssuer,omitempty"`
	PublicKeys       []string `json:"publicKeys,omitempty"`
}

// Binding is the set of names one namespace of one cluster reads.
type Binding struct {
	Cluster   string     `json:"cluster"`
	Namespace string     `json:"namespace"`
	Names     []string   `json:"names"`
	Wildcards []Wildcard `json:"wildcards,omitempty"`
}

// Wildcard is a reasoned glob read inside the env's directory.
type Wildcard struct {
	Pattern string `json:"pattern"`
	Reason  string `json:"reason"`
}

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	durRe    = regexp.MustCompile(`^[0-9]+(ns|us|µs|ms|s|m|h)([0-9]+(ns|us|µs|ms|s|m|h))*$`)
)

// Validate refuses a spec the renderer could not render safely. Every message
// names the escape. It re-states the rules KCL enforces, because a spec also
// reaches this package from callers that never ran KCL.
func (s Spec) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !dnsLabel.MatchString(s.Name) || len(s.Name) > 40 {
		bad("name %q must be an RFC-1123 label of at most 40 characters", s.Name)
	}
	if !dnsLabel.MatchString(s.Namespace) {
		bad("namespace %q must be an RFC-1123 label", s.Namespace)
	}
	if s.Image == "" {
		bad("image is required")
	}
	if s.Replicas < 1 || s.Replicas > 9 {
		bad("replicas %d must be 1..9", s.Replicas)
	}
	switch s.Storage.Type {
	case "raft":
		if s.Storage.Raft == nil {
			bad("storage.type raft needs storage.raft")
		} else if s.Replicas%2 == 0 {
			bad("replicas %d with Raft storage: Raft needs an odd number of voters to keep a quorum. Use %d or %d, or Postgres storage", s.Replicas, s.Replicas+1, s.Replicas-1)
		}
	case "postgres":
		p := s.Storage.Postgres
		switch {
		case p == nil:
			bad("storage.type postgres needs storage.postgres")
		case (p.CloudSQLInstance == "") == (p.Host == ""):
			bad("storage.postgres needs exactly one of cloudSQLInstance or host")
		case p.CloudSQLInstance != "" && (p.GCPServiceAccount == "" || s.CloudSQLProxyImage == ""):
			bad("storage.postgres.cloudSQLInstance needs gcpServiceAccount (Workload Identity) and cloudSQLProxyImage")
		}
	default:
		bad("storage.type %q must be raft or postgres", s.Storage.Type)
	}

	tlsListeners := 0
	seen := map[int]bool{}
	for _, l := range s.Listeners {
		if seen[l.Port] || l.Port == ClusterPort {
			bad("listener port %d is declared twice or collides with the cluster port %d", l.Port, ClusterPort)
		}
		seen[l.Port] = true
		switch {
		case l.TLS:
			tlsListeners++
			if l.TransitionalReason != "" {
				bad("listener %d sets a transitionalReason but has TLS on: the reason excuses a plaintext listener", l.Port)
			}
		case l.TransitionalReason == "":
			bad("listener %d is plaintext and states no transitionalReason. A store that hands out secrets does not speak plaintext: keep TLS on, or set a transitionalReason that says when this listener goes away", l.Port)
		}
	}
	if tlsListeners == 0 {
		bad("no TLS listener: a plaintext listener is only transitional, beside a TLS one")
	} else if !s.Listeners[0].TLS {
		bad("the first listener carries the cluster port and must be the TLS one")
	}

	if s.TLS.Secret != "" && s.TLS.CAConfigMap == "" {
		bad("tls.secret %q needs tls.caConfigMap: clients must trust the CA that signed it", s.TLS.Secret)
	}
	if s.TLS.CAConfigMap == "" {
		bad("tls.caConfigMap is required")
	}
	if !durRe.MatchString(s.TLS.Duration) && s.TLS.Secret == "" {
		bad("tls.duration %q is not a duration", s.TLS.Duration)
	}
	if s.Seal.KeyID == "" {
		bad("seal.keyID is required")
	}
	if s.RecoverySecret == "" {
		bad("recoverySecret is required")
	}
	if s.KV.Mount == "" || s.KV.MaxVersions < 2 {
		bad("kv needs a mount and maxVersions >= 2")
	}
	if !s.Audit.Stdout && !s.Audit.File {
		bad("audit has no device: a secret store with no audit trail is refused. Keep stdout or file")
	}
	if s.Access.Org == "" || s.Access.Env == "" || s.Access.SelfCluster == "" {
		bad("access needs org, env and selfCluster")
	}

	errs = append(errs, s.validatePublic()...)
	errs = append(errs, s.validateBootstrap()...)

	names := map[string]bool{}
	for _, c := range s.Consumers {
		if names[c.Name] {
			bad("consumer %q is declared twice", c.Name)
		}
		names[c.Name] = true
		if len(c.Grants) == 0 && !c.Delegated {
			bad("consumer %q has no grants and is not delegated", c.Name)
		}
		for _, g := range c.Grants {
			if err := g.validate(c.Name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// validate is the wildcard-read refusal: a read on a glob exposes every value
// under it, so it states why exact paths cannot be named.
func (g Grant) validate(consumer string) error {
	reads := false
	for _, c := range g.Capabilities {
		if c == "read" {
			reads = true
		}
		if c == "sudo" || c == "deny" {
			return fmt.Errorf("consumer %q grant %q: %q is not grantable here", consumer, g.Path, c)
		}
	}
	if len(g.Capabilities) == 0 {
		return fmt.Errorf("consumer %q grant %q lists no capabilities", consumer, g.Path)
	}
	if reads && strings.ContainsAny(g.Path, "*+") && strings.TrimSpace(g.WildcardReadReason) == "" {
		return fmt.Errorf("consumer %q grant %q grants read on a glob and states no wildcardReadReason. A glob read exposes every value under it. Grant exact paths instead, or set wildcardReadReason to say why they cannot be named", consumer, g.Path)
	}
	return nil
}

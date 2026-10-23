package deploy

import (
	"errors"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Context carries the TARGET facts a spec deliberately does not: where the
// objects land, which project and environment they belong to, and the
// env-wide network policy.
//
// Nothing hosted is in here. There is no org, no customer, no allocated
// hostname and no image allowlist. The control plane stamps those AROUND the
// renderer's output. If the renderer took them as inputs it would stop being
// the same function for both destinations.
type Context struct {
	// Namespace every rendered object lands in. Required.
	Namespace string
	// PartOf is the app.kubernetes.io/part-of value (the project). Empty
	// falls back to each object's own name (kcl/lib/labels.k:185-191).
	PartOf string
	// Env is the forge.dev/env ownership stamp (kcl/lib/labels.k). It is
	// written to every object's labels and every pod template's labels,
	// never to a selector. Empty emits no label, matching labels.k's
	// "no -D env= binding" case.
	Env string
	// Network, when set, adds the ENV-WIDE NetworkPolicy bundle
	// (kcl/lib/netpol.k): default-deny plus the DNS, same-namespace,
	// external-egress, telemetry and ingress-controller allows.
	//
	// WHY IT IS HERE, AND OPTIONAL. The per-workload ingress policy is a
	// fact about one workload (which of ITS ports may be dialled, and from
	// where), so RenderWorkloads always emits it. The bundle is a fact about
	// the NAMESPACE: which egress the environment's database, collector and
	// gateway need. RenderWorkloads already renders a whole env's set, which
	// is the one place that knows every workload shares the namespace, so
	// the bundle rides the same call instead of a second KCL renderer
	// (kcl/workloads/render.k:171 today). It is a pointer because the two
	// executors want different answers: a self-hosted env declares its
	// egress here, while the hosted control plane passes nil and layers its
	// own per-customer egress rules on the namespace it owns.
	Network *EnvNetworkPolicy
	// ImagePullSecrets are names of EXISTING image-pull Secrets in Namespace
	// (a private registry's credentials, e.g. ghcr-creds). An environment
	// fact, not a workload one: the same workload pulls anonymously from a
	// local registry in dev and with a credential in prod.
	//
	// PLACEMENT, as lib/rbac.k and lib/services.k:487 had it: on every
	// ServiceAccount forge generates (so every pod, Job and CronJob bound to
	// it pulls with them, and the pod spec stays untouched), and on the POD
	// SPEC only for a workload with a serviceAccount override, because forge
	// does not own that ServiceAccount and Kubernetes unions pod-level pull
	// secrets with the SA's own.
	ImagePullSecrets []string

	// DatabaseBackup, when set, is the object store a ManagedDatabase
	// archives WAL and base backups to. Nil renders a Cluster with no
	// backup stanza — see RenderManagedDatabase for why that stayed the
	// default rather than becoming an error.
	//
	// IT IS A TARGET FACT, NOT A SPEC FIELD, for the same reason Namespace
	// is: which bucket a database backs up to, and with which identity, is
	// decided by whoever runs the environment. A hosted user declares that
	// they want a database and (via spec.Restore) where to recover it
	// from; they never name a bucket, and a spec field would let them name
	// somebody else's.
	DatabaseBackup *DatabaseBackup

	// DatabasePlacement pins a ManagedDatabase's instances onto the nodes
	// that may hold data. Empty renders no placement, which is what a
	// single-pool cluster wants.
	DatabasePlacement *DatabasePlacement
}

// DatabaseBackup is the object store behind the managed-database tier, as the
// CNPG-I Barman Cloud plugin consumes it.
//
// THE PLUGIN, NOT spec.backup.barmanObjectStore. CNPG deprecated the in-tree
// stanza in 1.26 and is removing it; a data tier that has to be migrated off
// its backup mechanism after it holds customer rows is strictly worse than
// one that adopts the replacement while it holds none.
type DatabaseBackup struct {
	// Bucket is the destination, as a barman destinationPath WITHOUT the
	// per-database suffix: "s3://forge-db-backups/<org>/<env>" or
	// "gs://...". RenderManagedDatabase appends the database's own segment,
	// so two databases can never share a prefix.
	// +required
	Bucket string

	// EndpointURL overrides the object store endpoint (an in-cluster
	// S3-compatible store in dev and e2e). Empty uses the provider's real
	// endpoint, which is what prod wants.
	EndpointURL string

	// CredentialSecret names a Secret in the database's namespace holding
	// ACCESS_KEY_ID and ACCESS_SECRET_KEY. Empty means NO static
	// credential: on GCS the instance authenticates with Workload Identity
	// (googleCredentials.gkeEnvironment), which is the prod mode and the
	// reason this is not required.
	CredentialSecret string

	// RetentionPolicy is a barman retention window ("30d"). Empty keeps
	// backups forever, which is a cost decision rather than a safe default,
	// so environments set it.
	RetentionPolicy string

	// ServerName overrides the WAL stream this database archives under,
	// inside the bucket prefix. Empty derives one from the database name
	// (see databaseServerName).
	//
	// THE PLATFORM OWNS STREAM IDENTITY BECAUSE THE DERIVED NAME IS NOT
	// ENOUGH FOR A REPEATED IN-PLACE RESTORE. Restoring "app" from "app"
	// derives the stream "app-restored". Do it a SECOND time — the first
	// restore picked the wrong timestamp, which is the single most likely
	// reason to restore twice — and the derived name is unchanged, so the
	// new cluster tries to archive into a prefix that already has WAL in
	// it. barman refuses ("Expected empty archive"), the recovery job
	// crash-loops, and the failure arrives during an incident.
	//
	// An executor that can restore more than once must therefore allocate
	// a fresh stream per restore (a counter, the deployment generation, a
	// timestamp) and pass it here. forge cannot derive it: the number of
	// times this database has been restored is not in the spec.
	ServerName string
}

// DatabasePlacement is where a database's instances may run. A data tier's
// placement is not the same question as a stateless workload's: these pods
// own persistent volumes, so the nodes they land on decide where the data
// physically lives and whether it survives a pool being recycled.
type DatabasePlacement struct {
	// NodeSelector restricts instances to matching nodes.
	NodeSelector map[string]string
	// Tolerations let instances onto tainted nodes.
	Tolerations []v1alpha1.Toleration
}

// EnvNetworkPolicy is the input to the env-wide NetworkPolicy bundle
// (kcl/lib/netpol.k:53, WorkloadEnv's egress_ports / telemetry_namespace /
// ingress_namespace, kcl/workloads/render.k:96-116).
type EnvNetworkPolicy struct {
	// EgressPorts are TCP ports every pod may dial anywhere: 443 for HTTPS,
	// 5432 for a managed Postgres outside the cluster. Empty omits the
	// allow-egress-external policy.
	EgressPorts []int32
	// TelemetryNamespace runs the OTLP collector. Empty omits the policy.
	TelemetryNamespace string
	// IngressNamespace runs the Gateway / ingress controller. Empty omits
	// the policy.
	IngressNamespace string
}

func (c Context) validate() error {
	if c.Namespace == "" {
		return errors.New("render context has no namespace: every tier object is namespaced, and guessing one is how a workload lands in another customer's namespace")
	}
	return nil
}

// RunAsUser is the identity every rendered pod runs under. It is forge's value
// (lib/services.k) and control-plane's (Config.RunAsUser) alike.
const RunAsUser int64 = 65532

// Label keys forge stamps on every object it renders (kcl/lib/labels.k
// managed_labels). Selectors use ONLY LabelName, because a selector is
// immutable on a Deployment and part-of must stay free to change.
const (
	LabelName      = "app.kubernetes.io/name"
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPartOf    = "app.kubernetes.io/part-of"
	// LabelEnv is the env-ownership stamp (kcl/lib/labels.k:84).
	LabelEnv = "forge.dev/env"
	// LabelJobName is a Job's STABLE handle, since its metadata.name carries
	// a spec hash (kcl/workloads/expand.k:706-709).
	LabelJobName = "forge.dev/job-name"
	// LabelSpecHash answers "did this Job actually change?" without a YAML
	// diff.
	LabelSpecHash = "forge.dev/spec-hash"
)

// AnnotationDeployPhase places a standalone Job in the deploy
// (kcl/workloads/expand.k:714-718). internal/cluster reads it.
const AnnotationDeployPhase = "forge.dev/deploy-phase"

// AnnotationDeletionPolicy is stamped on a ManagedDatabase's Cluster so that
// whatever PRUNES rendered objects can see the author's retain decision
// without reading the spec. A retain-policy Cluster must never be pruned:
// deleting it deletes its volumes.
const AnnotationDeletionPolicy = "forge.dev/deletion-policy"

// managedLabels ports kcl/lib/labels.k:185-191.
func managedLabels(name, partOf string) map[string]string {
	if partOf == "" {
		partOf = name
	}
	return map[string]string{LabelName: name, LabelManagedBy: "forge", LabelPartOf: partOf}
}

// Render renders one StaticSite or ManagedDatabase. The object's
// metadata.name is the resource name and its spec is the declaration. Its
// metadata.namespace is NOT consulted: on hosted the CR lives where the
// control plane keeps it, not where the workload runs, so the target is
// always ctx.Namespace.
//
// A Workload is REFUSED here. Its `before` ordering and workloadURL
// references are relations between workloads, so one Workload cannot be
// rendered correctly on its own. Render the env's whole set with
// RenderWorkloads.
//
// StaticSite renders NO Kubernetes objects. Its executor is the release
// planner (bucket sync, retention, CDN invalidation), not an apply, so it
// returns an empty list rather than an error: a caller rendering a whole
// environment should not have to special-case it.
func Render(obj runtime.Object, ctx Context) ([]*unstructured.Unstructured, error) {
	switch o := obj.(type) {
	case *v1alpha1.ManagedDatabase:
		return RenderManagedDatabase(o.Name, o.Spec, ctx)
	case *v1alpha1.StaticSite:
		if err := o.Spec.Validate(); err != nil {
			return nil, fmt.Errorf("staticsite %s: %w", o.Name, err)
		}
		return nil, nil
	case *v1alpha1.Workload:
		return nil, fmt.Errorf("deploy.Render: workload %s must be rendered with its environment's other workloads (deploy.RenderWorkloads): `before` and workloadURL are resolved across the set", o.Name)
	default:
		return nil, fmt.Errorf("deploy.Render: %T is not a forge.dev tier", obj)
	}
}

// DatabaseCredentialSecretName is the Secret CloudNativePG publishes for a
// Cluster's application role: "<cluster>-app", in the Cluster's namespace.
// This is a CONTRACT WITH CNPG, not a name forge chooses. A DatabaseRef env
// var reads it directly, because the Cluster renders into the same namespace
// as the workload that references it.
func DatabaseCredentialSecretName(databaseName string) string { return databaseName + "-app" }

// DatabaseIdentifiers are the Postgres objects a ManagedDatabase creates. The
// Cluster name is the resource name unchanged. The database is that name with
// hyphens folded to underscores, so no reference ever has to remember to quote
// it. The role is the database plus "_app": it is distinct from the database
// and it is never `postgres`.
func DatabaseIdentifiers(name string) (database, role string) {
	database = strings.ReplaceAll(name, "-", "_")
	return database, database + "_app"
}

// ReadOnlyRoleConnectionLimit caps the read-only role's concurrent sessions.
// It bounds the load an agent's or the platform's queries can put on a primary
// the application is serving from.
const ReadOnlyRoleConnectionLimit int64 = 10

// readOnlyRole is the CNPG managed-role entry for dbgrants.RoleName.
//
// WHAT THE ROLE IS NOT: a superuser, a creator of roles or databases, a
// replication or RLS-bypassing role, or a member of any role (pg_read_all_data
// would make the column grants moot). What it MAY read is decided by the
// grants the migrator applies (pkg/dbgrants), not here; this entry only makes
// it exist and log in. CNPG converges it, so nobody runs a setup step.
//
// The password is a Secret the EXECUTOR mints (ReadOnlyRoleSecret): a render is
// pure, and a random password is not.
func readOnlyRole(database string) map[string]any {
	return map[string]any{
		"name":            dbgrants.RoleName,
		"ensure":          "present",
		"comment":         "forge read-only role: column grants only, no forge:secret column, no write privilege",
		"login":           true,
		"superuser":       false,
		"createdb":        false,
		"createrole":      false,
		"replication":     false,
		"bypassrls":       false,
		"inherit":         false,
		"connectionLimit": ReadOnlyRoleConnectionLimit,
		"passwordSecret":  map[string]any{"name": dbgrants.SecretName(database)},
	}
}

// ReadOnlyRoleSecret renders the kubernetes.io/basic-auth Secret that carries
// the read-only role's credentials for the ManagedDatabase called name, in
// ctx.Namespace. CNPG reads it as the managed role's passwordSecret, and a
// client that queries the database as the role (`forge db query`, the control
// plane) reads the same Secret.
//
// THE CALLER SUPPLIES THE PASSWORD and must create the Secret only if absent:
// rotating it on every apply would lock out a client that has the old one, and
// a render that invented a password could not be byte-identical twice. The
// `cnpg.io/reload` label makes CNPG apply a changed password immediately
// rather than at its next reconcile.
func ReadOnlyRoleSecret(name, password string, ctx Context) (*unstructured.Unstructured, error) {
	if err := ctx.validate(); err != nil {
		return nil, err
	}
	if err := v1alpha1.ValidateDatabaseName(name); err != nil {
		return nil, err
	}
	if password == "" {
		return nil, errors.New("deploy.ReadOnlyRoleSecret: a read-only role needs a password")
	}
	labels := map[string]any{"cnpg.io/reload": "true"}
	for k, v := range managedLabels(name, ctx.PartOf) {
		labels[k] = v
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/basic-auth",
		"metadata": map[string]any{
			"name":      dbgrants.SecretName(name),
			"namespace": ctx.Namespace,
			"labels":    labels,
		},
		"stringData": map[string]any{"username": dbgrants.RoleName, "password": password},
	}}
	stampEnv([]*unstructured.Unstructured{obj}, ctx.Env)
	return obj, nil
}

// RenderManagedDatabase renders a ManagedDatabase into a CloudNativePG Cluster.
//
// This is control-plane's closed shape (manageddatabase.BuildClusterPlan),
// the one that ran live. enableSuperuserAccess is written as false rather
// than left to CNPG's default: the value is the difference between a customer
// holding an owner role and holding `postgres`, and a CNPG release changing
// its default must not change that.
//
// # Backups are rendered when, and only when, the target offers a store
//
// ctx.DatabaseBackup nil still renders a Cluster with NO backup stanza, and
// that remains deliberate: a Cluster carrying a backup section it cannot
// reach would LOOK backed up while archiving nothing. What changed is that it
// is no longer the ONLY thing this renders — an environment that has declared
// an object store gets continuous WAL archiving, scheduled base backups and a
// retention window, which is what the gate in control-plane's
// manageddatabase/doc.go was waiting for.
//
// The stanza is the CNPG-I Barman Cloud plugin (`spec.plugins`), not
// `spec.backup.barmanObjectStore`: the in-tree form is deprecated from CNPG
// 1.26 and slated for removal, and moving a tier off its backup mechanism
// after it holds customer data is the migration nobody wants to schedule.
//
// # serverName, and why a restore may not reuse one
//
// Each database archives under its OWN serverName inside the bucket prefix,
// and a RESTORED database is given a fresh one (`<name>-r<n>`). This is not
// tidiness. barman-cloud refuses to archive into a non-empty prefix: a
// restored Cluster pointed at its source's serverName fails
// barman-cloud-check-wal-archive with "Expected empty archive", the recovery
// job crash-loops, and the restore never completes. Verified against CNPG
// 1.30.0 with plugin v0.15.0 — the first restore attempt failed exactly this
// way, and giving the restored cluster its own serverName fixed it.
//
// A fresh serverName also means a restore never writes into the WAL stream it
// is reading, so a botched restore cannot corrupt the backups it came from.
//
// The Cluster is unstructured on purpose. Importing CNPG's typed API would
// pull the whole operator module into every forge consumer to type-check six
// fields.
//
// Self-hosted, applying this requires the CNPG operator in the cluster (and,
// for backups, the Barman Cloud plugin). The executor should detect the
// postgresql.cnpg.io CRD and fail with a runbook if it is missing.
func RenderManagedDatabase(name string, spec v1alpha1.ManagedDatabaseSpec, ctx Context) ([]*unstructured.Unstructured, error) {
	if err := ctx.validate(); err != nil {
		return nil, err
	}
	if err := v1alpha1.ValidateDatabaseName(name); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("manageddatabase %s: %w", name, err)
	}
	spec = spec.WithDefaults()
	database, role := DatabaseIdentifiers(name)

	labels := map[string]any{}
	for k, v := range managedLabels(name, ctx.PartOf) {
		labels[k] = v
	}
	clusterSpec := map[string]any{
		"instances":             int64(spec.Instances),
		"enableSuperuserAccess": false,
		"bootstrap": map[string]any{
			"initdb": map[string]any{"database": database, "owner": role},
		},
		"storage": map[string]any{"size": fmt.Sprintf("%dGi", spec.StorageGiB)},
		"managed": map[string]any{"roles": []any{readOnlyRole(name)}},
	}
	if p := ctx.DatabasePlacement; p != nil {
		if len(p.NodeSelector) > 0 {
			sel := map[string]any{}
			for k, v := range p.NodeSelector {
				sel[k] = v
			}
			clusterSpec["nodeSelector"] = sel
		}
		if tols := databaseTolerations(p.Tolerations); len(tols) > 0 {
			clusterSpec["tolerations"] = tols
		}
	}

	objs := []*unstructured.Unstructured{}
	if b := ctx.DatabaseBackup; b != nil {
		store, err := renderObjectStore(name, spec, ctx, *b)
		if err != nil {
			return nil, err
		}
		objs = append(objs, store)

		// The archiving serverName. A restored database gets its own, so
		// it never writes into the stream it recovered from — barman
		// refuses a non-empty archive outright.
		clusterSpec["plugins"] = []any{map[string]any{
			"name":          barmanPluginName,
			"isWALArchiver": true,
			"parameters": map[string]any{
				"barmanObjectName": name,
				"serverName":       databaseServerName(name, spec.Restore, b.ServerName),
			},
		}}
	}

	if r := spec.Restore; r != nil {
		if ctx.DatabaseBackup == nil {
			return nil, fmt.Errorf("manageddatabase %s: spec.restore needs an object store, and this environment declares none; a restore cannot read backups that were never taken", name)
		}
		recoveryTarget := map[string]any{}
		if r.PointInTime != "" {
			recoveryTarget["targetTime"] = r.PointInTime
		}
		recovery := map[string]any{"source": restoreSourceName}
		if len(recoveryTarget) > 0 {
			recovery["recoveryTarget"] = recoveryTarget
		}
		// REPLACES initdb. A Cluster bootstraps one way or the other, and
		// declaring both is how you get an empty database that reports success.
		clusterSpec["bootstrap"] = map[string]any{"recovery": recovery}
		clusterSpec["externalClusters"] = []any{map[string]any{
			"name": restoreSourceName,
			"plugin": map[string]any{
				"name": barmanPluginName,
				"parameters": map[string]any{
					"barmanObjectName": name,
					// The SOURCE's stream, which is what makes this a restore.
					"serverName": r.SourceDatabase,
				},
			},
		}}
	}

	cluster := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   ctx.Namespace,
			"labels":      labels,
			"annotations": map[string]any{AnnotationDeletionPolicy: string(spec.DeletionPolicy)},
		},
		"spec": clusterSpec,
	}}
	objs = append(objs, cluster)
	stampEnv(objs, ctx.Env)
	return objs, nil
}

// barmanPluginName is the CNPG-I plugin the Barman Cloud operator registers.
const barmanPluginName = "barman-cloud.cloudnative-pg.io"

// restoreSourceName is the externalClusters entry a recovery bootstrap reads.
// Internal to the rendered object, so it is a constant rather than a knob.
const restoreSourceName = "origin"

// databaseServerName is the prefix a database's WAL and base backups are
// written under, inside the environment's bucket.
//
// A restored database MUST NOT reuse its source's stream: barman-cloud's
// pre-flight refuses to archive into a non-empty prefix ("Expected empty
// archive") and the recovery job crash-loops. The restored database is also a
// different timeline, so sharing a stream would interleave two histories in
// one prefix even if barman allowed it.
//
// override wins when the platform allocated a stream itself, which an
// executor that can restore the same database twice MUST do — see
// DatabaseBackup.ServerName. The derived "-restored" suffix is correct for a
// first restore and collides on a second.
func databaseServerName(name string, restore *v1alpha1.DatabaseRestore, override string) string {
	if override != "" {
		return override
	}
	if restore == nil {
		return name
	}
	return name + "-restored"
}

// renderObjectStore renders the plugin's ObjectStore: one per database, in the
// database's own namespace, under its own prefix.
//
// THE PREFIX IS THE ISOLATION BOUNDARY. It is composed from the environment's
// bucket and the database's name, both of which the platform controls — a
// hosted user cannot name a path, so they cannot name anyone else's.
func renderObjectStore(name string, spec v1alpha1.ManagedDatabaseSpec, ctx Context, b DatabaseBackup) (*unstructured.Unstructured, error) {
	if b.Bucket == "" {
		return nil, fmt.Errorf("manageddatabase %s: backup bucket is empty; an object store with no destination archives nothing", name)
	}
	configuration := map[string]any{
		"destinationPath": strings.TrimSuffix(b.Bucket, "/") + "/" + name,
		"wal":             map[string]any{"compression": "gzip"},
		"data":            map[string]any{"compression": "gzip"},
	}
	if b.EndpointURL != "" {
		configuration["endpointURL"] = b.EndpointURL
	}
	if b.CredentialSecret != "" {
		configuration["s3Credentials"] = map[string]any{
			"accessKeyId":     map[string]any{"name": b.CredentialSecret, "key": "ACCESS_KEY_ID"},
			"secretAccessKey": map[string]any{"name": b.CredentialSecret, "key": "ACCESS_SECRET_KEY"},
		}
	} else {
		// No secret at all: Workload Identity. The pod's own GCP identity
		// authenticates, so there is no key to rotate, leak or forget.
		configuration["googleCredentials"] = map[string]any{"gkeEnvironment": true}
	}

	storeSpec := map[string]any{"configuration": configuration}
	if b.RetentionPolicy != "" {
		storeSpec["retentionPolicy"] = b.RetentionPolicy
	}

	labels := map[string]any{}
	for k, v := range managedLabels(name, ctx.PartOf) {
		labels[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "barmancloud.cnpg.io/v1",
		"kind":       "ObjectStore",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ctx.Namespace,
			"labels":    labels,
		},
		"spec": storeSpec,
	}}, nil
}

// databaseTolerations converts to the unstructured shape, omitting empty
// fields so the rendered object stays diff-stable against the API server's
// defaulting.
func databaseTolerations(in []v1alpha1.Toleration) []any {
	var out []any
	for _, t := range in {
		tol := map[string]any{}
		if t.Key != "" {
			tol["key"] = t.Key
		}
		if t.Operator != "" {
			tol["operator"] = string(t.Operator)
		}
		if t.Value != "" {
			tol["value"] = t.Value
		}
		if t.Effect != "" {
			tol["effect"] = string(t.Effect)
		}
		if t.TolerationSeconds != nil {
			tol["tolerationSeconds"] = *t.TolerationSeconds
		}
		out = append(out, tol)
	}
	return out
}

// toUnstructured converts typed objects and removes ONLY the noise the typed
// encoding carries for fields an applier never writes: nulls, the
// server-owned status, metadata.creationTimestamp (and a metadata block left
// empty by removing it), and the zero Deployment strategy.
//
// This is deliberately NOT a general "drop empty maps" prune. In Kubernetes
// an empty map is frequently the MEANING. `podSelector: {}` in a NetworkPolicy
// peer means "every pod in this namespace", and dropping it turns a private
// workload's policy into allow-from-anywhere. `emptyDir: {}` is the volume
// source. TestRenderKeepsSemanticEmptyMaps pins both.
func toUnstructured(objs []runtime.Object) ([]*unstructured.Unstructured, error) {
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		m, err := toMap(o)
		if err != nil {
			return nil, err
		}
		delete(m, "status")
		if spec, ok := m["spec"].(map[string]any); ok {
			if s, ok := spec["strategy"].(map[string]any); ok && len(s) == 0 {
				delete(spec, "strategy")
			}
		}
		out = append(out, &unstructured.Unstructured{Object: m})
	}
	return out, nil
}

// toMap is the typed → map conversion shared by toUnstructured and the Job
// spec hash, so the hash is taken over exactly what is emitted.
func toMap(o any) (map[string]any, error) {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, fmt.Errorf("convert %T: %w", o, err)
	}
	dropNils(m)
	stripCreationTimestamps(m)
	return m, nil
}

func dropNils(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if val == nil {
				delete(t, k)
				continue
			}
			dropNils(val)
		}
	case []any:
		for _, val := range t {
			dropNils(val)
		}
	}
}

// stripCreationTimestamps removes metadata.creationTimestamp at every depth
// (the object's own, a pod template's, a CronJob's job template's). All are
// server-owned. A template metadata block that held nothing else is removed
// with it: an empty metadata map never means anything.
func stripCreationTimestamps(v any) {
	switch t := v.(type) {
	case map[string]any:
		if md, ok := t["metadata"].(map[string]any); ok {
			delete(md, "creationTimestamp")
			if len(md) == 0 {
				delete(t, "metadata")
			}
		}
		for _, val := range t {
			stripCreationTimestamps(val)
		}
	case []any:
		for _, val := range t {
			stripCreationTimestamps(val)
		}
	}
}

// stampEnv ports kcl/lib/labels.k stamp_env_all: forge.dev/env on every
// object's metadata.labels, and on the pod template of the kinds whose
// spec.template IS one (labels.k:97 _POD_TEMPLATE_KINDS) or a CronJob's
// spec.jobTemplate.spec.template. Never a selector (labels.k:64-74). A key
// already present is left as written (labels.k:112-123).
func stampEnv(objs []*unstructured.Unstructured, env string) {
	if env == "" {
		return
	}
	add := func(meta map[string]any) {
		labels, _ := meta["labels"].(map[string]any)
		if labels == nil {
			labels = map[string]any{}
			meta["labels"] = labels
		}
		if _, ok := labels[LabelEnv]; !ok {
			labels[LabelEnv] = env
		}
	}
	templateMeta := func(parent map[string]any) {
		tmpl, ok := parent["template"].(map[string]any)
		if !ok {
			return
		}
		md, _ := tmpl["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
			tmpl["metadata"] = md
		}
		add(md)
	}
	for _, o := range objs {
		md, _ := o.Object["metadata"].(map[string]any)
		if md == nil {
			md = map[string]any{}
			o.Object["metadata"] = md
		}
		add(md)
		spec, _ := o.Object["spec"].(map[string]any)
		switch o.GetKind() {
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job":
			if spec != nil {
				templateMeta(spec)
			}
		case "CronJob":
			if jt, ok := spec["jobTemplate"].(map[string]any); ok {
				if js, ok := jt["spec"].(map[string]any); ok {
					templateMeta(js)
				}
			}
		}
	}
}

func copyLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func objectMeta(name, namespace string, labels map[string]string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: copyLabels(labels)}
}

package codegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// runTierKCL evaluates main.k in a throwaway project depending on forge's KCL
// module, and returns kcl's JSON output — or, on failure, its error text (the
// negative cases assert on that).
//
// kcltest.Run, not exec+CombinedOutput: under a parallel `go test ./...` kcl
// prints "waiting for package-cache lock..." on STDOUT ahead of the JSON, and
// the round-trip decode then failed with `invalid character 'w'` against a
// document that was correct. See internal/kcltest.
func runTierKCL(t *testing.T, main string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH")
	}
	dir := t.TempDir()
	mod := "[package]\nname = \"tiercheck\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\nforge = { path = \"" + forgeKCLModuleDir(t) + "\" }\n"
	for f, c := range map[string]string{"kcl.mod": mod, "main.k": "import forge.tiers\n\n" + main} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kcltest.Run(t.Context(), dir, "run", "main.k", "--format", "json")
	if err != nil {
		return string(out) + "\n" + err.Error(), err
	}
	return string(out), nil
}

// TestTierKCLRoundTripsIntoGo evaluates declarations written against the
// GENERATED schemas, projects them with the generated _json lambdas, decodes
// the result into the Go spec types, and runs Validate(). This is the "one
// declaration" claim, end to end: what an author writes in KCL IS a valid Go
// spec, and therefore a valid CR spec.
func TestTierKCLRoundTripsIntoGo(t *testing.T) {
	if testing.Short() {
		t.Skip("runs kcl; full mode only")
	}
	out, err := runTierKCL(t, `
_api = tiers.Workload {
    image = "ghcr.io/acme/api:v1.4.2"
    args = ["serve"]
    replicas = 3
    ports = [tiers.Port { name = "http", port = 8080, expose = True }]
    probes = tiers.Probes { readinessPath = "/readyz", livenessPath = "/healthz" }
    env = [
        tiers.EnvVar { name = "LOG_LEVEL", value = "info" }
        tiers.EnvVar { name = "DATABASE_URL", databaseRef = tiers.DatabaseRef { name = "orders" } }
        tiers.EnvVar { name = "STRIPE_KEY", managedSecret = tiers.ManagedSecretRef { name = "STRIPE_KEY" } }
        tiers.EnvVar { name = "SENTRY_DSN", managedSecret = tiers.ManagedSecretRef { name = "SENTRY_DSN", optional = True } }
        tiers.EnvVar { name = "PW", secretRef = tiers.SecretKeyRef { name = "s", key = "k" } }
        tiers.EnvVar { name = "CFG", configMapRef = tiers.ConfigMapKeyRef { name = "c", key = "k" } }
        tiers.EnvVar { name = "POD", fieldRef = tiers.FieldRef { fieldPath = "metadata.name" } }
    ]
    namespacedRBAC = [tiers.PolicyRule { apiGroups = [""], resources = ["configmaps"], verbs = ["get"] }]
    serviceAccountAnnotations = {"iam.gke.io/gcp-service-account" = "api@p.iam.gserviceaccount.com"}
}
_pod = tiers.Workload {
    image = "ghcr.io/acme/api:v1"
    ports = [tiers.Port { name = "http", port = 8080, appProtocol = "h2c", expose = True, domains = ["api.acme.com"] }]
    probes = tiers.Probes {}
    sidecars = [tiers.Container {
        name = "cloud-sql-proxy"
        image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
        args = ["--address=127.0.0.1", "--port=5432", "acme:us:db"]
        resources = tiers.Resources { cpuRequestMillicores = 10, memoryRequestBytes = 16777216 }
    }]
    volumes = [
        tiers.Volume { name = "scratch", mountPath = "/scratch", source = tiers.VolumeSource { emptyDir = tiers.EmptyDirVolumeSource {} } }
        tiers.Volume { name = "key", mountPath = "/etc/key", readOnly = True, source = tiers.VolumeSource { secret = tiers.SecretVolumeSource { name = "k", items = [tiers.KeyToPath { key = "pem", path = "key.pem" }] } } }
        tiers.Volume { name = "cfg", mountPath = "/etc/cfg", source = tiers.VolumeSource { configMap = tiers.ConfigMapVolumeSource { name = "c" } } }
        tiers.Volume { name = "cache", mountPath = "/cache", source = tiers.VolumeSource { pvc = tiers.PVCVolumeSource { claimName = "cache" } } }
    ]
    serviceAccount = "reliant-cloudsql"
    nodeSelector = {"cloud.google.com/gke-nodepool" = "general"}
    tolerations = [tiers.Toleration { key = "pool", operator = "Equal", value = "general", effect = "NoSchedule" }]
    podAnnotations = {"cluster-autoscaler.kubernetes.io/safe-to-evict" = "true"}
}
_job = tiers.Workload { kind = "job", image = "ghcr.io/acme/api:v1", args = ["migrate"], before = ["*"] }
_op = tiers.Workload { kind = "operator", image = "ghcr.io/acme/op:v1", crds = ["Widget"], group = "acme.dev", version = "v1alpha1", clusterRBAC = [tiers.PolicyRule { apiGroups = ["acme.dev"], resources = ["widgets"], verbs = ["*"] }] }
_min = tiers.Workload { image = "ghcr.io/acme/w:v1", ports = [tiers.Port { name = "http", port = 3000 }] }
_req = tiers.Workload { image = "ghcr.io/acme/w:v1", resources = tiers.Resources { cpuRequestMillicores = 500, memoryRequestBytes = 2147483648 } }
_ss = tiers.StaticSite { bucket = "gs://acme", keepReleases = 0, cdn = tiers.StaticSiteCDN { urlMap = "lb" } }
_db = tiers.ManagedDatabase {}

api = tiers.workload_json(_api)
pod = tiers.workload_json(_pod)
job = tiers.workload_json(_job)
op = tiers.workload_json(_op)
min = tiers.workload_json(_min)
req = tiers.workload_json(_req)
ss = tiers.static_site_json(_ss)
db = tiers.managed_database_json(_db)
`)
	if err != nil {
		t.Fatalf("kcl: %v\n%s", err, out)
	}
	var doc struct {
		API v1alpha1.WorkloadSpec        `json:"api"`
		Pod v1alpha1.WorkloadSpec        `json:"pod"`
		Job v1alpha1.WorkloadSpec        `json:"job"`
		Op  v1alpha1.WorkloadSpec        `json:"op"`
		Min v1alpha1.WorkloadSpec        `json:"min"`
		Req v1alpha1.WorkloadSpec        `json:"req"`
		SS  v1alpha1.StaticSiteSpec      `json:"ss"`
		DB  v1alpha1.ManagedDatabaseSpec `json:"db"`
	}
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields() // a key the Go type does not know is a generator bug
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("decode KCL output into the Go specs: %v\n%s", err, out)
	}
	for name, v := range map[string]interface{ Validate() error }{"ss": doc.SS, "db": doc.DB} {
		if err := v.Validate(); err != nil {
			t.Errorf("%s: KCL-authored spec fails Go Validate: %v", name, err)
		}
	}
	for name, w := range map[string]v1alpha1.WorkloadSpec{"api": doc.API, "pod": doc.Pod, "job": doc.Job, "op": doc.Op, "min": doc.Min, "req": doc.Req} {
		if err := w.Validate(v1alpha1.ProfileFull); err != nil {
			t.Errorf("%s: KCL-authored workload fails Go Validate(Full): %v", name, err)
		}
	}
	// The same api is refused under Restricted, naming the Full-only fields.
	if err := doc.API.Validate(v1alpha1.ProfileRestricted); err == nil || !strings.Contains(err.Error(), "namespacedRBAC") || !strings.Contains(err.Error(), "env[PW].secretRef") {
		t.Errorf("api under Restricted = %v, want namespacedRBAC and env[PW].secretRef refused", err)
	}
	// EMPTY schema instances survive the _json projection: an
	// `emptyDir = EmptyDirVolumeSource {}` is the commonest volume, and
	// `probes = Probes {}` means "probe with every default". Dropping either
	// as falsy would silently change what runs.
	if src := doc.Pod.Volumes[0].Source; src.EmptyDir == nil {
		t.Errorf("an empty emptyDir source was dropped by the projection: %+v", src)
	}
	if doc.Pod.Probes == nil {
		t.Error("probes = tiers.Probes {} was dropped by the projection")
	}
	if doc.Pod.Sidecars[0].Resources.CPURequestMillicores != 10 || doc.Pod.Volumes[1].Source.Secret.Items[0].Path != "key.pem" ||
		doc.Pod.Ports[0].Domains[0] != "api.acme.com" || doc.Pod.Ports[0].AppProtocol != "h2c" || doc.Pod.Tolerations[0].Effect != v1alpha1.TaintNoSchedule {
		t.Errorf("pod-level fields did not round-trip: %+v", doc.Pod)
	}
	// Every pod-level field is Full-only; appProtocol and domains are not.
	if err := doc.Pod.Validate(v1alpha1.ProfileRestricted); err == nil {
		t.Error("the pod-level fixture must be refused under Restricted")
	} else {
		for _, f := range []string{"sidecars:", "volumes:", "serviceAccount:", "nodeSelector:", "tolerations:", "podAnnotations:"} {
			if !strings.Contains(err.Error(), f) {
				t.Errorf("Restricted did not refuse %s: %v", f, err)
			}
		}
		if strings.Contains(err.Error(), "appProtocol") || strings.Contains(err.Error(), "domains") {
			t.Errorf("Restricted refused a Restricted-allowed port field: %v", err)
		}
	}
	// The working defaults reached Go: an omitted resources block is the
	// entry shape, not zero; kind defaults to service; replicas to 1; a
	// port's protocol to tcp.
	if r := doc.Min.Resources; r.CPURequestMillicores != 250 || r.MemoryRequestBytes != 1<<30 {
		t.Errorf("omitted resources = %+v, want the 250m/1Gi default shape", r)
	}
	if doc.Min.Kind != v1alpha1.KindService || doc.Min.Replicas != 1 || doc.Min.Ports[0].Protocol != v1alpha1.ProtocolTCP {
		t.Errorf("min = %+v, want kind service, replicas 1, protocol tcp", doc.Min)
	}
	// Probes is a pointer: unset stays ABSENT, so the kind's probe policy
	// decides. An empty default instance would give every job a probe.
	if doc.Min.Probes != nil || doc.Job.Probes != nil {
		t.Errorf("unset probes = %+v / %+v, want absent", doc.Min.Probes, doc.Job.Probes)
	}
	if p := doc.API.Probes; p == nil || p.PeriodSeconds != v1alpha1.DefaultProbePeriodSeconds || p.TimeoutSeconds != v1alpha1.DefaultProbeTimeoutSeconds {
		t.Errorf("declared probes = %+v, want the readiness timing defaults filled", p)
	}
	// Kind-dependent defaults have NO static default: leaderElection and
	// deployPhase stay unset, and the Effective helpers resolve them.
	if doc.Op.LeaderElection != nil || doc.Job.DeployPhase != "" || !doc.Op.EffectiveLeaderElection() {
		t.Errorf("op.leaderElection = %v, job.deployPhase = %q: want both unset, leader election effective", doc.Op.LeaderElection, doc.Job.DeployPhase)
	}
	// Request-only: KCL must leave the limits UNSET (a static default cannot
	// say "equals the request"), so Go's WithDefaults resolves them to the
	// requests at render time.
	if r := doc.Req.Resources; r.CPULimitMillicores != 0 || r.MemoryLimitBytes != 0 {
		t.Errorf("request-only resources = %+v, want the limits left unset for Go to default", r)
	}
	if r := doc.Req.Resources.WithDefaults(); r.CPULimitMillicores != 500 || r.MemoryLimitBytes != 2<<30 {
		t.Errorf("defaulted request-only resources = %+v, want limits equal to the requests", r)
	}
	if doc.DB.DeletionPolicy != v1alpha1.DeletionPolicyRetain || doc.DB.Instances != 1 || doc.DB.StorageGiB != 10 {
		t.Errorf("database defaults = %+v", doc.DB)
	}
	// keepReleases = 0 must survive as an explicit 0 ("retain everything"),
	// not collapse to unset (which means the default of 10).
	if doc.SS.KeepReleases == nil || *doc.SS.KeepReleases != 0 {
		t.Errorf("keepReleases = %v, want an explicit 0", doc.SS.KeepReleases)
	}
	if doc.SS.CDN == nil || doc.SS.CDN.Invalidate != v1alpha1.InvalidateEntrypoints {
		t.Errorf("cdn = %+v, want invalidate defaulted to entrypoints", doc.SS.CDN)
	}
	if doc.API.Env[1].DatabaseRef == nil || doc.API.Env[1].DatabaseRef.EffectiveKey() != v1alpha1.DatabaseKeyURI {
		t.Errorf("databaseRef = %+v", doc.API.Env[1].DatabaseRef)
	}
	// Unset optionals are ABSENT, not null. A null would clear a CR field.
	if strings.Contains(out, "null") {
		t.Errorf("projection emitted a null:\n%s", out)
	}
}

// TestTierKCLRejects: each constraint a declaration can break at author time
// fails `kcl run` with a message naming the field, and the schemas are closed.
func TestTierKCLRejects(t *testing.T) {
	if testing.Short() {
		t.Skip("runs kcl; full mode only")
	}
	cases := map[string]struct{ decl, want string }{
		"security context: never root":    {`x = tiers.PodSecurity { runAsUser = 0 }`, "PodSecurity.runAsUser must be at least 1"},
		"closed schema: privileged":       {`x = tiers.PodSecurity { privileged = True }`, "privileged"},
		"closed schema: cluster":          {`x = tiers.Workload { image = "ghcr.io/a/b:v1", cluster = "c" }`, "cluster"},
		"closed schema: config_map_ref":   {`x = tiers.EnvVar { name = "A", config_map_ref = "c" }`, "config_map_ref"},
		"closed schema: org":              {`x = tiers.ManagedDatabase { orgId = "o" }`, "orgId"},
		"kind enum":                       {`x = tiers.Workload { image = "ghcr.io/a/b:v1", kind = "daemon" }`, "Workload.kind must be one of"},
		"replicas floor":                  {`x = tiers.Workload { image = "ghcr.io/a/b:v1", replicas = 0 }`, "Workload.replicas must be at least 1"},
		"port range":                      {`x = tiers.Port { name = "http", port = 70000 }`, "Port.port must be at most 65535"},
		"port name":                       {`x = tiers.Port { name = "HTTP_PORT", port = 80 }`, "Port.name must match"},
		"deploy phase enum":               {`x = tiers.Workload { image = "ghcr.io/a/b:v1", deployPhase = "during" }`, "Workload.deployPhase must be one of"},
		"env name pattern":                {`x = tiers.EnvVar { name = "1BAD" }`, "EnvVar.name must match"},
		"managed secret no path":          {`x = tiers.ManagedSecretRef { name = "other/secret" }`, "ManagedSecretRef.name must match"},
		"managed secret is a ref":         {`x = tiers.EnvVar { name = "A", managedSecret = "BARE" }`, "expected ManagedSecretRef"},
		"db key enum":                     {`x = tiers.DatabaseRef { name = "db", key = "dsn" }`, "DatabaseRef.key must be one of"},
		"digest not tag":                  {`x = tiers.StaticSite { liveDigest = "latest" }`, "StaticSite.liveDigest must match"},
		"invalidate enum":                 {`x = tiers.StaticSiteCDN { urlMap = "m", invalidate = "some" }`, "StaticSiteCDN.invalidate must be one of"},
		"instances ceiling":               {`x = tiers.ManagedDatabase { instances = 50 }`, "ManagedDatabase.instances must be at most 3"},
		"deletion policy enum":            {`x = tiers.ManagedDatabase { deletionPolicy = "purge" }`, "ManagedDatabase.deletionPolicy must be one of"},
		"probe path must be absolute":     {`x = tiers.Probes { readinessPath = "readyz" }`, "Probes.readinessPath must match"},
		"volume source exactly one":       {`x = tiers.VolumeSource { emptyDir = tiers.EmptyDirVolumeSource {}, pvc = tiers.PVCVolumeSource { claimName = "c" } }`, "exactly one of the fields"},
		"volume source none":              {`x = tiers.VolumeSource {}`, "exactly one of the fields"},
		"closed volume: hostPath":         {`x = tiers.VolumeSource { hostPath = { path = "/" } }`, "hostPath"},
		"closed sidecar: securityContext": {`x = tiers.Container { name = "s", image = "i", securityContext = {} }`, "securityContext"},
		"toleration effect enum":          {`x = tiers.Toleration { key = "k", effect = "Evict" }`, "Toleration.effect must be one of"},
		"domains ceiling":                 {`x = tiers.Port { name = "http", port = 80, domains = ["a.x.io", "b.x.io", "c.x.io", "d.x.io", "e.x.io", "f.x.io", "g.x.io", "h.x.io", "i.x.io"] }`, "Port.domains allows at most 8 entries"},
		"policy rule needs verbs":         {`x = tiers.PolicyRule { apiGroups = [""], resources = ["pods"] }`, "verbs"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := runTierKCL(t, c.decl)
			if err == nil {
				t.Fatalf("kcl accepted an invalid declaration:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("error does not mention %q:\n%s", c.want, out)
			}
		})
	}
}

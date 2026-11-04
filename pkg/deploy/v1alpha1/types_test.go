package v1alpha1

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func ptr[T any](v T) *T { return &v }

const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// fullWorkload sets EVERY field, so a round-trip that drops one is visible.
// It is a Full-profile spec: every field is set, including the Full-only ones.
func fullWorkload() *Workload {
	return &Workload{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "Workload"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "acme-prod", Labels: map[string]string{LabelOrgID: "org_1"}},
		Spec: WorkloadSpec{
			Kind:     KindOperator,
			Image:    "ghcr.io/acme/api:v1.4.2",
			Command:  []string{"/app/api"},
			Args:     []string{"operator"},
			Replicas: 2,
			Env: []EnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "DB_PASSWORD", SecretRef: &SecretKeyRef{Name: "db", Key: "password"}},
				{Name: "STRIPE_KEY", ManagedSecret: &ManagedSecretRef{Name: "STRIPE_KEY", Optional: true}},
				{Name: "DATABASE_URL", DatabaseRef: &DatabaseRef{Name: "orders", Key: DatabaseKeyURI}},
				{Name: "CORS_ORIGINS", WorkloadURL: &WorkloadURLRef{Name: "web"}},
				{Name: "FEATURES", ConfigMapRef: &ConfigMapKeyRef{Name: "flags", Key: "features"}},
				{Name: "POD_NAME", FieldRef: &FieldRef{FieldPath: "metadata.name"}},
			},
			Resources:                 Resources{CPURequestMillicores: 500, CPULimitMillicores: 1000, MemoryRequestBytes: 2 << 30, MemoryLimitBytes: 2 << 30},
			Ports:                     []Port{{Name: "metrics", Port: 8080, Protocol: ProtocolTCP, AppProtocol: "http"}, {Name: "webhook", Port: 9443}},
			Probes:                    &Probes{Port: 8081, ReadinessPath: "/readyz", LivenessPath: "/healthz", InitialDelaySeconds: 1, PeriodSeconds: 10, TimeoutSeconds: 3, FailureThreshold: 4},
			StorageGiB:                0,
			Schedule:                  "",
			Before:                    nil,
			DeployPhase:               "",
			ClusterRBAC:               []PolicyRule{{APIGroups: []string{"acme.dev"}, Resources: []string{"widgets", "widgets/status"}, Verbs: []string{"*"}, ResourceNames: []string{"w1"}}},
			CRDs:                      []string{"Widget"},
			Group:                     "acme.dev",
			Version:                   "v1alpha1",
			LeaderElection:            ptr(false),
			ServiceAccountAnnotations: map[string]string{"iam.gke.io/gcp-service-account": "api@acme.iam.gserviceaccount.com"},
			Sidecars: []Container{{
				Name: "cloud-sql-proxy", Image: "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1",
				Command: []string{"/cloud-sql-proxy"}, Args: []string{"--port=5432", "acme:us:db"},
				Env:       []EnvVar{{Name: "LOG", Value: "debug"}},
				Ports:     []Port{{Name: "proxy-health", Port: 9801}},
				Resources: Resources{CPURequestMillicores: 10, MemoryRequestBytes: 16 << 20},
				Probes:    &Probes{TCP: true},
			}},
			Volumes: []Volume{
				{Name: "scratch", MountPath: "/scratch", Source: VolumeSource{EmptyDir: &EmptyDirVolumeSource{SizeLimitBytes: 1 << 30}}},
				{Name: "key", MountPath: "/etc/key", ReadOnly: true, Source: VolumeSource{Secret: &SecretVolumeSource{Name: "k", Items: []KeyToPath{{Key: "pem", Path: "key.pem"}}}}},
				{Name: "cfg", MountPath: "/etc/cfg", ReadOnly: true, Source: VolumeSource{ConfigMap: &ConfigMapVolumeSource{Name: "c", Items: []KeyToPath{{Key: "a", Path: "a.yaml"}}}}},
				{Name: "cache", MountPath: "/cache", Source: VolumeSource{PVC: &PVCVolumeSource{ClaimName: "cache"}}},
			},
			NodeSelector:   map[string]string{"cloud.google.com/gke-nodepool": "ops"},
			Tolerations:    []Toleration{{Key: "dedicated", Operator: TolerationOpEqual, Value: "ops", Effect: TaintNoExecute, TolerationSeconds: ptr(int64(30))}},
			PodAnnotations: map[string]string{"cluster-autoscaler.kubernetes.io/safe-to-evict": "true"},
		},
		Status: WorkloadStatus{
			TierStatus: TierStatus{Phase: PhaseReady, ObservedGeneration: 3, Hostname: "lively-ferret.apps.example", URL: "https://lively-ferret.apps.example", Message: "ok",
				Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Unix(1700000000, 0)}}},
			ServiceName: "api", WorkloadName: "api", ReadyReplicas: 1, UpdatedReplicas: 1, DesiredReplicas: 1,
			ObservedImage: "ghcr.io/acme/api:v1.4.2", LastReadyAt: ptr(metav1.Unix(1700000000, 0)),
		},
	}
}

func fullSite() *StaticSite {
	return &StaticSite{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "StaticSite"},
		ObjectMeta: metav1.ObjectMeta{Name: "web"},
		Spec: StaticSiteSpec{
			Bucket: "gs://acme-web", BasePath: "/admin", LiveDigest: digestA, PreviousDigest: digestB,
			RetainedDigests: []string{digestA}, KeepReleases: ptr(int32(0)), Entrypoints: []string{"/index.html"},
			CDN:     &StaticSiteCDN{URLMap: "acme-lb", Invalidate: InvalidateAll, ExtraInvalidatePaths: []string{"/sw.js"}},
			Domains: []string{"www.acme.com"},
			RuntimeConfig: map[string]RuntimeConfigValue{
				"API_URL": {WorkloadURL: &WorkloadURLRef{Name: "api"}},
				"FLAG":    {Value: ptr("")},
			},
		},
		Status: StaticSiteStatus{TierStatus: TierStatus{Phase: PhaseProgressing}, BucketPrefix: "sites/web", LiveDigest: digestB, PreviousDigest: digestA, ReleaseCount: 4, LastSyncedAt: ptr(metav1.Unix(1700000000, 0))},
	}
}

func fullDatabase() *ManagedDatabase {
	return &ManagedDatabase{
		TypeMeta:   metav1.TypeMeta{APIVersion: GroupVersion.String(), Kind: "ManagedDatabase"},
		ObjectMeta: metav1.ObjectMeta{Name: "orders"},
		Spec:       ManagedDatabaseSpec{Engine: EnginePostgres, Instances: 2, StorageGiB: 50, DeletionPolicy: DeletionPolicyDelete},
		Status: ManagedDatabaseStatus{Phase: PhaseLocked, ObservedGeneration: 2, ClusterName: "orders", DatabaseName: "orders", RoleName: "orders_app",
			SecretName: "orders-app", EngineVersion: "16.4", ReadyAt: ptr(metav1.Unix(1700000000, 0)), Message: "released"},
	}
}

// TestTierTypesRoundTripJSON: every kind survives encode→decode unchanged, and
// the encoding re-encodes byte-identically (no field lost to a tag mismatch).
func TestTierTypesRoundTripJSON(t *testing.T) {
	for name, obj := range map[string]runtime.Object{"Workload": fullWorkload(), "StaticSite": fullSite(), "ManagedDatabase": fullDatabase()} {
		t.Run(name, func(t *testing.T) {
			b, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			fresh := reflect.New(reflect.TypeOf(obj).Elem()).Interface()
			if err := json.Unmarshal(b, fresh); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(obj, fresh) {
				t.Fatalf("round-trip changed the object:\nbefore %+v\nafter  %+v", obj, fresh)
			}
			b2, _ := json.Marshal(fresh)
			if string(b) != string(b2) {
				t.Fatalf("re-encoding differs:\n%s\n%s", b, b2)
			}
		})
	}
}

// TestSpecCarriesNoHostedIdentity pins the design rule that ownership is
// labels, never spec: an author-writable org id is the defect it prevents.
// (NamespacedRBAC / ClusterRBAC are RBAC tiers, not target coordinates.)
func TestSpecCarriesNoHostedIdentity(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)^(org|tena|environment|deployment|namespace($|name)|cluster($|name|id))`)
	for _, typ := range []reflect.Type{reflect.TypeOf(WorkloadSpec{}), reflect.TypeOf(StaticSiteSpec{}), reflect.TypeOf(ManagedDatabaseSpec{})} {
		for i := 0; i < typ.NumField(); i++ {
			if f := typ.Field(i); forbidden.MatchString(f.Name) {
				t.Errorf("%s.%s: hosted identity and target coordinates are not spec", typ.Name(), f.Name)
			}
		}
	}
}

// TestSchemeRegistersAllKinds guards the registration trap: an unregistered
// kind decodes as "no kind registered" only at runtime.
func TestSchemeRegistersAllKinds(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"Workload", "WorkloadList", "StaticSite", "StaticSiteList", "ManagedDatabase", "ManagedDatabaseList"} {
		if !s.Recognizes(GroupVersion.WithKind(k)) {
			t.Errorf("%s not registered under %s", k, GroupVersion)
		}
	}
	if GroupVersion.Group != "forge.dev" {
		t.Errorf("group = %q, want forge.dev", GroupVersion.Group)
	}
}

// TestDeepCopyIsIndependent: mutating a copy's nested pointer/slice must not
// reach the original.
func TestDeepCopyIsIndependent(t *testing.T) {
	a := fullWorkload()
	b := a.DeepCopy()
	b.Spec.Env[3].DatabaseRef.Name = "changed"
	b.Spec.Env[5].ConfigMapRef.Key = "changed"
	b.Spec.Ports[0].Port = 1
	b.Spec.Probes.Port = 1
	b.Spec.ClusterRBAC[0].Verbs[0] = "get"
	*b.Spec.LeaderElection = true
	b.Spec.ServiceAccountAnnotations["x"] = "y"
	b.Spec.Sidecars[0].Args[0] = "changed"
	b.Spec.Sidecars[0].Probes.TCP = false
	b.Spec.Volumes[1].Source.Secret.Items[0].Path = "changed"
	*b.Spec.Tolerations[0].TolerationSeconds = 1
	b.Spec.NodeSelector["x"] = "y"
	b.Spec.PodAnnotations["x"] = "y"
	if a.Spec.Env[3].DatabaseRef.Name != "orders" || a.Spec.Env[5].ConfigMapRef.Key != "features" || a.Spec.Ports[0].Port != 8080 ||
		a.Spec.Probes.Port != 8081 || a.Spec.ClusterRBAC[0].Verbs[0] != "*" || *a.Spec.LeaderElection || len(a.Spec.ServiceAccountAnnotations) != 1 ||
		a.Spec.Sidecars[0].Args[0] != "--port=5432" || !a.Spec.Sidecars[0].Probes.TCP || a.Spec.Volumes[1].Source.Secret.Items[0].Path != "key.pem" ||
		*a.Spec.Tolerations[0].TolerationSeconds != 30 || len(a.Spec.NodeSelector) != 1 || len(a.Spec.PodAnnotations) != 1 {
		t.Fatal("DeepCopy shares memory with the original")
	}
}

func mustFail(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want an error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

func TestStaticSiteValidateAndRetention(t *testing.T) {
	if err := fullSite().Spec.Validate(); err != nil {
		t.Fatal(err)
	}
	mustFail(t, StaticSiteSpec{LiveDigest: "latest"}.Validate(), "never a tag")
	mustFail(t, StaticSiteSpec{KeepReleases: ptr(int32(1))}.Validate(), "predecessor release")
	mustFail(t, StaticSiteSpec{CDN: &StaticSiteCDN{}}.Validate(), "urlMap")
	mustFail(t, StaticSiteSpec{CDN: &StaticSiteCDN{URLMap: "m", Invalidate: "some"}}.Validate(), "cdn.invalidate")
	mustFail(t, StaticSiteSpec{BasePath: "admin"}.Validate(), "basePath")

	// assetDir: a clean directory relative to the mount, never normalized.
	for _, ok := range []string{"assets", "_next/static", "static/js"} {
		if err := (StaticSiteSpec{AssetDir: ok}).Validate(); err != nil {
			t.Errorf("assetDir %q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"/assets", "assets/", "a//b", "./assets", "../up", "a/../b", `a\b`} {
		mustFail(t, StaticSiteSpec{AssetDir: bad}.Validate(), "assetDir")
	}

	// Runtime config: exactly one channel per entry, JS-identifier keys,
	// DNS-label reference names. An explicit empty value is legal.
	empty, lit := "", "x"
	ok := StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{
		"API_URL": {WorkloadURL: &WorkloadURLRef{Name: "api"}},
		"FLAG":    {Value: &empty},
	}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid runtime config refused: %v", err)
	}
	mustFail(t, StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{
		"X": {Value: &lit, WorkloadURL: &WorkloadURLRef{Name: "api"}},
	}}.Validate(), "both value and workloadURL")
	mustFail(t, StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{"X": {}}}.Validate(), "neither value nor workloadURL")
	mustFail(t, StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{"api-url": {Value: &lit}}}.Validate(), "JavaScript identifier")
	mustFail(t, StaticSiteSpec{RuntimeConfig: map[string]RuntimeConfigValue{
		"X": {WorkloadURL: &WorkloadURLRef{Name: "API"}},
	}}.Validate(), "workloadURL.name")
	big := map[string]RuntimeConfigValue{}
	for i := 0; i <= MaxRuntimeConfigEntries; i++ {
		big[fmt.Sprintf("K%d", i)] = RuntimeConfigValue{Value: &lit}
	}
	mustFail(t, StaticSiteSpec{RuntimeConfig: big}.Validate(), "at most 128")

	for _, c := range []struct {
		keep *int32
		want int32
	}{{nil, DefaultKeepReleases}, {ptr(int32(0)), 0}, {ptr(int32(1)), MinKeepReleases}, {ptr(int32(5)), 5}} {
		if got := (StaticSiteSpec{KeepReleases: c.keep}).EffectiveKeepReleases(); got != c.want {
			t.Errorf("EffectiveKeepReleases(%v) = %d, want %d", c.keep, got, c.want)
		}
	}
	if (StaticSiteCDN{}).EffectiveInvalidate() != InvalidateEntrypoints {
		t.Error("an unset invalidate policy must mean entrypoints, never all")
	}
}

func TestManagedDatabaseValidateAndDefaults(t *testing.T) {
	d := ManagedDatabaseSpec{}.WithDefaults()
	if d.Engine != EnginePostgres || d.Instances != 1 || d.StorageGiB != 10 || d.DeletionPolicy != DeletionPolicyRetain {
		t.Fatalf("defaults = %+v; retain MUST be the deletion default", d)
	}
	if err := (ManagedDatabaseSpec{}).Validate(); err != nil {
		t.Fatalf("an empty spec must validate via defaults: %v", err)
	}
	// Refused, not clamped: a 50-instance declaration must not deploy as 3.
	mustFail(t, ManagedDatabaseSpec{Instances: 50}.Validate(), "instances 50")
	mustFail(t, ManagedDatabaseSpec{StorageGiB: 501}.Validate(), "storageGiB")
	mustFail(t, ManagedDatabaseSpec{Engine: "mysql"}.Validate(), "engine")
	mustFail(t, ManagedDatabaseSpec{DeletionPolicy: "purge"}.Validate(), "deletionPolicy")
	if err := ValidateDatabaseName("orders-db"); err != nil {
		t.Fatal(err)
	}
	mustFail(t, ValidateDatabaseName(strings.Repeat("a", 41)), "at most 40")
	mustFail(t, ValidateDatabaseName("Orders"), "RFC-1123")
}

// TestResourcesLimitDefaultsToRequest: "set a request, leave the limit" is the
// most common resources block anyone writes, and it must deploy. An unset
// limit is the (defaulted) request, never the static entry shape: a static
// 250m limit under a 500m request is a spec forge itself then refuses.
func TestResourcesLimitDefaultsToRequest(t *testing.T) {
	cases := map[string]struct{ in, want Resources }{
		"empty is the entry shape": {Resources{}, Resources{
			CPURequestMillicores: DefaultCPUMillicores, CPULimitMillicores: DefaultCPUMillicores,
			MemoryRequestBytes: DefaultMemoryBytes, MemoryLimitBytes: DefaultMemoryBytes,
		}},
		"cpu request only": {Resources{CPURequestMillicores: 500}, Resources{
			CPURequestMillicores: 500, CPULimitMillicores: 500,
			MemoryRequestBytes: DefaultMemoryBytes, MemoryLimitBytes: DefaultMemoryBytes,
		}},
		"memory request only": {Resources{MemoryRequestBytes: 2 << 30}, Resources{
			CPURequestMillicores: DefaultCPUMillicores, CPULimitMillicores: DefaultCPUMillicores,
			MemoryRequestBytes: 2 << 30, MemoryLimitBytes: 2 << 30,
		}},
		"explicit limit kept": {Resources{CPURequestMillicores: 500, CPULimitMillicores: 2000}, Resources{
			CPURequestMillicores: 500, CPULimitMillicores: 2000,
			MemoryRequestBytes: DefaultMemoryBytes, MemoryLimitBytes: DefaultMemoryBytes,
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.in.WithDefaults(); got != c.want {
				t.Errorf("WithDefaults(%+v) = %+v, want %+v", c.in, got, c.want)
			}
			if err := c.in.Validate(); err != nil {
				t.Errorf("Validate(%+v): %v", c.in, err)
			}
		})
	}
	// A limit that is SET below its request is still the authoring mistake
	// Kubernetes rejects at admission.
	mustFail(t, Resources{CPURequestMillicores: 500, CPULimitMillicores: 250}.Validate(), "cpuLimitMillicores (250) must be at least the request (500)")
	mustFail(t, Resources{MemoryRequestBytes: 2 << 30, MemoryLimitBytes: 1 << 30}.Validate(), "memoryLimitBytes")
}

// TestDefaultMarkersMatchGoDefaults: the +kubebuilder:default markers (what the
// API server and the generated KCL apply) must equal the Go defaults (what
// WithDefaults and Render apply), or the two destinations run different shapes.
//
// The resource LIMITS must carry NO marker. Their Go default is "equal to
// the (defaulted) request", which is not a static value. A static marker
// would make the API server stamp 250m under a 500m request, and the
// operator's Validate would then refuse the CR. The generated KCL would
// inherit the same wrong default. So for these fields the only correct
// marker is none.
func TestDefaultMarkersMatchGoDefaults(t *testing.T) {
	// DeployPhase and LeaderElection default by KIND (a standalone job's
	// phase, an operator's election), so a static marker would stamp them
	// on every kind and Validate would refuse the CR. Probes' absence is
	// the declaration.
	mustHaveNoMarker := []string{"CPULimitMillicores", "MemoryLimitBytes", "DeployPhase", "LeaderElection", "Probes"}
	want := map[string]string{
		"CPURequestMillicores": strconv.FormatInt(DefaultCPUMillicores, 10),
		"MemoryRequestBytes":   strconv.FormatInt(DefaultMemoryBytes, 10),
		"Kind":                 string(DefaultWorkloadKind),
		"Replicas":             strconv.Itoa(int(DefaultReplicas)),
		"Protocol":             string(DefaultPortProtocol),
		"InitialDelaySeconds":  strconv.Itoa(int(DefaultProbeInitialDelaySeconds)),
		"PeriodSeconds":        strconv.Itoa(int(DefaultProbePeriodSeconds)),
		"TimeoutSeconds":       strconv.Itoa(int(DefaultProbeTimeoutSeconds)),
		"FailureThreshold":     strconv.Itoa(int(DefaultProbeFailureThreshold)),
		"Instances":            strconv.Itoa(int(DefaultDatabaseInstances)),
		"StorageGiB":           strconv.Itoa(int(DefaultDatabaseStorageGiB)),
		"DeletionPolicy":       string(DeletionPolicyRetain),
		"Engine":               string(EnginePostgres),
		"Invalidate":           string(InvalidateEntrypoints),
		"RowCap":               strconv.Itoa(int(DefaultDashboardRowCap)),
		"TimeoutMs":            strconv.Itoa(int(DefaultDashboardTimeoutMs)),
		"MaxConcurrency":       strconv.Itoa(int(DefaultDashboardMaxConcurrency)),
	}
	// A default marker applies to the next non-comment line, which is the field.
	marker := regexp.MustCompile(`^\s*// \+kubebuilder:default=(\S+)$`)
	fieldLine := regexp.MustCompile(`^\s*(\w+)\s`)
	found := map[string]string{}
	for _, f := range []string{"common.go", "workload_types.go", "staticsite_types.go", "manageddatabase_types.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pending := ""
		for _, line := range strings.Split(string(b), "\n") {
			if m := marker.FindStringSubmatch(line); m != nil {
				pending = m[1]
				continue
			}
			if pending != "" && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				if m := fieldLine.FindStringSubmatch(line); m != nil {
					found[m[1]] = pending
				}
				pending = ""
			}
		}
	}
	for _, field := range mustHaveNoMarker {
		if d, ok := found[field]; ok {
			t.Errorf("%s has +kubebuilder:default=%s; it must have none — its default is its request, which no static marker can express", field, d)
		}
	}
	for field, w := range want {
		if found[field] != w {
			t.Errorf("%s: marker default %q, Go default %q", field, found[field], w)
		}
	}
	for field := range found {
		if _, ok := want[field]; !ok {
			t.Errorf("%s has a default marker with no Go-default pin here — add it", field)
		}
	}
}

// TestDatabaseDashboardsValidateAndDefaults pins the limits the control plane
// and KCL both repeat: refused out of range, never clamped.
func TestDatabaseDashboardsValidateAndDefaults(t *testing.T) {
	if got := (DatabaseDashboards{Enabled: true}).WithDefaults(); got != (DatabaseDashboards{Enabled: true, RowCap: 1000, TimeoutMs: 5000, MaxConcurrency: 2}) {
		t.Errorf("defaults = %+v", got)
	}
	for name, tc := range map[string]struct {
		d    DatabaseDashboards
		want string
	}{
		"ok":           {DatabaseDashboards{Enabled: true}, ""},
		"ceilings":     {DatabaseDashboards{Enabled: true, RowCap: 10000, TimeoutMs: 10000, MaxConcurrency: 4}, ""},
		"row cap":      {DatabaseDashboards{RowCap: 10001}, "rowCap"},
		"timeout high": {DatabaseDashboards{TimeoutMs: 10001}, "at most 10s"},
		"timeout low":  {DatabaseDashboards{TimeoutMs: 99}, "timeoutMs"},
		"concurrency":  {DatabaseDashboards{MaxConcurrency: 5}, "maxConcurrency"},
		"negative":     {DatabaseDashboards{MaxConcurrency: -1}, "maxConcurrency"},
	} {
		err := ManagedDatabaseSpec{Dashboards: &tc.d}.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	// WithDefaults on the spec fills a declared dashboards block and leaves an
	// undeclared one nil.
	if s := (ManagedDatabaseSpec{}).WithDefaults(); s.Dashboards != nil {
		t.Errorf("an undeclared block was invented: %+v", s.Dashboards)
	}
	if s := (ManagedDatabaseSpec{Dashboards: &DatabaseDashboards{}}).WithDefaults(); s.Dashboards == nil || s.Dashboards.RowCap != 1000 {
		t.Errorf("a declared block was not defaulted: %+v", s.Dashboards)
	}
}

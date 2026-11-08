package deploy

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// updateGolden rewrites testdata/*.golden.yaml from the current render. A
// flag, not an env var: forge/pkg must not read the ambient environment
// (internal/pkgguard), tests included.
var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden.yaml from the current render")

// The golden suite pins RenderWorkloads' COMPLETE output for representative
// environments, so any change to a rendered object is a reviewed diff in
// testdata/*.golden.yaml rather than a silent drift.
//
// Regenerate with:
//
//	go test ./pkg/deploy -run TestGolden -update
//
// The same sets, rendered through the retired KCL renderer
// (kcl/workloads/expand.k @ 9c739754), are the parity baseline for the
// intentional differences recorded in the PR (probe timeout 3s, /tmp mount,
// no token automount, no Namespace object, per-workload NetworkPolicy, …).

// goldenImage is what the lowering resolves a forge-built workload's image to.
const goldenImage = "ghcr.io/acme/demo:v1.2.3"

var goldenCtx = Context{Namespace: "demo-prod", PartOf: "demo", Env: "prod"}

type goldenCase struct {
	name    string
	profile v1alpha1.Profile
	network *EnvNetworkPolicy
	pull    []string
	ws      []v1alpha1.Workload
}

func gw(name string, spec v1alpha1.WorkloadSpec) v1alpha1.Workload {
	if spec.Image == "" {
		spec.Image = goldenImage
	}
	return wl(name, spec)
}

// scaffoldAPI is the scaffold's default service as the lowering produces it:
// forge built it, so it carries HTTP /readyz + /healthz probes explicitly
// (ADR 0002 §5).
func scaffoldAPI() v1alpha1.Workload {
	return gw("api", v1alpha1.WorkloadSpec{
		Kind:   v1alpha1.KindService,
		Ports:  []v1alpha1.Port{{Name: "http", Port: 8080, Expose: true}},
		Probes: &v1alpha1.Probes{},
	})
}

func scaffoldMigrate() v1alpha1.Workload {
	return gw("migrate", v1alpha1.WorkloadSpec{
		Kind: v1alpha1.KindJob, Command: []string{"/app/demo", "db", "migrate", "up"}, Before: []string{v1alpha1.BeforeAll},
	})
}

func goldenCases() []goldenCase {
	cronSpec := v1alpha1.WorkloadSpec{Kind: v1alpha1.KindCron, Command: []string{"/app/demo", "sweep"}, Schedule: "@daily"}
	return []goldenCase{
		{name: "scaffold", ws: []v1alpha1.Workload{
			scaffoldAPI(),
			scaffoldMigrate(),
			gw("idp-provision", v1alpha1.WorkloadSpec{
				Kind: v1alpha1.KindJob, Command: []string{"/app/demo", "auth", "idp-provision"},
				NamespacedRBAC: []v1alpha1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get", "create", "update", "patch"}}},
			}),
		}},
		{name: "worker", ws: []v1alpha1.Workload{gw("reconciler", v1alpha1.WorkloadSpec{
			Kind: v1alpha1.KindWorker, Command: []string{"/app/demo", "reconcile"},
			NamespacedRBAC: []v1alpha1.PolicyRule{{APIGroups: []string{"example.com"}, Resources: []string{"widgets"}, Verbs: []string{"list", "watch"}}},
		})}},
		{name: "cron", ws: []v1alpha1.Workload{gw("nightly", cronSpec)}},
		{name: "operator", ws: []v1alpha1.Workload{gw("manager", v1alpha1.WorkloadSpec{
			Kind: v1alpha1.KindOperator, Group: "example.com", Version: "v1", CRDs: []string{"Widget"},
			Ports:                     []v1alpha1.Port{{Name: "metrics", Port: 8080}},
			ClusterRBAC:               []v1alpha1.PolicyRule{{APIGroups: []string{"example.com"}, Resources: []string{"widgets"}, Verbs: []string{"get", "list", "watch", "update"}}},
			ServiceAccountAnnotations: map[string]string{"iam.gke.io/gcp-service-account": "manager@p.iam.gserviceaccount.com"},
		})}},
		{name: "storage", ws: []v1alpha1.Workload{gw("store", v1alpha1.WorkloadSpec{
			Kind: v1alpha1.KindService, Ports: []v1alpha1.Port{{Name: "http", Port: 8080}}, StorageGiB: 20,
			Resources: v1alpha1.Resources{CPURequestMillicores: 500, MemoryRequestBytes: 2 << 30},
		})}},
		{name: "replicas3", ws: []v1alpha1.Workload{func() v1alpha1.Workload {
			w := scaffoldAPI()
			w.Spec.Replicas = 3
			w.Spec.Env = []v1alpha1.EnvVar{{Name: "PRE_STOP_DELAY", Value: "10s"}, {Name: "SHUTDOWN_TIMEOUT", Value: "1m"}}
			return w
		}()}},
		{name: "restricted", profile: v1alpha1.ProfileRestricted, ws: restrictedSet()},
		{name: "before_broadcast", ws: []v1alpha1.Workload{
			scaffoldMigrate(),
			gw("seed", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Command: []string{"/app/demo", "seed"}, Before: []string{"api"}}),
			scaffoldAPI(),
			gw("reconciler", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Command: []string{"/app/demo", "reconcile"}}),
			gw("nightly", cronSpec),
			gw("report", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindJob, Command: []string{"/app/demo", "report"}, DeployPhase: v1alpha1.DeployPhasePostRollout}),
		}},
		// Pull secrets ride each generated SA; the override pod carries them
		// itself.
		{name: "pull_secrets", pull: []string{"ghcr-creds"}, ws: []v1alpha1.Workload{
			scaffoldAPI(),
			func() v1alpha1.Workload {
				w := gw("proxy", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Command: []string{"/app/demo", "proxy"}})
				w.Spec.ServiceAccount = "reliant-cloudsql"
				return w
			}(),
		}},
		{name: "netpol", network: &EnvNetworkPolicy{EgressPorts: []int32{443, 5432}, TelemetryNamespace: "observability", IngressNamespace: "gateway-system"},
			ws: []v1alpha1.Workload{scaffoldAPI()}},
		// enableServiceLinks is the opt-in: this pod renders true, its
		// neighbour keeps the default false.
		{name: "service_links", ws: []v1alpha1.Workload{
			scaffoldAPI(),
			func() v1alpha1.Workload {
				w := gw("legacy", v1alpha1.WorkloadSpec{Kind: v1alpha1.KindWorker, Command: []string{"/app/demo", "legacy"}})
				w.Spec.EnableServiceLinks = true
				return w
			}(),
		}},
		{name: "mixed_env", ws: []v1alpha1.Workload{func() v1alpha1.Workload {
			w := scaffoldAPI()
			w.Spec.Env = []v1alpha1.EnvVar{
				{Name: "LOG_LEVEL", Value: "info"},
				{Name: "DB_PASSWORD", SecretRef: &v1alpha1.SecretKeyRef{Name: "demo-db", Key: "password"}},
				{Name: "FEATURES", ConfigMapRef: &v1alpha1.ConfigMapKeyRef{Name: "features", Key: "map"}},
				{Name: "POD_IP", FieldRef: &v1alpha1.FieldRef{FieldPath: "status.podIP"}},
				{Name: "STRIPE_KEY", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "STRIPE_KEY"}},
				{Name: "SENTRY_DSN", ManagedSecret: &v1alpha1.ManagedSecretRef{Name: "SENTRY_DSN", Optional: true}},
				{Name: "DATABASE_URL", DatabaseRef: &v1alpha1.DatabaseRef{Name: "orders"}},
			}
			return w
		}()}},
	}
}

func TestGolden(t *testing.T) {
	update := *updateGolden
	for _, c := range goldenCases() {
		t.Run(c.name, func(t *testing.T) {
			rctx := goldenCtx
			rctx.Network = c.network
			rctx.ImagePullSecrets = c.pull
			objs, err := RenderWorkloads(c.ws, c.profile, rctx)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			for _, o := range objs {
				b, err := yaml.Marshal(o.Object)
				if err != nil {
					t.Fatal(err)
				}
				buf.WriteString("---\n")
				buf.Write(b)
			}
			path := filepath.Join("testdata", c.name+".golden.yaml")
			if update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (regenerate with -update)", err)
			}
			if !bytes.Equal(want, buf.Bytes()) {
				t.Errorf("%s differs from the render (-update to accept):\n--- got\n%s", path, buf.String())
			}
		})
	}
}

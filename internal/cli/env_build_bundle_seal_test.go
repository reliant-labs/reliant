package cli

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// bundleSealExclusions are the release.Shape fields the bundle deliberately
// does NOT take from the declaration: bundle.Build projects them itself from
// the manifest stream it packages, so a caller cannot pass them.
var bundleSealExclusions = map[string]string{
	"Objects": "projected by bundle.Build from the manifests it packages, never passed in",
}

// fullyDeclaredShape sets every release.Shape field a declaration can carry to
// a non-default value. TestBundleSealsEveryShapeField fails when a field is
// added to release.Shape and left zero here, so a new field cannot be added
// without deciding whether the bundle seals it.
func fullyDeclaredShape() release.Shape {
	replicas := 2
	policy := release.DefaultDeliveryPolicy()
	policy.VulnGate.Mode = release.DeliveryEnforce
	delivery := release.ShapeIdentityDelivery{
		Kind: release.ControlPlaneSecretIdentity, Name: "forge-identity-identity", Namespace: "app",
		Cluster: "prod-ctx", ConnectedCluster: "prod",
	}
	return release.Shape{
		Kind:      release.EnvPersistent,
		Workloads: []release.ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "prod-ctx", Artifact: "api", Replicas: &replicas}},
		Secrets:   []release.ShapeSecret{{Name: "DATABASE_URL", Provider: "external", DeclaredBy: []string{"api"}}},
		Domains:   []string{"api.acme.test"},
		Clusters:  []string{"prod-ctx"},

		ControlPlaneSecrets: []release.ShapeControlPlaneSecret{{
			Name: "telemetry", Namespace: "app", Cluster: "prod-ctx", ConnectedCluster: "prod",
			Kind: release.ControlPlaneSecretTelemetryToken, Keys: []string{"token"},
		}, {
			Name: delivery.Name, Namespace: delivery.Namespace, Cluster: delivery.Cluster, ConnectedCluster: delivery.ConnectedCluster,
			Kind: release.ControlPlaneSecretIdentity, Keys: []string{"ISSUER", "AUDIENCE"}, Required: true,
		}},
		Alerts: []release.ShapeAlert{{
			Alert: "HighErrorRate", Expr: "up == 0", For: "5m",
			Labels: map[string]string{"severity": "critical"}, Source: release.AlertSourceUser,
		}},
		AlertRoutes: []release.ShapeAlertRoute{
			{Match: map[string]string{"severity": "critical"}, Receiver: "pager"},
			{Receiver: "default"},
		},
		ObservabilityPolicy: &release.ShapeObservabilityPolicy{
			Enabled: true, Disabled: []string{release.ObsCategoryLogs}, OptIn: []string{release.ObsOptInReplay},
			Retention: &release.ShapeRetention{ReplayDays: 7, ErrorsDays: 60, ProductEventsDays: 120},
		},
		FrontendTelemetry: []release.ShapeFrontendTelemetry{{
			Frontend: "web", Sourcemaps: release.SourcemapsOff, ErrorsIgnore: []string{"AbortError", "re:^Failed"},
			Funnels: []release.ShapeFunnel{{ID: "onboarding", Title: "Onboarding", Steps: []string{"signup", "first_chat"}, Window: "7d", By: "user"}},
		}},
		DeliveryPolicy: &policy,
		DataAccess:     &release.ShapeDataAccess{DBQuery: release.DBQueryDeny},
		Databases: []release.ShapeDatabase{{Name: "app-db", Dashboards: &release.ShapeDatabaseDashboards{
			Enabled: true, RowCap: 500, TimeoutMs: 3000, MaxConcurrency: 1,
		}}},
		UptimeChecks: []release.ShapeUptimeCheck{{Name: "web", Target: "web", Method: "GET", IntervalSeconds: 60, TimeoutSeconds: 10, Source: release.UptimeSourceDefault}},
		ReleaseHealth: &release.ShapeReleaseHealth{
			WindowSeconds: 1800, MinRequests: 100, MaxErrorRateIncrease: 0.02, MaxP95Ratio: 2,
			NewIssueMinEvents: 5, FailOn: []string{release.HealthErrorRate, release.HealthLatency},
		},
		Identity: &release.ShapeIdentity{
			Name: "identity", Runtime: release.IdentityRuntimeHosted,
			Login: release.ShapeIdentityLogin{Mode: release.IdentityLoginNative},
			Roles: []release.ShapeIdentityRole{{Key: "admin", MembersFrom: release.IdentityMembersConsole}},
			Applications: []release.ShapeIdentityApplication{
				{Name: "prod:api", Workload: "api", Kind: release.IdentityAppNativeBroker},
			},
			Consumers:  []release.ShapeIdentityConsumer{{Workload: "api", Application: "prod:api"}},
			Deliveries: []release.ShapeIdentityDelivery{delivery},
		},
		StoreSecrets: []release.ShapeStoreSecret{{
			Cluster: "prod-ctx", ConnectedCluster: "prod", Namespace: "app", Store: release.StoreOpenBao,
			Names: []string{"SIGNING_KEY", "SIGNING_PUB"}, Pins: []release.ShapeStorePin{{Name: "SIGNING_KEY", Version: 2}},
			Immutable: []string{"SIGNING_PUB"},
		}},
		GeneratedSecrets: []release.ShapeGeneratedSecret{{Store: release.StoreOpenBao, Kind: "rsa_2048", Names: []string{"SIGNING_KEY", "SIGNING_PUB"}}},
		OpenBao:          &release.ShapeOpenBao{Name: "openbao", Namespace: "openbao", Cluster: "prod-ctx", ConnectedCluster: "prod", Image: "quay.io/openbao/openbao:2.6.2"},
		ExternalSecretsOperators: []release.ShapeExternalSecretsOperator{
			{Cluster: "prod-ctx", ConnectedCluster: "prod", Namespace: "external-secrets", Version: "2.13.0"},
		},
		Applier: release.ApplierForge,
		Units: []release.ShapeUnit{
			{Name: "base", Applier: release.UnitApplierForge},
			{Name: "reliant", Applier: release.UnitApplierHub},
		},
	}
}

// The release shape recorded on the ledger and the shape sealed in the bundle's
// config blob must say the same thing. A field that bundleShapeInputOf forgot
// (observability_policy, once) is recorded but never sealed, so the bundle a
// reconciler or the control plane reads silently lacks what the env declared.
func TestBundleSealsEveryShapeField(t *testing.T) {
	declared := fullyDeclaredShape()

	typ := reflect.TypeOf(declared)
	val := reflect.ValueOf(declared)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, excluded := bundleSealExclusions[name]; excluded {
			continue
		}
		if val.Field(i).IsZero() {
			t.Errorf("fullyDeclaredShape leaves Shape.%s zero: set it, or list it in bundleSealExclusions with the reason the bundle does not seal it", name)
		}
	}

	built, err := bundle.Build(context.Background(), bundle.BuildInput{
		Project:    "shop",
		Env:        "prod",
		Release:    "v1.0.0",
		Pins:       release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Provenance: release.Provenance{Commit: rep40('c'), Tree: rep40('a')},
		Shape: bundleShapeInputOf(envShapeDoc{
			Project:   "shop",
			Env:       "prod",
			Shape:     declared,
			manifests: bundleTestStream,
		}),
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("bundle.Build: %v", err)
	}
	var doc release.BundleDoc
	if err := json.Unmarshal(built.ConfigBlob(), &doc); err != nil {
		t.Fatalf("decode the sealed config blob: %v", err)
	}

	want := declared.Canonical()
	sealed := reflect.ValueOf(doc.Shape)
	wantV := reflect.ValueOf(want)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if _, excluded := bundleSealExclusions[name]; excluded {
			continue
		}
		w, _ := json.Marshal(wantV.Field(i).Interface())
		g, _ := json.Marshal(sealed.Field(i).Interface())
		if string(w) != string(g) {
			t.Errorf("the bundle does not seal Shape.%s as declared:\n declared %s\n sealed   %s", name, w, g)
		}
	}
	for _, key := range []string{`"observability_policy"`, `"identity"`} {
		if !strings.Contains(string(built.ConfigBlob()), key) {
			t.Errorf("the config blob has no %s", key)
		}
	}
}

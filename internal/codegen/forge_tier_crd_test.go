package codegen

import (
	"strings"
	"testing"
)

// TestForgeTierCRDsProject loads forge's own tier package through the SAME
// loader a consumer's `forge generate` uses (by import path, not ./api/...)
// and checks that the three forge.dev CRDs come out structurally sound: a
// status subresource, no identity in spec, and the working defaults present
// in the schema the API server applies.
func TestForgeTierCRDsProject(t *testing.T) {
	if testing.Short() {
		t.Skip("loads Go packages (seconds); full mode only")
	}
	restore, err := chdir(forgeRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	docs, err := loadCRDDocs([]string{ForgeTierAPIPackage})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]CRDDoc{}
	for _, d := range docs {
		kinds[d.Kind] = d
	}
	for _, k := range []string{"Workload", "StaticSite", "ManagedDatabase"} {
		d, ok := kinds[k]
		if !ok {
			t.Fatalf("no CRD projected for %s (got %d docs)", k, len(docs))
		}
		if d.CRD.Spec.Group != "forge.dev" {
			t.Errorf("%s group = %q, want forge.dev", k, d.CRD.Spec.Group)
		}
		if len(d.CRD.Spec.Versions) != 1 || d.CRD.Spec.Versions[0].Subresources == nil || d.CRD.Spec.Versions[0].Subresources.Status == nil {
			t.Errorf("%s: status must be a subresource, so spec writers cannot clobber it", k)
		}
		spec := d.CRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
		for field := range spec.Properties {
			if strings.HasPrefix(strings.ToLower(field), "org") || field == "namespace" || field == "environmentId" {
				t.Errorf("%s spec carries identity field %q", k, field)
			}
		}
	}
	wl := kinds["Workload"].CRD
	if names := wl.Spec.Names; names.Plural != "workloads" || len(names.ShortNames) != 1 || names.ShortNames[0] != "wl" || len(names.Categories) != 1 || names.Categories[0] != "forge" {
		t.Errorf("Workload names = %+v, want plural workloads, shortName wl, category forge", names)
	}
	wlSpec := wl.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	if d := wlSpec.Properties["kind"].Default; d == nil || string(d.Raw) != `"service"` {
		t.Errorf("spec.kind default = %v, want service", d)
	}
	if d := wlSpec.Properties["replicas"].Default; d == nil || string(d.Raw) != "1" {
		t.Errorf("spec.replicas default = %v, want 1", d)
	}
	// Kind-dependent defaults must NOT be static: the API server would stamp
	// them on every kind, and Validate would then refuse the CR it was handed.
	for _, f := range []string{"deployPhase", "leaderElection", "probes"} {
		if d := wlSpec.Properties[f].Default; d != nil {
			t.Errorf("spec.%s default = %s, want none — its default depends on the kind", f, d.Raw)
		}
	}
	// The status keeps the billing inputs.
	wlStatus := wl.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["status"]
	for _, f := range []string{"readyReplicas", "observedImage", "lastReadyAt", "serviceName", "workloadName", "hostname", "phase"} {
		if _, ok := wlStatus.Properties[f]; !ok {
			t.Errorf("Workload status lacks %q", f)
		}
	}
	res := wlSpec.Properties["resources"]
	if d := res.Properties["cpuRequestMillicores"].Default; d == nil || string(d.Raw) != "250" {
		t.Errorf("resources.cpuRequestMillicores default = %v, want 250 — the API server must apply the same working default as Go and KCL", d)
	}
	// The limits carry NO default: an unset limit is its request, which no
	// static default can express. The operator's WithDefaults fills it.
	for _, f := range []string{"cpuLimitMillicores", "memoryLimitBytes"} {
		if d := res.Properties[f].Default; d != nil {
			t.Errorf("resources.%s default = %s, want none — the API server would stamp it under a larger request and the operator would refuse the CR", f, d.Raw)
		}
	}
	mdb := kinds["ManagedDatabase"].CRD.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties["spec"]
	if d := mdb.Properties["deletionPolicy"].Default; d == nil || string(d.Raw) != `"retain"` {
		t.Errorf("deletionPolicy default = %v, want retain", d)
	}
	if _, err := GenerateCRDKCL(docs, ControllerToolsVersion); err != nil {
		t.Fatalf("render the forge.dev CRD module: %v", err)
	}
}

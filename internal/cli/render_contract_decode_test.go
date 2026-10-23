package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/deploytarget"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// The render-contract goldens (internal/cli/testdata/render_contract, owned by
// P2a) are the §9.1 JSON a real `output = forge.render(bundle)` evaluates to.
// These tests decode EXACTLY those files through the production decoder, and
// drive the production consumers (deploy grouping, host argv, manifest
// expansion) from them. A contract change that breaks a consumer fails here,
// not in a user's deploy.

func loadContract(t *testing.T, name string) (*KCLEntities, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "render_contract", name+".json"))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	e, err := parseKCLEntities(raw)
	if err != nil {
		t.Fatalf("decode golden %s: %v", name, err)
	}
	return e, raw
}

func runtimesOf(e *KCLEntities) map[string][]string {
	out := map[string][]string{}
	for _, w := range e.Workloads {
		out[w.Runtime.Type] = append(out[w.Runtime.Type], w.Name)
	}
	return out
}

// Every golden decodes, strictly, and every workload's runtime block carries
// exactly the variant its type names — nothing guessed, nothing dropped.
func TestRenderContract_EveryGoldenDecodes(t *testing.T) {
	for _, name := range []string{"host", "compose", "cluster", "hosted", "build-only", "mixed", "unit-split"} {
		t.Run(name, func(t *testing.T) {
			e, _ := loadContract(t, name)
			if len(e.Workloads) == 0 {
				t.Fatalf("golden %s decoded no workloads", name)
			}
			if e.Project == "" || e.Env == "" {
				t.Errorf("project/env not decoded: %q/%q", e.Project, e.Env)
			}
			for _, w := range e.Workloads {
				rt := w.Runtime
				variant := map[string]bool{
					RuntimeHost: rt.Host != nil, RuntimeCompose: rt.Compose != nil,
					RuntimeCluster: rt.Cluster != nil, RuntimeBuildOnly: rt.BuildOnly != nil,
				}
				for typ, set := range variant {
					if set != (typ == rt.Type) {
						t.Errorf("%s: runtime %q has variant %s set=%v", w.Name, rt.Type, typ, set)
					}
				}
				if w.Kind != string(w.Spec.EffectiveKind()) {
					t.Errorf("%s: kind %q != spec kind %q", w.Name, w.Kind, w.Spec.EffectiveKind())
				}
			}
		})
	}
}

// Per runtime, the golden's workloads land on that runtime and the
// runtime-specific facts arrive where consumers read them.
func TestRenderContract_RuntimeFacts(t *testing.T) {
	t.Run("host", func(t *testing.T) {
		e, _ := loadContract(t, "host")
		if len(e.WorkloadsOn(RuntimeHost)) == 0 {
			t.Fatal("no host workloads")
		}
		if len(e.Infra) == 0 {
			t.Error("host golden's HostInfra entry was not decoded into Infra")
		}
		// The engine-specific fields survive the decode: render.k's
		// projection is a whitelist, so a field missing there or here never
		// reaches the supervisor however correctly the env declares it.
		for _, hi := range e.Infra {
			switch hi.Engine {
			case "nats":
				if hi.NATSMonitorPort != 8223 {
					t.Errorf("nats: monitor port %d, want 8223", hi.NATSMonitorPort)
				}
			case "temporal":
				if len(hi.TemporalNamespaces) != 1 || hi.TemporalNamespaces[0] != "acme" {
					t.Errorf("temporal: namespaces %v, want [acme]", hi.TemporalNamespaces)
				}
			}
		}
		for _, w := range e.WorkloadsOn(RuntimeHost) {
			if w.Spec.Image != "" {
				t.Errorf("%s: host workload carries spec.image %q", w.Name, w.Spec.Image)
			}
		}
	})
	t.Run("compose", func(t *testing.T) {
		e, _ := loadContract(t, "compose")
		for _, w := range e.WorkloadsOn(RuntimeCompose) {
			if w.Build.Type != "" {
				t.Errorf("%s: a compose workload is not built by forge, got build %q", w.Name, w.Build.Type)
			}
		}
	})
	t.Run("cluster", func(t *testing.T) {
		e, _ := loadContract(t, "cluster")
		ws := e.WorkloadsOn(RuntimeCluster)
		if len(ws) == 0 {
			t.Fatal("no cluster workloads")
		}
		for _, w := range ws {
			if w.Runtime.Cluster.Cluster == "" || w.Runtime.Cluster.Namespace == "" {
				t.Errorf("%s: cluster runtime without cluster/namespace: %+v", w.Name, *w.Runtime.Cluster)
			}
			if w.Spec.Image == "" {
				t.Errorf("%s: a cluster workload's spec.image is the resolved ref, got empty", w.Name)
			}
		}
		if e.ManifestNamespace == "" {
			t.Error("ManifestNamespace not derived from output.manifests")
		}
	})
	t.Run("hosted", func(t *testing.T) {
		e, _ := loadContract(t, "hosted")
		if !e.HasHosted() || e.ControlPlane == nil {
			t.Fatalf("hosted golden: HasHosted=%v control_plane=%v", e.HasHosted(), e.ControlPlane)
		}
		var kinds []string
		for _, w := range e.WorkloadsOn(RuntimeHosted) {
			kinds = append(kinds, w.Kind)
		}
		sort.Strings(kinds)
		if !strings.Contains(strings.Join(kinds, ","), "job") {
			t.Errorf("hosted golden declares a hosted job; decoded kinds %v", kinds)
		}
	})
	t.Run("build-only", func(t *testing.T) {
		e, _ := loadContract(t, "build-only")
		bo := e.WorkloadsOn(RuntimeBuildOnly)
		if len(bo) == 0 || len(bo[0].Runtime.BuildOnly.BuildVariants) == 0 {
			t.Fatalf("build-only workload or its variants not decoded: %+v", bo)
		}
	})
}

// A mixed env binds every runtime side by side, and the deploy dispatch turns
// it into per-runtime groups: a k8s-cluster group AND a hosted group in the
// SAME deploy — hosting is per workload, never an env mode — plus compose and
// host-infra. Host and build-only workloads join no group.
func TestRenderContract_MixedEnvGroupsPerWorkloadRuntime(t *testing.T) {
	e, _ := loadContract(t, "mixed")
	rts := runtimesOf(e)
	for _, want := range []string{RuntimeHost, RuntimeCompose, RuntimeCluster, RuntimeHosted, RuntimeBuildOnly} {
		if len(rts[want]) == 0 {
			t.Fatalf("mixed golden has no %s workload (runtimes %v)", want, rts)
		}
	}
	groups, err := buildDeployGroups("dev", e, "fallback-ns")
	if err != nil {
		t.Fatalf("buildDeployGroups: %v", err)
	}
	byProvider := map[string][]string{}
	for _, g := range groups {
		for _, s := range g.Services {
			byProvider[g.ProviderID] = append(byProvider[g.ProviderID], s.Name)
		}
	}
	for _, w := range rts[RuntimeCluster] {
		if !containsName(byProvider["k8s-cluster"], w) {
			t.Errorf("cluster workload %s is in no k8s-cluster group: %v", w, byProvider)
		}
	}
	for _, w := range rts[RuntimeHosted] {
		if !containsName(byProvider[deploytarget.HostedProviderID], w) {
			t.Errorf("hosted workload %s is in no hosted group: %v", w, byProvider)
		}
	}
	for _, w := range rts[RuntimeCompose] {
		if !containsName(byProvider["compose"], w) {
			t.Errorf("compose workload %s is in no compose group: %v", w, byProvider)
		}
	}
	for _, w := range append(rts[RuntimeHost], rts[RuntimeBuildOnly]...) {
		for p, names := range byProvider {
			if containsName(names, w) {
				t.Errorf("%s workload %s landed in the %s group", runtimeOf(e, w), w, p)
			}
		}
	}
	local, hosted := splitHostedGroups(groups)
	if len(hosted) != 1 || len(local) == 0 {
		t.Fatalf("split: %d local, %d hosted group(s); a mixed env is BOTH", len(local), len(hosted))
	}
	if destinationOf(e) != destinationMixed {
		t.Errorf("destinationOf(mixed) = %q, want mixed", destinationOf(e))
	}
}

func runtimeOf(e *KCLEntities, name string) string {
	if w := e.FindWorkload(name); w != nil {
		return w.Runtime.Type
	}
	return ""
}

func containsName(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// The hosted group is published as ONE Workload CR per hosted workload — jobs
// included, not silently dropped — admitted by the deploy path's own plan
// (Workload.Validate(ProfileRestricted) + a restricted set render).
func TestRenderContract_HostedGroupPublishesEveryWorkloadIncludingJobs(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	group, err := buildHostedGroup("prod", e)
	if err != nil || group == nil {
		t.Fatalf("buildHostedGroup: %v (group %v)", err, group)
	}
	want := map[string]bool{}
	for _, w := range e.WorkloadsOn(RuntimeHosted) {
		want[w.Name] = true
	}
	var published []string
	for _, s := range group.Services {
		if s.Hosted != nil && s.Hosted.Tier == deploytarget.HostedTierWorkload {
			published = append(published, s.Name)
			delete(want, s.Name)
		}
	}
	if len(want) > 0 {
		t.Errorf("hosted workloads not published as Workload CRs: %v (published %v)", want, published)
	}
	items, err := deploytarget.PreflightHosted(*group)
	if err != nil {
		t.Fatalf("the golden's hosted part is not admissible: %v", err)
	}
	for _, it := range items {
		if it.Workload != nil && it.Workload.EffectiveKind() == deployv1alpha1.KindJob {
			return
		}
	}
	t.Error("no job among the admitted hosted items")
}

// A hosted item with no control_plane has nowhere to go: refused with the fix.
func TestHostedGroupRefusedWithoutControlPlane(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	e.ControlPlane = nil
	_, err := buildHostedGroup("prod", e)
	if err == nil || !strings.Contains(err.Error(), "control_plane") {
		t.Fatalf("err = %v, want a refusal naming control_plane", err)
	}
	if _, gerr := buildDeployGroups("prod", e, ""); gerr == nil {
		t.Error("buildDeployGroups accepted hosted workloads with no control_plane")
	}
}

// Host workloads derive their argv from build + args through the ONE
// derivation (hostlaunch.BuildCmd via hostRunnerSpec): no `server <name>`.
func TestRenderContract_HostArgvIsDerived(t *testing.T) {
	e, _ := loadContract(t, "host")
	for _, w := range e.WorkloadsOn(RuntimeHost) {
		spec := hostRunnerSpec(w)
		if w.GoBuild() != nil && spec.GoPkg != w.GoBuild().Cmd {
			t.Errorf("%s: GoPkg %q, want the build cmd %q", w.Name, spec.GoPkg, w.GoBuild().Cmd)
		}
		if !reflect.DeepEqual(spec.Args, w.Spec.Args) {
			t.Errorf("%s: args %v, want spec.args %v", w.Name, spec.Args, w.Spec.Args)
		}
	}
	job := e.FindWorkload("migrate")
	if job == nil || !job.IsJob() {
		t.Fatalf("host golden has no migrate job: %+v", job)
	}
	if got := hostJobs(e); len(got) == 0 || got[0].Name != "migrate" {
		t.Errorf("hostJobs = %v, want the migrate job", got)
	}
}

// Every Cluster-bound workload has exactly one record in output.manifests, its
// spec byte-identical to the workload's, and the record expands through
// RenderWorkloads (the applied stream carries no record).
func TestRenderContract_ClusterRecordsMatchAndExpand(t *testing.T) {
	for _, name := range []string{"cluster", "mixed"} {
		t.Run(name, func(t *testing.T) {
			e, raw := loadContract(t, name)
			var doc struct {
				Output struct {
					Workloads []struct {
						Name string          `json:"name"`
						Spec json.RawMessage `json:"spec"`
					} `json:"workloads"`
					Manifests []struct {
						Kind     string `json:"kind"`
						Metadata struct {
							Name   string            `json:"name"`
							Labels map[string]string `json:"labels"`
						} `json:"metadata"`
						Spec json.RawMessage `json:"spec"`
					} `json:"manifests"`
				} `json:"output"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			records := map[string]json.RawMessage{}
			for _, m := range doc.Output.Manifests {
				if m.Kind == "Workload" {
					if _, dup := records[m.Metadata.Name]; dup {
						t.Errorf("two records for %s", m.Metadata.Name)
					}
					records[m.Metadata.Name] = m.Spec
					if m.Metadata.Labels[cluster.ClusterRoutingLabel] == "" {
						t.Errorf("record %s carries no %s label", m.Metadata.Name, cluster.ClusterRoutingLabel)
					}
				}
			}
			for _, w := range doc.Output.Workloads {
				rec, has := records[w.Name]
				onCluster := runtimeOf(e, w.Name) == RuntimeCluster
				if has != onCluster {
					t.Errorf("%s (runtime %s): record present=%v", w.Name, runtimeOf(e, w.Name), has)
				}
				if has && string(rec) != string(w.Spec) {
					t.Errorf("%s: record spec differs from workloads[].spec", w.Name)
				}
			}
			expanded, err := cluster.ExtractManifests(raw)
			if err != nil {
				t.Fatalf("ExtractManifests over the golden: %v", err)
			}
			if strings.Contains(expanded, "kind: Workload\n") {
				t.Error("a Workload record survived expansion into the applied stream")
			}
			for n := range records {
				if !strings.Contains(expanded, "name: "+n+"\n") {
					t.Errorf("expanded stream renders nothing named %s", n)
				}
			}
		})
	}
}

// A top-level `manifests` var is the retired two-entrypoint shape and is
// refused, naming the one entrypoint.
func TestParseKCLEntities_RefusesTopLevelManifests(t *testing.T) {
	_, err := parseKCLEntities([]byte(`{"manifests":[],"output":{"workloads":[]}}`))
	if err == nil || !strings.Contains(err.Error(), "forge.render(bundle)") {
		t.Fatalf("err = %v, want a refusal naming output = forge.render(bundle)", err)
	}
}

// The spec decodes STRICTLY: a key forge's WorkloadSpec does not know is an
// error naming the workload, never silently dropped.
func TestParseKCLEntities_SpecIsStrict(t *testing.T) {
	_, err := parseKCLEntities([]byte(`{"output":{"workloads":[{"name":"api","kind":"service","image":"","build":null,` +
		`"runtime":{"type":"hosted"},"spec":{"kind":"service","image":"x","network":"public"}}]}}`))
	if err == nil || !strings.Contains(err.Error(), `"api"`) || !strings.Contains(err.Error(), "network") {
		t.Fatalf("err = %v, want a strict-decode refusal naming the workload and the field", err)
	}
}

// A workload with no runtime, or an unknown one, is refused: nothing runs it.
func TestParseKCLEntities_RuntimeIsRequired(t *testing.T) {
	for _, rt := range []string{`null`, `{"type":"external"}`, `{}`} {
		_, err := parseKCLEntities([]byte(`{"output":{"workloads":[{"name":"api","kind":"service","build":null,` +
			`"runtime":` + rt + `,"spec":{"image":"x"}}]}}`))
		if err == nil || !strings.Contains(err.Error(), "runtime") {
			t.Errorf("runtime %s: err = %v, want a runtime refusal", rt, err)
		}
	}
}

// SecretKeyRef.optional reaches KCLEnvVar.SecretOptional, so the store
// pre-flight still exempts a config-codegen `optional: true` secret.
func TestWorkloadEnvVars_ProjectsSecretOptional(t *testing.T) {
	e, _ := loadContract(t, "host")
	api := e.FindWorkload("api")
	if api == nil {
		t.Fatal("no api in the host golden")
	}
	var found bool
	for _, ev := range api.EnvVars() {
		if ev.Name == "API_KEY" {
			found = true
			if ev.SecretRef != "acme-secrets" || ev.SecretKey != "api_key" || !ev.SecretOptional {
				t.Fatalf("API_KEY = %+v, want the optional secret_ref acme-secrets/api_key", ev)
			}
		}
	}
	if !found {
		t.Fatal("API_KEY (secretRef, optional) not projected")
	}
}

// A managedSecret decodes to its {name, optional} object and is NOT a store
// Secret ref (the provider materializes it into forge-managed-secrets), so
// EnvVars leaves it out. An OPTIONAL one is also not a value `forge secret
// ensure` demands; a required one is.
func TestWorkloadEnvVars_ManagedSecretIsNotAStoreRef(t *testing.T) {
	e, _ := loadContract(t, "hosted")
	api := e.FindWorkload("api")
	if api == nil {
		t.Fatal("no api in the hosted golden")
	}
	var ms *deployv1alpha1.ManagedSecretRef
	for _, ev := range api.Spec.Env {
		if ev.Name == "API_KEY" {
			ms = ev.ManagedSecret
		}
	}
	if ms == nil || ms.Name != "API_KEY" || !ms.Optional {
		t.Fatalf("API_KEY managedSecret = %+v, want {API_KEY optional}", ms)
	}
	for _, ev := range api.EnvVars() {
		if ev.Name == "API_KEY" {
			t.Fatalf("a managedSecret was projected as a store channel: %+v", ev)
		}
	}
	if got := managedSecretNamesForService(api); len(got) != 0 {
		t.Errorf("optional managedSecret demanded by ensure: %v", got)
	}
	required := *api
	required.Spec.Env = []deployv1alpha1.EnvVar{{Name: "K", ManagedSecret: &deployv1alpha1.ManagedSecretRef{Name: "STRIPE_KEY"}}}
	if got := managedSecretNamesForService(&required); len(got) != 1 || got[0] != "STRIPE_KEY" {
		t.Errorf("required managedSecret names = %v, want [STRIPE_KEY]", got)
	}
}

// The mixed-split golden is the decoder's contract for an env mid-move onto the
// hub: the declaration survives KCL, the units survive the decoder, and the
// split that falls out of them is the one the author wrote.
func TestRenderContract_UnitSplit(t *testing.T) {
	e, raw := loadContract(t, "unit-split")
	if !reflect.DeepEqual(e.HubConverges, []string{"reliant"}) {
		t.Fatalf("hub_converges did not survive the render: %v", e.HubConverges)
	}
	var doc struct {
		Output map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Output["hub_converges"]; !ok {
		t.Fatal("the golden does not carry the declaration")
	}
	split, err := unitSplitOf(e)
	if err != nil {
		t.Fatalf("unitSplitOf: %v", err)
	}
	if !reflect.DeepEqual(split.Hub, []string{"reliant"}) || !reflect.DeepEqual(split.Forge, []string{"base", "infra"}) {
		t.Fatalf("split = %s, want hub: reliant; forge: base, infra", split.Describe())
	}
	// Each workload's object carries the unit it was declared in, which is what
	// direct apply drops by and what the bundle's trees are cut by.
	units := map[string]string{}
	for _, m := range e.Workloads {
		units[m.Name] = m.DeliveryUnit
	}
	if units["api"] != "reliant" || units["nats"] != "infra" || units["admin"] != "" {
		t.Errorf("workload units = %v", units)
	}
	// A frontend forge ships belongs to no unit and so is forge's whatever the
	// split says.
	if !forgeAppliesLocally(e, split) {
		t.Error("the Firebase frontend and the forge-applied units are forge's work")
	}
	allHub := unitSplit{Hub: split.Forge, Source: unitSplitDeclared}
	allHub.Hub = append(allHub.Hub, "reliant")
	if !forgeAppliesLocally(e, allHub) {
		t.Error("with every unit moved, forge still ships the frontend from a machine")
	}
	// Direct apply is allowed for an env that declares the split.
	if !DirectApplyAllowed(e) {
		t.Error("an env that declares hub_converges is mid-move and may be direct-applied for now")
	}
}

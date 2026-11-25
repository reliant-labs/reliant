package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/release"
)

func storeBacked(name, ns, policy string, shadow bool) ExternalSecretEntity {
	return ExternalSecretEntity{
		Name: name, Namespace: ns, Keys: []string{"k"},
		Source: &StoreSourceEntity{
			RenderedStoreSource: release.RenderedStoreSource{
				Store: release.RenderedStore{Type: release.StoreHosted},
				Data:  map[string]string{"k": strings.ToUpper(name)},
			},
			CreationPolicy: policy, Shadow: shadow,
		},
	}
}

// An Orphan store-backed Secret is created by ESO AFTER the deploy, so the live
// existence check must not demand it; a Merge one only updates a Secret that
// exists, so it keeps the check. A declaration with no source is untouched.
func TestStoreBackedOrphanIsNotAPrecondition(t *testing.T) {
	e := &KCLEntities{RequiredSecrets: []ExternalSecretEntity{
		storeBacked("orphan", "app", "Orphan", false),
		storeBacked("merge", "app", "Merge", false),
		{Name: "oob", Namespace: "app", Keys: []string{"k"}},
	}}
	var required []string
	for _, r := range requiredSecretsForPreflight(e) {
		required = append(required, r.Name)
	}
	if strings.Join(required, ",") != "merge,oob" {
		t.Fatalf("live-checked = %v, want merge and oob only", required)
	}

	var provisioned []string
	for _, p := range provisionedByDeployForPreflight(e) {
		provisioned = append(provisioned, p.Name)
	}
	if strings.Join(provisioned, ",") != "orphan" {
		t.Fatalf("provisioned by the deploy = %v, want orphan only", provisioned)
	}
}

// A shadow ExternalSecret syncs <name>-eso-shadow, which ESO creates; the
// workload mounts <name>, which the shadow does not write.
func TestStoreBackedShadowAlsoProvisionsItsShadow(t *testing.T) {
	e := &KCLEntities{RequiredSecrets: []ExternalSecretEntity{storeBacked("db", "app", "Orphan", true)}}
	var got []string
	for _, p := range storeProvisionedByDeploy(e) {
		got = append(got, p.Namespace+"/"+p.Name)
	}
	if strings.Join(got, ",") != "app/db,app/db-eso-shadow" {
		t.Fatalf("got %v", got)
	}
}

// refuseUnsyncable's exemption: a hub-reconciled deploy is not refused for a
// Secret ESO delivers, and is still refused for one nothing delivers.
func TestHubReconciledDeployIsNotRefusedForStoreBackedSecrets(t *testing.T) {
	e := &KCLEntities{RequiredSecrets: []ExternalSecretEntity{storeBacked("billing", "app", "Orphan", false)}}
	refs := []release.BundleSecretRef{
		{Cluster: "hub", Namespace: "app", Name: "billing"},
		{Cluster: "hub", Namespace: "app", Name: "raw-creds"},
	}
	kept := withoutStoreBacked(refs, e)
	if len(kept) != 1 || kept[0].Name != "raw-creds" {
		t.Fatalf("kept = %+v, want only raw-creds", kept)
	}
	err := refuseUnsyncableHostedSecrets("prod", kept)
	if err == nil || !strings.Contains(err.Error(), "app/raw-creds") || strings.Contains(err.Error(), "billing") {
		t.Fatalf("want a refusal naming raw-creds only, got %v", err)
	}
	// The refusal names its escapes, the store one first.
	for _, w := range []string{"source = forge.StoreSource", "forge secret set", "forge env secrets sync prod"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal lacks escape %q:\n%v", w, err)
		}
	}
	if err := refuseUnsyncableHostedSecrets("prod", withoutStoreBacked(refs[:1], e)); err != nil {
		t.Fatalf("a deploy needing only store-backed Secrets must not be refused: %v", err)
	}
}

func TestEnvSyncedSecretRefsExcludesStoreBacked(t *testing.T) {
	e := &KCLEntities{RequiredSecrets: []ExternalSecretEntity{storeBacked("raw-creds", "app", "Orphan", false)}}
	refs, err := envSyncedSecretRefs(e, nil, "app", secretStream)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("raw-creds is delivered by ESO, not synced by forge: %+v", refs)
	}
}

func expectationAt(t *testing.T, ex []cluster.StoreExpectation, ctx string) cluster.StoreExpectation {
	t.Helper()
	for _, x := range ex {
		if x.Context == ctx {
			return x
		}
	}
	t.Fatalf("no expectation for %q in %+v", ctx, ex)
	return cluster.StoreExpectation{}
}

// The expectation per cluster: what the deploy installs depends on --target,
// and the names are those a person must set (not the ones forge generates).
func TestStoreExpectations(t *testing.T) {
	e := storeEnv()
	e.ExternalSecretsOperators = []release.RenderedExternalSecretsOperator{{Cluster: "ctx-hub", Namespace: "external-secrets", Version: "2.13.0"}}
	e.RequiredSecrets[0].Source.Generate = map[string]release.RenderedGeneratedSecret{"DB_PASSWORD": {Kind: "password"}}

	t.Run("untargeted deploy installs the operator and the store", func(t *testing.T) {
		x := expectationAt(t, storeExpectations(e, nil, "acme.prod"), "ctx-hub")
		if !x.InstallsESO || x.OpenBao == nil || !x.OpenBao.Installs || x.Owner != "acme.prod" {
			t.Fatalf("got %+v", x)
		}
		kinds := map[string][]string{}
		for _, r := range x.Reads {
			kinds[r.Label()] = r.Names
			if r.Kind == release.StoreOpenBao && !r.Pending {
				t.Errorf("an OpenBao this deploy installs holds nothing yet: must be Pending")
			}
		}
		if strings.Join(kinds["openbao"], ",") != "API_KEY" {
			t.Errorf("openbao names = %v, want API_KEY (DB_PASSWORD is generated)", kinds["openbao"])
		}
		if strings.Join(kinds["hosted"], ",") != "STRIPE_SECRET_KEY" || strings.Join(kinds["gcpsm:acme-prod"], ",") != "tls-cert-prod" {
			t.Errorf("reads = %v", kinds)
		}
	})

	t.Run("an app-only --target neither installs the operator nor the store", func(t *testing.T) {
		x := expectationAt(t, storeExpectations(e, []string{"api"}, "acme.prod"), "ctx-hub")
		if x.InstallsESO || x.OpenBao == nil || x.OpenBao.Installs {
			t.Fatalf("got %+v", x)
		}
		for _, r := range x.Reads {
			if r.Pending {
				t.Errorf("a store the deploy does not install is live and must be asked: %+v", r)
			}
		}
	})

	t.Run("--target external-secrets installs the operator only", func(t *testing.T) {
		x := expectationAt(t, storeExpectations(e, []string{"external-secrets"}, "acme.prod"), "ctx-hub")
		if !x.InstallsESO || x.OpenBao.Installs {
			t.Fatalf("got %+v", x)
		}
	})

	t.Run("an env with no store-backed Secret has no expectation", func(t *testing.T) {
		if got := storeExpectations(&KCLEntities{RequiredSecrets: []ExternalSecretEntity{{Name: "x"}}}, nil, "o"); got != nil {
			t.Fatalf("got %+v", got)
		}
		if liveStoreProbe("prod", &KCLEntities{}) != nil {
			t.Fatal("no store sources must build no probe")
		}
	})

	t.Run("a source on another cluster lands there", func(t *testing.T) {
		e2 := storeEnv()
		e2.RequiredSecrets[1].Source.Cluster = "ctx-daemon"
		x := expectationAt(t, storeExpectations(e2, nil, "acme.prod"), "ctx-daemon")
		if len(x.Reads) != 1 || x.Reads[0].Kind != release.StoreHosted {
			t.Fatalf("got %+v", x)
		}
	})
}

// End to end through runDeployPreflight's inputs: a deploy to a cluster with no
// operator, reading from the hosted store, is blocked before any apply.
func TestStoreExpectationsFeedThePreflight(t *testing.T) {
	e := storeEnv()
	opts := cluster.PreflightOpts{
		StoreExpect: storeExpectations(e, nil, "acme.prod"),
		StoreProbe:  noOperatorProbe{},
	}
	_, err := cluster.PreflightReport(t.Context(), opts)
	if err == nil {
		t.Fatal("a cluster with no operator and no install must block")
	}
	if !strings.Contains(err.Error(), "does not serve") {
		t.Fatalf("got %v", err)
	}
}

// The Merge Secret absent from the cluster is the ordinary required-secret
// failure, with its ordinary message: nothing store-specific is invented.
func TestMergeSecretKeepsTheOrdinaryExistenceCheck(t *testing.T) {
	e := &KCLEntities{RequiredSecrets: []ExternalSecretEntity{storeBacked("merge", "app", "Merge", false)}}
	rs := requiredSecretsForPreflight(e)
	if len(rs) != 1 || rs[0].Name != "merge" {
		t.Fatalf("got %+v", rs)
	}
	if got := secretSupplyForPreflight(e); len(got) != 1 || got[0].Kind != cluster.SupplyExternalSecret {
		t.Fatalf("a store-backed Secret still counts as supply for the render gate: %+v", got)
	}
}

type noOperatorProbe struct{}

func (noOperatorProbe) CRD(_ context.Context, _, _ string) (cluster.CRDState, error) {
	return cluster.CRDState{}, nil
}
func (noOperatorProbe) ConvergeSucceeded(_ context.Context, _, _, _ string) (bool, bool, error) {
	return false, false, nil
}
func (noOperatorProbe) Presence(_ context.Context, _ string, r cluster.StoreRead) (map[string]bool, error) {
	out := map[string]bool{}
	for _, n := range r.Names {
		out[n] = true
	}
	return out, nil
}

// The structured findings mirror PreflightResult.OK(): every blocking store
// finding is reported as blocking, and a store that could not be asked is not.
func TestDeployPreflightStoreFindingsAreStructured(t *testing.T) {
	res := cluster.PreflightResult{Store: cluster.StoreFindings{
		MissingCRDs:       []string{"cluster c does not serve ExternalSecret"},
		OwnerConflicts:    []string{"owned by other.dev"},
		StoreNotReady:     []string{"openbao has not converged"},
		MissingStoreNames: []string{"the hosted store holds no value for A"},
		Warnings:          []string{"could not ask the gcpsm store"},
	}}
	if res.OK() {
		t.Fatal("blocking store findings must make the result not OK")
	}
	blocking := map[string]bool{}
	for _, f := range deployFindingsFromPreflight(res) {
		blocking[f.Check] = f.Blocking
	}
	for _, check := range []string{"store_crd_missing", "store_operator_owner", "store_not_ready", "store_value_missing"} {
		if b, ok := blocking[check]; !ok || !b {
			t.Errorf("%s: present=%v blocking=%v, want a blocking finding", check, ok, b)
		}
	}
	if b, ok := blocking["store_unverified"]; !ok || b {
		t.Errorf("store_unverified: present=%v blocking=%v, want an advisory finding", ok, b)
	}
	warnOnly := cluster.PreflightResult{Store: cluster.StoreFindings{Warnings: []string{"w"}}}
	if !warnOnly.OK() {
		t.Error("a warning alone must not block")
	}
}

// An env the control plane has not seen holds no secrets: the presence probe
// answers "none present" (a refusal that names the missing names), not an
// error that would downgrade to a warning and let the deploy go unverified.
func TestStorePresenceTreatsAnUnknownHostedEnvAsHoldingNothing(t *testing.T) {
	prev := openStoreSource
	t.Cleanup(func() { openStoreSource = prev })
	openStoreSource = func(context.Context, string, *KCLEntities, storeRef, storeOptions, bool) (storeSource, error) {
		return nil, errHostedEnvNotFound
	}
	got, err := storePresence("prod", storeEnv())(context.Background(), "ctx-hub",
		cluster.StoreRead{Kind: release.StoreHosted, Names: []string{"STRIPE_SECRET_KEY"}})
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v: want an empty presence map and no error", got, err)
	}
}

// An env that declares forge.OpenBao is probed before any Secret reads from it:
// that is the deploy that may be adopting a live store, and the adoption warning
// rides this probe. The probe is the kubectl one, so it can answer the
// adoption read; an env with neither stays unprobed.
func TestLiveStoreProbe_AnOpenBaoOnlyEnvIsProbedForAnAdoption(t *testing.T) {
	probe := liveStoreProbe("prod", &KCLEntities{OpenBao: &OpenBaoEntity{}})
	if probe == nil {
		t.Fatal("an env that runs OpenBao must be probed, or an adoption is never warned about")
	}
	if _, ok := probe.(cluster.AdoptionProbe); !ok {
		t.Errorf("the live probe %T cannot answer the adoption read", probe)
	}
	if liveStoreProbe("prod", &KCLEntities{}) != nil || liveStoreProbe("prod", nil) != nil {
		t.Error("an env with no store and no OpenBao must stay unprobed")
	}
}

// The adoption advisory is reported, and never blocks.
func TestDeployFindings_AdoptionIsAdvisory(t *testing.T) {
	res := cluster.PreflightResult{Store: cluster.StoreFindings{Adoptions: []string{"this deploy ADOPTS the OpenBao"}}}
	if !res.OK() {
		t.Fatal("an adoption advisory must not block the deploy")
	}
	var found bool
	for _, f := range deployFindingsFromPreflight(res) {
		if f.Check == "store_adoption" {
			found = true
			if f.Blocking {
				t.Error("store_adoption must be advisory")
			}
		}
	}
	if !found {
		t.Error("the adoption advisory is missing from the deploy findings")
	}
}

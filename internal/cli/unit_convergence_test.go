package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// prodShaped is an env the way control-plane's prod is: a control plane, a
// cluster bound to a connected cluster, three delivery units, workloads in each,
// and a Firebase frontend forge ships from a machine.
func prodShaped() *KCLEntities {
	rt := func() RuntimeEntity {
		return RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{
			Cluster: "gke_prod", ConnectedCluster: "prod-control-plane",
		}}
	}
	return &KCLEntities{
		ControlPlane:  &ControlPlaneEntity{Endpoint: "https://cp.example"},
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_prod", Namespace: "prod", ConnectedCluster: "prod-control-plane"},
		DeliveryUnits: []release.DeliveryUnit{{Name: "reliant"}, {Name: "infra"}},
		Workloads: []WorkloadEntity{
			{Name: "api", Runtime: rt(), DeliveryUnit: "reliant"},
			{Name: "nats", Runtime: rt(), DeliveryUnit: "infra"},
			{Name: "admin", Runtime: rt()},
		},
	}
}

func withFrontend(e *KCLEntities) *KCLEntities {
	e.Frontends = []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}}}
	return e
}

func splitOf(t *testing.T, e *KCLEntities) unitSplit {
	t.Helper()
	s, err := unitSplitOf(e)
	if err != nil {
		t.Fatalf("unitSplitOf: %v", err)
	}
	return s
}

// THE TABLE. Every way an env says who applies it, and what that means per unit.
func TestUnitSplit_Table(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(*KCLEntities)
		wantHub    []string
		wantForge  []string
		wantSource unitSplitSource
	}{
		{
			name:       "hub_converges lists one unit: it moves, the rest stay forge's",
			mutate:     func(e *KCLEntities) { e.HubConverges = []string{"reliant"} },
			wantHub:    []string{"reliant"},
			wantForge:  []string{"base", "infra"},
			wantSource: unitSplitDeclared,
		},
		{
			name:       "hub_converges lists the default unit by name",
			mutate:     func(e *KCLEntities) { e.HubConverges = []string{"base", "infra"} },
			wantHub:    []string{"base", "infra"},
			wantForge:  []string{"reliant"},
			wantSource: unitSplitDeclared,
		},
		{
			name:       "every unit listed: forge applies nothing",
			mutate:     func(e *KCLEntities) { e.HubConverges = []string{"base", "infra", "reliant"} },
			wantHub:    []string{"base", "infra", "reliant"},
			wantSource: unitSplitDeclared,
		},
		{
			name:       "hub_converges = [] is a declaration: nothing moved, inference is OFF",
			mutate:     func(e *KCLEntities) { e.HubConverges = []string{} },
			wantForge:  []string{"base", "infra", "reliant"},
			wantSource: unitSplitDeclared,
		},
		{
			name:       "applier = forge is the same statement, spelled as a fact about the env",
			mutate:     func(e *KCLEntities) { e.Applier = applierForge },
			wantForge:  []string{"base", "infra", "reliant"},
			wantSource: unitSplitApplier,
		},
		{
			name:       "declares nothing, bound and plain: the hub converges the whole env (inferred)",
			mutate:     func(e *KCLEntities) {},
			wantHub:    []string{"base", "infra", "reliant"},
			wantSource: unitSplitInferred,
		},
		{
			name:      "declares nothing but forge ships a frontend: stays forge's (no silent handoff)",
			mutate:    func(e *KCLEntities) { withFrontend(e) },
			wantForge: []string{"base", "infra", "reliant"},
		},
		{
			name:      "declares nothing and has no control plane: no hub exists",
			mutate:    func(e *KCLEntities) { e.ControlPlane = nil },
			wantForge: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := prodShaped()
			tc.mutate(e)
			got := splitOf(t, e)
			if !reflect.DeepEqual(got.Hub, tc.wantHub) {
				t.Errorf("hub = %v, want %v", got.Hub, tc.wantHub)
			}
			if !reflect.DeepEqual(got.Forge, tc.wantForge) {
				t.Errorf("forge = %v, want %v", got.Forge, tc.wantForge)
			}
			if got.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

// A declaration is exactly what was written: an env that already works must not
// be handed to the hub because forge learned something new about it, and a
// declared list is never augmented.
func TestUnitSplit_DeclaredNeverAugmentedByInference(t *testing.T) {
	e := prodShaped() // fully bound, nothing hosted: the inference would say "all hub"
	e.HubConverges = []string{"reliant"}
	if s := splitOf(t, e); s.IsHub("infra") || s.IsHub("base") {
		t.Errorf("inference leaked past a declaration: %s", s.Describe())
	}
}

func TestUnitSplit_RefusesWhatTheHubCannotConverge(t *testing.T) {
	t.Run("no bound cluster", func(t *testing.T) {
		e := prodShaped()
		e.ClusterTarget, e.Workloads = &ClusterTargetEntity{}, nil
		e.HubConverges = []string{"reliant"}
		_, err := unitSplitOf(e)
		if err == nil || !strings.Contains(err.Error(), "connected cluster") {
			t.Fatalf("a unit handed to a hub with no cluster to converge onto must be refused, got %v", err)
		}
	})
	t.Run("hosted tier beside a hub unit", func(t *testing.T) {
		e := prodShaped()
		e.Databases = []DatabaseEntity{{Name: "d", Runtime: RuntimeHosted}}
		e.HubConverges = []string{"reliant"}
		_, err := unitSplitOf(e)
		if err == nil || !strings.Contains(err.Error(), "hosted") {
			t.Fatalf("a hosted tier cannot share an env with a hub-converged unit, got %v", err)
		}
	})
}

func TestUnitSplit_ShapeUnits(t *testing.T) {
	e := prodShaped()
	e.HubConverges = []string{"reliant"}
	got := splitOf(t, e).ShapeUnits()
	want := []release.ShapeUnit{
		{Name: "base", Applier: release.UnitApplierForge},
		{Name: "infra", Applier: release.UnitApplierForge},
		{Name: "reliant", Applier: release.UnitApplierHub},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shape units = %+v, want %+v", got, want)
	}
	// An env that declares no split records none, so every shape written before
	// the field encodes and hashes as it did.
	if (unitSplit{}).ShapeUnits() != nil {
		t.Error("an undeclared split recorded units")
	}
	e2 := prodShaped()
	e2.ControlPlane = nil
	if splitOf(t, e2).ShapeUnits() != nil {
		t.Error("an env with no control plane recorded units")
	}
}

// What the control plane is TOLD. The env-level flag is deliberately
// conservative: a control plane that predates units reads only it, so true here
// would hand it units forge still applies.
func TestHubConvergedWire(t *testing.T) {
	cases := []struct {
		name      string
		hub       []string
		forge     []string
		wantFlag  bool
		wantUnits []string
	}{
		{"nothing moved", nil, []string{"base", "infra"}, false, []string{}},
		{"some moved", []string{"reliant"}, []string{"base"}, false, []string{"reliant"}},
		{"all moved", []string{"base", "reliant"}, nil, true, []string{"base", "reliant"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flag, units := hubConvergedWire(unitSplit{Hub: tc.hub, Forge: tc.forge})
			if *flag != tc.wantFlag {
				t.Errorf("hubConverged = %v, want %v", *flag, tc.wantFlag)
			}
			if !reflect.DeepEqual(*units, tc.wantUnits) {
				t.Errorf("hubConvergedUnits = %v, want %v", *units, tc.wantUnits)
			}
		})
	}
}

// LEDGER TABLE. How the split decides what the follow-through does: who plans,
// what is waited on, whether the deploy has anything to apply from a machine.
func TestEnvLedger_Table(t *testing.T) {
	cases := []struct {
		name           string
		mutate         func(*KCLEntities)
		wantMixed      bool
		wantLedgerOnly bool
		wantHubUnits   bool
		wantForgePlans bool
	}{
		{
			name:           "nothing moved: ledger-only, forge plans and applies it all",
			mutate:         func(e *KCLEntities) { e.HubConverges = []string{} },
			wantMixed:      true,
			wantLedgerOnly: true,
			wantForgePlans: true,
		},
		{
			name:           "one unit moved: both halves, forge still plans (the hub sees only its unit)",
			mutate:         func(e *KCLEntities) { e.HubConverges = []string{"reliant"} },
			wantMixed:      true,
			wantHubUnits:   true,
			wantForgePlans: true,
		},
		{
			name:         "every unit moved, no frontend: nothing to apply, the hub plans",
			mutate:       func(e *KCLEntities) { e.HubConverges = []string{"base", "infra", "reliant"} },
			wantHubUnits: true,
		},
		{
			name: "every unit moved, forge still ships a frontend: it applies locally, the hub still plans",
			mutate: func(e *KCLEntities) {
				withFrontend(e)
				e.HubConverges = []string{"base", "infra", "reliant"}
			},
			wantMixed:    true,
			wantHubUnits: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := prodShaped()
			tc.mutate(e)
			units := splitOf(t, e)
			l := envLedger{Hosted: true, Units: units, Mixed: forgeAppliesLocally(e, units), HostedTiers: hostedTiersOf(e)}
			if l.Mixed != tc.wantMixed {
				t.Errorf("Mixed = %v, want %v", l.Mixed, tc.wantMixed)
			}
			if l.ledgerOnly() != tc.wantLedgerOnly {
				t.Errorf("ledgerOnly = %v, want %v", l.ledgerOnly(), tc.wantLedgerOnly)
			}
			if l.hubUnits() != tc.wantHubUnits {
				t.Errorf("hubUnits = %v, want %v", l.hubUnits(), tc.wantHubUnits)
			}
			if l.forgePlans() != tc.wantForgePlans {
				t.Errorf("forgePlans = %v, want %v", l.forgePlans(), tc.wantForgePlans)
			}
		})
	}
}

// What forge applies from a machine when units have moved: the work outside any
// unit is always forge's.
func TestForgeAppliesLocally_WorkOutsideAUnitIsAlwaysForges(t *testing.T) {
	allHub := func(e *KCLEntities) unitSplit {
		e.HubConverges = []string{"base", "infra", "reliant"}
		return splitOf(t, e)
	}
	t.Run("plain", func(t *testing.T) {
		e := prodShaped()
		if forgeAppliesLocally(e, allHub(e)) {
			t.Error("a fully hub-converged env has nothing for forge to apply")
		}
	})
	for name, mutate := range map[string]func(*KCLEntities){
		"a frontend forge ships": func(e *KCLEntities) { withFrontend(e) },
		"a helm chart":           func(e *KCLEntities) { e.HelmCharts = []HelmChartEntity{{Name: "cert-manager"}} },
		"a compose workload": func(e *KCLEntities) {
			e.Workloads = append(e.Workloads, WorkloadEntity{Name: "c", Runtime: RuntimeEntity{Type: RuntimeCompose}})
		},
		"host infra":               func(e *KCLEntities) { e.Infra = []HostInfraEntity{{Name: "postgres"}} },
		"a Secret forge renders":   func(e *KCLEntities) { e.RenderedSecrets = []RenderedSecretEntity{{Name: "s"}} },
		"a kubeconfig forge mints": func(e *KCLEntities) { e.KubeconfigSecrets = []KubeconfigSecretEntity{{Name: "k"}} },
	} {
		t.Run(name, func(t *testing.T) {
			e := prodShaped()
			mutate(e)
			if e.Applier == "" {
				e.HubConverges = []string{"base", "infra", "reliant"}
			}
			// host infra and compose cannot be hub-converged; the point is
			// only that they count as forge's work, so judge the split by hand.
			split := unitSplit{Hub: []string{"base", "infra", "reliant"}, Source: unitSplitDeclared}
			if !forgeAppliesLocally(e, split) {
				t.Errorf("%s is outside every unit, so forge applies it whatever the split says", name)
			}
		})
	}
}

func TestEnvLedger_ShapeErrorNamesTheUnits(t *testing.T) {
	err := offlineShapeError("prod", "https://cp.example", unitSplit{Hub: []string{"reliant", "infra"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "reliant, infra") {
		t.Fatalf("--offline-ledger must name the units the hub converges, got %v", err)
	}
}

// The direct-apply allowance follows the declaration, not the env's name: an env
// mid-move is allowed, an env that declared nothing is not.
func TestDirectApplyAllowed_HubConvergesIsADeclaration(t *testing.T) {
	e := prodShaped()
	if DirectApplyAllowed(e) {
		t.Fatal("control: an undeclared real env must not be direct-appliable")
	}
	e.HubConverges = []string{"reliant"}
	if !DirectApplyAllowed(e) {
		t.Error("an env that declares which units the hub converges was not exempted from the planned refusal")
	}
}

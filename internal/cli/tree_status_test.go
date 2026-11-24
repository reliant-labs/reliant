package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// The paths a reconciler reports a tree under are the bundle's own.
var (
	reliantRollout = release.BundleTreePath("prod", "reliant", release.PhaseRollout)
	infraRollout   = release.BundleTreePath("prod", "infra", release.PhaseRollout)
)

const (
	boundDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	oldDigest   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// treeShape is a bound bundle's shape: objects in two units on a v2 cluster,
// one on a flat cluster (which has no trees), and one with no cluster.
func treeShape() release.Shape {
	return release.Shape{Objects: []release.ShapeObject{
		{Cluster: "prod", Kind: "Namespace", Name: "a", Tree: "reliant/pre"},
		{Cluster: "prod", Kind: "Deployment", Name: "api", Tree: "reliant/rollout"},
		{Cluster: "prod", Kind: "Deployment", Name: "api2", Tree: "reliant/rollout"}, // same tree: counted once
		{Cluster: "prod", Kind: "Deployment", Name: "nats", Tree: "infra/rollout"},
		{Cluster: "daemon", Kind: "DaemonSet", Name: "kata", Tree: "infra/rollout"},
		{Cluster: release.BundleHostedCluster, Kind: "Workload", Name: "w"}, // flat: no tree
		{Kind: "ConfigMap", Name: "orphan"},                                 // no cluster
	}}
}

func TestTreeInventory(t *testing.T) {
	var got []string
	for _, tr := range treeInventory(treeShape()) {
		got = append(got, tr.Path)
	}
	want := []string{"manifests/daemon/infra/rollout", "manifests/prod/infra/rollout", "manifests/prod/reliant/pre", "manifests/prod/reliant/rollout"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("trees = %v, want %v (the bundle's own tree paths, one per cluster x unit x phase, sorted; flat clusters have none)", got, want)
	}
	if len(treeInventory(release.Shape{})) != 0 {
		t.Error("an empty shape has trees")
	}
}

func judged(t *testing.T, split unitSplit, bound string, readings []treeReading) map[string]envStatusTree {
	t.Helper()
	out := map[string]envStatusTree{}
	for _, tr := range judgeTrees(treeInventory(treeShape()), split, bound, readings) {
		out[tr.Path] = tr
	}
	return out
}

var reliantMoved = unitSplit{Hub: []string{"reliant"}, Forge: []string{"base", "infra"}, Source: unitSplitDeclared}

// THE VERDICTS. Promotion is INTENT and convergence is OBSERVED: a tree bound to
// the new release and live at the old one is BEHIND, and that is the
// information. The binding is never moved late to make the two agree.
func TestJudgeTrees_BoundVsLive(t *testing.T) {
	cases := []struct {
		name     string
		readings []treeReading
		want     string
	}{
		{"live at the bound revision", []treeReading{{Cluster: "prod", Tree: reliantRollout, State: "converged", Revision: boundDigest}}, treeConverged},
		{"live at an older revision, nothing failing", []treeReading{{Cluster: "prod", Tree: reliantRollout, State: "converged", Revision: oldDigest}}, treeBehind},
		{"the reconciler says failed", []treeReading{{Cluster: "prod", Tree: reliantRollout, State: "failed", Reason: "HealthCheckFailed", Message: "api not ready"}}, treeFailed},
		{"nothing reported", nil, treeUnobserved},
		{"a state it does not know", []treeReading{{Cluster: "prod", Tree: reliantRollout, State: "progressing"}}, treeUnobserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := judged(t, reliantMoved, boundDigest, tc.readings)[reliantRollout]
			if got.Status != tc.want {
				t.Errorf("status = %q, want %q (%+v)", got.Status, tc.want, got)
			}
			if got.Bound != boundDigest {
				t.Errorf("bound = %q, want the bound bundle's digest", got.Bound)
			}
		})
	}
}

// A unit forge applies has no observer. Showing another tree's reading against
// it would invent an agreement nothing checked.
func TestJudgeTrees_ForgeAppliedTreesTakeNoReading(t *testing.T) {
	// A cluster-level reading for prod, converged at the bound digest.
	readings := []treeReading{{Cluster: "prod", State: "converged", Revision: boundDigest}}
	got := judged(t, reliantMoved, boundDigest, readings)
	infra := got[infraRollout]
	if infra.Status != treeForgeApplied || infra.Live != "" || infra.Applier != release.UnitApplierForge {
		t.Errorf("a forge-applied tree took the cluster's reading: %+v", infra)
	}
	if reliant := got[reliantRollout]; reliant.Status != treeConverged || reliant.Applier != release.UnitApplierHub {
		t.Errorf("the hub's tree did not take its cluster's reading: %+v", reliant)
	}
}

// DEGRADES GRACEFULLY. A control plane that predates per-tree reports gives one
// reading per cluster; it is used for the cluster's hub trees and labelled so,
// and a reading for the tree itself always wins over it.
func TestJudgeTrees_DegradesToClusterLevelReadings(t *testing.T) {
	cluster := treeReading{Cluster: "prod", State: "converged", Revision: oldDigest}
	got := judged(t, reliantMoved, boundDigest, []treeReading{cluster})[reliantRollout]
	if got.Status != treeBehind || got.Source != "cluster" {
		t.Errorf("a cluster-level reading must be used and labelled as one: %+v", got)
	}
	perTree := treeReading{Cluster: "prod", Tree: reliantRollout, State: "converged", Revision: boundDigest}
	got = judged(t, reliantMoved, boundDigest, []treeReading{cluster, perTree})[reliantRollout]
	if got.Status != treeConverged || got.Source != "tree" {
		t.Errorf("a reading for the tree itself must beat its cluster's: %+v", got)
	}
	if none := judged(t, reliantMoved, boundDigest, nil)[reliantRollout]; none.Source != "" || none.Live != "" {
		t.Errorf("nothing reported must claim no source and no live revision: %+v", none)
	}
}

func TestWriteTreeTable(t *testing.T) {
	trees := judgeTrees(treeInventory(treeShape()), reliantMoved, boundDigest, []treeReading{
		{Cluster: "prod", State: "converged", Revision: oldDigest},
	})
	var b bytes.Buffer
	writeTreeTable(&b, trees)
	out := b.String()
	for _, want := range []string{
		"bound is the promotion's intent; live is what the hub's reconciler reports",
		"prod/reliant/rollout",
		"BEHIND",
		"FORGE-APPLIED",
		"applied by forge from a machine; no reconciler observes it",
		"reading is for the tree's cluster as a whole",
		"reports convergence per cluster",
		"bbbbbbbbbbbb", // bound, shortened
		"aaaaaaaaaaaa", // live, shortened
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table lacks %q:\n%s", want, out)
		}
	}
	var empty bytes.Buffer
	writeTreeTable(&empty, nil)
	if empty.Len() != 0 {
		t.Errorf("an env with no trees printed %q", empty.String())
	}
}

// The failing tree is named with the reconciler's own words, not a paraphrase.
func TestWriteTreeTable_FailureCarriesTheReconcilersWords(t *testing.T) {
	trees := judgeTrees(treeInventory(treeShape()), reliantMoved, boundDigest, []treeReading{
		{Cluster: "prod", Tree: reliantRollout, State: "failed", Reason: "HealthCheckFailed", Message: "deployment api not ready"},
	})
	var b bytes.Buffer
	writeTreeTable(&b, trees)
	if !strings.Contains(b.String(), "HealthCheckFailed: deployment api not ready") {
		t.Errorf("the failure lost the reconciler's words:\n%s", b.String())
	}
}

// A reconciler's revision ("latest@sha256:…") is compared by its digest.
func TestRevisionDigestOf(t *testing.T) {
	for in, want := range map[string]string{
		"latest@" + oldDigest: oldDigest,
		oldDigest:             oldDigest,
		"":                    "",
		"main":                "main",
	} {
		if got := revisionDigestOf(in); got != want {
			t.Errorf("revisionDigestOf(%q) = %q, want %q", in, got, want)
		}
	}
}

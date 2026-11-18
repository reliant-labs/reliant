package cluster

import (
	"context"
	"fmt"
)

// The component-owner stamp and the single-owner check for platform
// components that own cluster-scoped singletons (today: the External Secrets
// Operator, whose CRDs are cluster-wide).
//
// Two installs on one cluster fight over those CRDs: whichever applied last
// owns the served versions and the conversion settings, and the other's
// controller reconciles against a schema it did not ship. KCL refuses a second
// declaration within one Bundle; this is the half that sees ACROSS bundles,
// by reading the live CRD.
//
// Detection is by kind and owner label, never by name, so a differently named
// HelmChart still collides. An existing CRD with NO owner label was not
// installed by forge (a hand-run helm install, say): it is adopted, because
// refusing would make a first forge install impossible on a cluster that
// already runs ESO. An existing CRD labelled for ANOTHER owner is refused.

const (
	componentOwnerLabel = "forge.dev/component-owner"
	// esoOwnerProbeCRD is the CRD whose owner label decides the cluster's
	// External Secrets Operator owner.
	esoOwnerProbeCRD = "externalsecrets.external-secrets.io"
)

// ComponentOwnerError is a refusal to install a component another owner holds.
type ComponentOwnerError struct {
	Component, Cluster, Holder, Wanted string
}

func (e *ComponentOwnerError) Error() string {
	return fmt.Sprintf("%s on cluster %q is owned by %q; this env (%q) may not install a second one.\n"+
		"One %s per cluster: its CRDs are cluster-scoped, so two installs fight over them.\n"+
		"fix: remove the declaration from this env and let %q's bundle own the cluster, or move the owner's "+
		"declaration here after removing it there", e.Component, e.Cluster, e.Holder, e.Wanted, e.Component, e.Holder)
}

// checkComponentOwner refuses a chart that installs a single-owner component
// when the live cluster says another owner holds it. A chart that is not such
// a component, an unowned spec, and a cluster with no CRD yet all pass.
func checkComponentOwner(ctx context.Context, kctx string, spec HelmChartSpec) error {
	if spec.CRDBundle != "external-secrets" || spec.Owner == "" {
		return nil
	}
	o, err := getChartObject(ctx, kctx, inventoryEntry{Group: "apiextensions.k8s.io", Kind: "CustomResourceDefinition", Name: esoOwnerProbeCRD})
	if err != nil {
		return fmt.Errorf("check External Secrets Operator owner on %q: %w", kctx, err)
	}
	if o == nil {
		return nil
	}
	holder := o.Metadata.Labels[componentOwnerLabel]
	if holder == "" || holder == spec.Owner {
		return nil
	}
	return &ComponentOwnerError{Component: "External Secrets Operator", Cluster: kctx, Holder: holder, Wanted: spec.Owner}
}

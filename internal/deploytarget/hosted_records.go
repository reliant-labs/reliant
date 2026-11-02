package deploytarget

import (
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// HostedRecord is one hosted tier declaration as it rides a bundle: the
// forge.dev CR the control plane applies, WITHOUT identity.
//
// No namespace and no org/environment/deployment labels: forge cannot know
// them, and identity is metadata the platform stamps, never something a spec
// author (or a bundle) can claim. The control plane's Kustomization sets the
// namespace and stamps the labels.
type HostedRecord struct {
	Kind string
	Name string
	// YAML is the serialized CR.
	YAML []byte
}

// HostedRecords plans the hosted group exactly as a publish would — pin,
// validate under ProfileRestricted, band-check, render the set — and returns
// the CRs, so what a bundle carries is what the platform admits.
func HostedRecords(group ServiceGroup) ([]HostedRecord, error) {
	plan, err := planHosted(group)
	if err != nil {
		return nil, err
	}
	return hostedRecordsOf(plan)
}

func hostedRecordsOf(plan []hostedPlanItem) ([]HostedRecord, error) {
	out := make([]HostedRecord, 0, len(plan))
	for _, item := range plan {
		tm := metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String()}
		om := metav1.ObjectMeta{Name: item.Name, Labels: map[string]string{"app.kubernetes.io/name": item.Name}}
		var doc any
		switch spec := item.Spec.(type) {
		case v1alpha1.WorkloadSpec:
			tm.Kind = "Workload"
			doc = v1alpha1.Workload{TypeMeta: tm, ObjectMeta: om, Spec: spec}
		case v1alpha1.StaticSiteSpec:
			tm.Kind = "StaticSite"
			doc = v1alpha1.StaticSite{TypeMeta: tm, ObjectMeta: om, Spec: spec}
		case v1alpha1.ManagedDatabaseSpec:
			tm.Kind = "ManagedDatabase"
			doc = v1alpha1.ManagedDatabase{TypeMeta: tm, ObjectMeta: om, Spec: spec}
		default:
			return nil, fmt.Errorf("%s: hosted plan item carries a %T spec", item.Name, item.Spec)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.Name, err)
		}
		// A declaration carries no status: it is the controller's to write,
		// and an empty one in a sealed bundle is noise that Flux would apply.
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, fmt.Errorf("%s: %w", item.Name, err)
		}
		delete(generic, "status")
		if raw, err = json.Marshal(generic); err != nil {
			return nil, fmt.Errorf("%s: %w", item.Name, err)
		}
		y, err := yaml.JSONToYAML(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", item.Name, err)
		}
		out = append(out, HostedRecord{Kind: tm.Kind, Name: item.Name, YAML: y})
	}
	return out, nil
}

// PreflightHostedRecords is HostedRecords over placeholder digests, for a
// render that has no release yet. The result is for DISPLAY and shape
// projection only: its digests are obviously fake and a bundle must never
// seal it (callers refuse to).
func PreflightHostedRecords(group ServiceGroup) ([]HostedRecord, error) {
	plan, err := preflightHostedPlan(group)
	if err != nil {
		return nil, err
	}
	return hostedRecordsOf(plan)
}

// HostedArtifactKey is the release artifact a hosted service's digest is bound
// under — the key the plan looks a pin up by. Exported so a caller that BINDS
// a release (or asserts on one) uses the plan's own rule, never a copy of it.
func HostedArtifactKey(svc ResolvedService) string {
	if svc.Hosted == nil {
		return ""
	}
	return hostedArtifactOf(svc.Name, svc.Hosted)
}

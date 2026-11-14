package stsresize

import (
	_ "embed"
	"encoding/json"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Script is the converge procedure as a POSIX shell script that needs only
// kubectl and jq, for a Job that runs INSIDE the cluster before the workloads
// roll out. [Converge] is the same procedure for a caller holding a cluster
// client (forge's direct apply); the Script is for an applier that holds none,
// a Flux-reconciled bundle, whose only way to act before a rollout is an
// object in an earlier tree.
//
// It reads RESIZE_NAMESPACE and RESIZE_DECLARED (a JSON array of [Declared])
// and exits:
//
//	0  converged, or nothing to do
//	2  REFUSED: a shrink, or a change other than a larger storage request.
//	   Permanent; the Job declares exit 2 a non-retriable failure
//	1  anything else (transient; the Job retries)
//
// Its decision is [Decide]'s, restated in jq so the Job needs no forge binary;
// script_test.go runs both over one table of cases so they cannot drift. Two
// differences are deliberate. The script has no server-side dry run of the
// whole StatefulSet (it holds only the declared immutable fields, not the
// manifest), so it checks serviceName, podManagementPolicy and the selector
// itself. And it holds no cluster-scoped permission, so it cannot read
// StorageClasses: instead it dry-run-patches every claim before it writes any,
// and the API server's own expansion admission refuses a claim whose class
// does not allow it.
//
//go:embed resize.sh
var Script string

// Declared is what the Script is told about one StatefulSet: the fields whose
// change it judges, and nothing else. It is the Job's input, so it is small
// and carries no pod template.
type Declared struct {
	Name                string                `json:"name"`
	ServiceName         string                `json:"serviceName,omitempty"`
	PodManagementPolicy string                `json:"podManagementPolicy,omitempty"`
	Selector            *metav1.LabelSelector `json:"selector,omitempty"`
	// VolumeClaimTemplates is in declaration order, which the live object
	// keeps.
	VolumeClaimTemplates []DeclaredTemplate `json:"volumeClaimTemplates"`
}

// DeclaredTemplate is one volumeClaimTemplate, without the status and the
// null creationTimestamp a typed claim would carry.
type DeclaredTemplate struct {
	Metadata DeclaredTemplateMeta             `json:"metadata"`
	Spec     corev1.PersistentVolumeClaimSpec `json:"spec"`
}

// DeclaredTemplateMeta is the part of a template's metadata the API server
// compares.
type DeclaredTemplateMeta struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// DeclaredOf extracts what the Script judges from a StatefulSet.
func DeclaredOf(sts *appsv1.StatefulSet) Declared {
	d := Declared{
		Name:                sts.Name,
		ServiceName:         sts.Spec.ServiceName,
		PodManagementPolicy: string(sts.Spec.PodManagementPolicy),
		Selector:            sts.Spec.Selector,
	}
	for _, t := range sts.Spec.VolumeClaimTemplates {
		d.VolumeClaimTemplates = append(d.VolumeClaimTemplates, DeclaredTemplate{
			Metadata: DeclaredTemplateMeta{Name: t.Name, Labels: t.Labels, Annotations: t.Annotations},
			Spec:     *t.Spec.DeepCopy(),
		})
	}
	return d
}

// DeclaredJSON is the Script's RESIZE_DECLARED value for a set of
// StatefulSets: a JSON array in the order given.
func DeclaredJSON(sets ...*appsv1.StatefulSet) (string, error) {
	declared := make([]Declared, 0, len(sets))
	for _, s := range sets {
		declared = append(declared, DeclaredOf(s))
	}
	b, err := json.Marshal(declared)
	if err != nil {
		return "", fmt.Errorf("stsresize: encode the declared volumes: %w", err)
	}
	return string(b), nil
}

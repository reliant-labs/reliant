// Package varsoperatorcr exercises the controller-runtime variant of the
// kubebuilder/controller-runtime exception in ExportedVarsAnalyzer. Newer
// kubebuilder scaffolds use `&scheme.Builder{...}` from
// sigs.k8s.io/controller-runtime/pkg/scheme instead of the classic
// `runtime.NewSchemeBuilder(...)`. Both forms must be exempt.
package varsoperatorcr

import (
	schema "varsoperatorcr/k8sschema"
	"varsoperatorcr/scheme"
)

// These three names are the kubebuilder convention. None should be flagged.
var (
	GroupVersion  = schema.GroupVersion{Group: "example.dev", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

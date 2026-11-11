// Package varsoperator exercises the kubebuilder/controller-runtime exception
// in ExportedVarsAnalyzer: operator API packages MUST expose three
// package-level vars (`GroupVersion`, `SchemeBuilder`, `AddToScheme`) verbatim
// because the k8s API machinery discovers them by name. Wrapping them in a
// getter would silently break operator scheme registration.
//
// This testdata package uses local stubs for `schema` and `runtime` to avoid
// pulling k8s.io into the linter's testdata module — the analyzer itself
// matches by selector name, not type identity.
package varsoperator

import (
	"varsoperator/runtime"
	"varsoperator/schema"
)

// These three names are the kubebuilder convention. None should be flagged.
var (
	GroupVersion  = schema.GroupVersion{Group: "example.dev", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

// A var that happens to share one of the names but with a non-conforming
// initializer (here, a string) MUST still be flagged — the exception only
// applies when the initializer matches the controller-runtime convention.
var GroupVersion2 = "not exempt" // want `exported package variable GroupVersion2 should be a method on a struct or a getter function`

func addKnownTypes(_ *runtime.Scheme) error { return nil }

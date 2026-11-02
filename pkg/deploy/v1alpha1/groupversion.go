package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group is the API group of every deploy-tier kind forge declares.
//
// forge.dev rather than control-plane's historical reliant.dev: the spec is
// forge's, and reliant.dev is a product name the self-hosted user has no
// relationship with. A self-hosted cluster that never saw the control plane
// should not carry a CR group named after it.
const Group = "forge.dev"

// Version is the API version of this package's types.
const Version = "v1alpha1"

// GroupVersion is forge.dev/v1alpha1.
var GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

// SchemeBuilder registers this package's kinds. The same shape
// controller-runtime's scheme.Builder produces, built on apimachinery's
// runtime.SchemeBuilder so this package pulls in no controller-runtime code.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme adds every kind in this package to a scheme.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&Workload{}, &WorkloadList{},
		&StaticSite{}, &StaticSiteList{},
		&ManagedDatabase{}, &ManagedDatabaseList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

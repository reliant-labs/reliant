// Package scheme is a stub matching the relevant subset of
// sigs.k8s.io/controller-runtime/pkg/scheme, used only by the analyzer
// testdata so we don't have to pull controller-runtime into the linter's
// testdata.
package scheme

import "varsoperatorcr/k8sschema"

type Builder struct {
	GroupVersion k8sschema.GroupVersion
}

func (b *Builder) AddToScheme(_ any) error { return nil }

func (b *Builder) Register(_ ...any) *Builder { return b }

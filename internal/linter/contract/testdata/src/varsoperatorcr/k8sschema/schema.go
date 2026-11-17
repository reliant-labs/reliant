// Package k8sschema is a stub matching the relevant subset of
// k8s.io/apimachinery/pkg/runtime/schema, used only by the analyzer
// testdata. Imported into varsoperatorcr as `schema` via aliased import.
package k8sschema

type GroupVersion struct {
	Group   string
	Version string
}

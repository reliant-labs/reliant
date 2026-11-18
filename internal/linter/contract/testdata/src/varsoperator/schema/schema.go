// Package schema is a stub matching the relevant subset of k8s.io's
// apimachinery/pkg/runtime/schema, used only by the analyzer testdata so we
// don't have to pull k8s.io into the linter's testdata.
package schema

type GroupVersion struct {
	Group   string
	Version string
}

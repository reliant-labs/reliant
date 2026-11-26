// Package runtime is a stub matching the relevant subset of k8s.io's
// apimachinery/pkg/runtime, used only by the analyzer testdata so we don't
// have to pull k8s.io into the linter's testdata.
package runtime

type Scheme struct{}

type SchemeBuilder []func(*Scheme) error

func NewSchemeBuilder(funcs ...func(*Scheme) error) *SchemeBuilder {
	sb := SchemeBuilder(funcs)
	return &sb
}

func (sb *SchemeBuilder) AddToScheme(s *Scheme) error {
	for _, f := range *sb {
		if err := f(s); err != nil {
			return err
		}
	}
	return nil
}

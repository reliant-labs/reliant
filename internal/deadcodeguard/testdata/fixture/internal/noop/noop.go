// Package noop plants one instance of every shape the noop-func rule must
// judge, and one of every shape it must leave alone.
package noop

import "errors"

// AppendServiceToConfig reproduces the real defect verbatim: a `return nil`
// whose caller computes a port, prints it to the user as fact, and hands it
// here.
//
// WANT noop-func.
func AppendServiceToConfig(projectRoot, serviceName string, port int) error {
	return nil
}

// LoadConfigKV is the same lie told with an empty container instead of nil.
//
// WANT noop-func.
func LoadConfigKV(projectDir, envName string) map[string]string {
	return map[string]string{}
}

// BranchingNoop takes every branch to the same zero answer.
//
// WANT noop-func.
func BranchingNoop(name string, n int) (int, error) {
	if name == "" {
		return 0, nil
	}
	return 0, nil
}

// OK: no parameters, so there is nothing for the body to ignore. This is the
// ordinary shape of a build-tag stub.
func Register() error { return nil }

// OK: the body does real work before returning a fixed nil.
func Render(name string) error {
	if name == "" {
		return errors.New("empty")
	}
	println(name)
	return nil
}

// Provider is the interface the null object below satisfies.
type Provider interface {
	Resolve(name string) (string, bool)
}

type nullProvider struct{}

// OK: a METHOD's signature is dictated by the interface it satisfies. The Null
// Object pattern is deliberate and must never be flagged.
func (nullProvider) Resolve(name string) (string, bool) { return "", false }

// New returns the null provider so the type is live.
func New() Provider { return nullProvider{} }

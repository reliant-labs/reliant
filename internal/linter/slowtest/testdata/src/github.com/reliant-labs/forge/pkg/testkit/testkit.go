// Package testkit is a stand-in for forge/pkg/testkit's slow-test surface.
package testkit

import "testing"

type SlowOption func(*[]string)

func Inputs(globs ...string) SlowOption {
	return func(c *[]string) { *c = append(*c, globs...) }
}

func Slow(t testing.TB, why string, opts ...SlowOption) { // want Slow:"marks slow"
	if testing.Short() {
		t.Skip(why)
	}
}

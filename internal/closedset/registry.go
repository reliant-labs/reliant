// Package closedset keeps a database CHECK constraint's value list equal to
// the Go set it mirrors.
//
// Some vocabularies are enforced twice: in Go, where a value that cannot be
// spelled cannot be granted (accesstoken.AllScopes), and in postgres, where a
// CHECK decides what a row may hold at all. The two must not drift. When Go
// gains a value the CHECK lacks, every write of it fails the constraint with
// an opaque error; when the CHECK admits one Go lacks, a row becomes
// unreadable. Every project that stores such a set used to pin the two with a
// test of its own, and a test that needs a database is skipped on the -short
// tier — which is how a forge bump that added two scopes reached a project's
// main green and turned it red there.
//
// So the CHECK DECLARES the set it mirrors, in the schema:
//
//	COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
//	    'forge:closed-set accesstoken.AllScopes';
//
// and `forge lint` (the closed-sets lane, on by default) reads the CHECK the
// migrations last defined, compares its value list with the named Go set,
// and on a mismatch names the missing and extra values and prints the
// forward migration that fixes it. It reads migration text, not a database,
// so it runs everywhere lint does.
//
// A set name resolves one of two ways, and never by reflection over code
// forge has not vetted:
//
//   - a short name (`accesstoken.AllScopes`) is a set forge exports, looked up
//     in the registry below. It is compiled into the forge binary, which is
//     the pinned forge (forge refuses to run at a version the project does not
//     pin), so the lint compares against exactly the set the project builds
//     against.
//   - a qualified name (`github.com/acme/app/internal/billing.AllTiers`) is a
//     set the PROJECT owns: a package-level variable whose initializer is a
//     composite literal of constants. forge type-checks that package and
//     reads the constants' values (ResolveGo); it runs none of the project's
//     code.
package closedset

import (
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/accesstoken"
)

// Set is one closed set forge exports.
type Set struct {
	// Name is how a forge:closed-set declaration names it: the Go package
	// name and the variable, `accesstoken.AllScopes`.
	Name string
	// Values is the set's members, in the Go order — the order a fix
	// migration lists them in.
	Values []string
}

// registry is every closed set forge exports. A set belongs here when forge
// owns the Go half of a vocabulary that projects also store behind a CHECK:
// growing it then reaches every project at its next forge bump, and the
// closed-sets lane is what tells them their CHECK must follow.
func registry() []Set {
	return []Set{
		{Name: "accesstoken.AllScopes", Values: stringsOf(accesstoken.AllScopes)},
		{Name: "accesstoken.AllResourceKinds", Values: stringsOf(accesstoken.AllResourceKinds)},
	}
}

// Lookup returns the forge-exported set called name.
func Lookup(name string) (Set, bool) {
	for _, s := range registry() {
		if s.Name == name {
			return s, true
		}
	}
	return Set{}, false
}

// Names returns every forge-exported set's name, sorted.
func Names() []string {
	var out []string
	for _, s := range registry() {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// IsQualified reports whether name is a project set's qualified Go name
// (`<import path>.<Var>`) rather than a forge-exported short name. An import
// path of a module always carries a `/`; a registry name never does.
func IsQualified(name string) bool { return strings.Contains(name, "/") }

func stringsOf[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

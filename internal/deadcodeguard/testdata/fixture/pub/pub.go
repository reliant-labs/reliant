// Package pub is the fixture module's public API: it re-exports an internal
// type by alias, the way forge's cli package re-exports internal/cli's.
package pub

import "deadcodeguardfixture/internal/phantom"

// Options is phantom.Options, for the module's consumers.
type Options = phantom.Options

// Dial is phantom.Dial.
func Dial(o Options) string { return phantom.Dial(o) }

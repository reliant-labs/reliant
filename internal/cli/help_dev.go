// Package cli — hideDevFlags, the user-vs-maintainer CLI surface split.
//
// Commands like `forge lint` and `forge generate` accumulate flags for
// two very different audiences: project users (the 5-7 flags they
// actually reach for) and forge maintainers / debugging agents (wiring
// audits, pipeline narrowing, migration escape hatches). Showing all of
// them in --help buries the user-facing surface. hideDevFlags marks the
// maintainer set Hidden — fully functional, just invisible in --help —
// and registers a visible --help-dev flag that lists exactly the hidden
// set, so the flags stay discoverable in one place.
//
// Rule of thumb when adding a new flag: if a project user (not a forge
// developer) would reach for it in a normal week, it stays visible;
// otherwise add it to the command's hideDevFlags call. The surface test
// in help_surface_test.go pins the visible set so every new flag must
// consciously pick a side.

package cli

import (
	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// hideDevFlags forwards to cmdutil.HideDevFlags — the shared
// user-vs-maintainer surface split now lives in the leaf package so the
// dir-nested lint group reaches it without importing internal/cli. The
// unexported alias keeps internal/cli's call sites (generate.go) unchanged.
func hideDevFlags(cmd *cobra.Command, names ...string) { cmdutil.HideDevFlags(cmd, names...) }

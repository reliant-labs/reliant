package cmdutil

import (
	"strings"

	"github.com/spf13/cobra"
)

// ConfiguresCloneAnnotation marks a command that writes the files forge's
// per-clone git setup exists for: generated files, and forge's committed
// state under .forge/. Only such a command (or a subcommand of one) may
// write the clone's git configuration as a side effect — the generated-file
// merge driver (.git/config, info/attributes) and core.hooksPath.
//
// Opt-in, not opt-out: most forge commands only read the project (env status,
// env render, env deploy --dry-run) or have nothing to do with it (slot run),
// and a command added tomorrow must not start rewriting .git/config because
// nobody remembered to exempt it. .git/config is shared by every linked
// worktree of the clone, so a write there is visible to, and races with,
// every agent working in any of them.
//
// The annotation's value is a comma-separated list of boolean flags that
// make a run of the command read-only (generate's --check and --plan).
const ConfiguresCloneAnnotation = "forge.configures-clone"

// ConfiguresClone marks cmd, and through it every subcommand, as one that
// may set up the clone's git configuration — unless one of readOnlyFlags is
// set on the run. It returns cmd for chaining.
func ConfiguresClone(cmd *cobra.Command, readOnlyFlags ...string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[ConfiguresCloneAnnotation] = strings.Join(readOnlyFlags, ",")
	return cmd
}

// ConfiguresCloneFor reports whether this run of cmd may set up the clone's
// git configuration: cmd or one of its parents carries
// ConfiguresCloneAnnotation, and none of the read-only flags it names is set.
func ConfiguresCloneFor(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		readOnly, ok := c.Annotations[ConfiguresCloneAnnotation]
		if !ok {
			continue
		}
		for _, name := range strings.Split(readOnly, ",") {
			if name == "" {
				continue
			}
			if f := cmd.Flags().Lookup(name); f != nil && f.Changed && f.Value.String() == "true" {
				return false
			}
		}
		return true
	}
	return false
}

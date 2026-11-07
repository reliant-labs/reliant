package cmdutil

import "github.com/spf13/cobra"

// AddPackageScaffoldFlags registers the flags every package-scaffolding verb
// shares (`forge package new`, `forge scaffold package`, `forge scaffold
// adapter`), so the three spellings cannot drift apart.
//
//   - --base-url opts an adapter into a config block with one base_url leaf,
//     bound to <NAME>_BASE_URL; --base-url-env names that env var instead
//     (and implies --base-url). An adapter talks to SOME downstream, not
//     necessarily one HTTP API with a base URL (a queue, a vendor SDK, a
//     storage gateway), so the leaf is asked for, never assumed.
//   - --dry-run prints what the scaffold would write and changes nothing.
func AddPackageScaffoldFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("base-url", false, "adapter only: compose a config block with a base_url leaf, bound to <NAME>_BASE_URL; omit for an adapter with no single base URL")
	cmd.Flags().String("base-url-env", "", "adapter only: the `ENV_VAR` the base_url leaf binds instead of <NAME>_BASE_URL (implies --base-url)")
	cmd.Flags().Bool("dry-run", false, "print the files and config edits the scaffold would make, and write nothing")
}

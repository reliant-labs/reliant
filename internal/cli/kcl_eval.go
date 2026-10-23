// File: internal/cli/kcl_eval.go
//
// `forge kcl eval <file> [-S <path>]…` — evaluate ONE KCL file of this
// project and print a field out of it.
//
// # Why this command exists
//
// forge #277 made the `forge` KCL module come from the forge binary, so that
// the binary rendering a project IS the module version it renders against.
// A consequence, and an intended one, is that `kcl run` on a project file
// that says `import forge` cannot work: there is no dependency to fetch and
// no copy on disk. Nothing but forge evaluates a forge project's KCL.
//
// That left a real gap. A project keeps plain declarative constants in
// library files — control-plane's deploy/kcl/lib/kata_pool.k declares the GKE
// node-pool inputs its build scripts need, lib/daemon_placement.k declares
// which cluster each env's daemon pods land in — and those files are read by
// scripts and tests, never deployed. `forge env render` evaluates an
// ENVIRONMENT into Kubernetes objects, which is not that. So there was no
// forge command for "evaluate this file, give me this field".
//
// What filled the gap went around forge, which is the thing that must not
// happen. control-plane grew scripts/lib/kcl-forge-module.sh: make forge
// materialize its module into FORGE_KCL_MODULE_CACHE, dig the directory out
// of the cache, then run `kcl run <file> -S <field> -E forge=<dir>` — forge's
// own render setup, reconstructed from outside, pinned to the cache layout
// and the module name, and needing `kcl` on PATH that forge itself has not
// needed since it embedded the runtime. The same reconstruction sat in a Go
// test (internal/operators/shared/daemon_placement_test.go). Every one of
// those is a forge defect that got worked around instead of reported, and the
// fix is a forge command, not a better script.
//
// # Why `forge kcl eval` and not `forge env render --file`
//
// Because it is not a render of an environment, and every flag `env render`
// has says so. That command resolves an image tag from build state, applies
// digests, templates declared helm charts, and attributes each object to the
// cluster `forge env deploy` would apply it to — `--tag`, `--cluster`,
// `--kind`, `--target`, `--no-digest`, `--no-charts` are all meaningless for
// a library file that declares seven strings. Hanging `--file` off it would
// put the whole env surface in front of callers who want one constant, and
// would have to answer what `--cluster` means for a file with no workloads.
//
// A `kcl` group also says the true thing about scope: the unit is a KCL file,
// and the group is where a future `forge kcl fmt` or `forge kcl options`
// belongs. `env render` remains the way to evaluate an environment.
//
// # Read-only
//
// The evaluation claims nothing. The read-only halves of the render context
// are armed (see armReadOnlyKCLContext): fp.allocate_port resolves keys to
// blocks they already hold, fp.resolve_port reads its store without writing
// it, and fp.write_file is disarmed. A read-only command never claims a port
// block (#272). KCL's own `file.write` is not forge's to suppress — see
// `env render`'s note on the same subject — so a file that generates a file
// on evaluation still does, and that is reported on stderr.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/kcleval"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

func newKCLCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kcl",
		Short: "Evaluate this project's KCL directly",
		Long: `Work with this project's KCL files directly.

A forge project's KCL resolves ` + "`import forge`" + ` from the forge binary, not from a
kcl.mod dependency or a copy on disk, so a plain ` + "`kcl run`" + ` on a project file
CANNOT evaluate it. That is by design: the binary is the module version. These
subcommands are how anything outside forge reads a value out of project KCL.`,
	}
	cmd.AddCommand(newKCLEvalCmd())
	// Without this, cobra accepts `forge kcl <typo>` and exits 0 — a script
	// whose command name rotted would silently succeed and read an empty value.
	return cmdutil.StrictGroup(cmd)
}

func newKCLEvalCmd() *cobra.Command {
	var (
		selectors []string
		format    string
		options   []string
	)

	cmd := &cobra.Command{
		Use:   "eval <file> [-S <path>]...",
		Short: "Evaluate one KCL file of this project and print a selected field",
		Args:  cobra.ExactArgs(1),
		Long: `Evaluate a single .k file in this project and print a field out of it.

This is THE way a script or a test reads a value from project KCL. A plain
` + "`kcl run`" + ` cannot do it: since the forge KCL module is supplied by the forge
binary, ` + "`import forge`" + ` resolves only inside a forge evaluation. Nothing needs
` + "`kcl`" + ` on PATH, and nothing needs to know where forge keeps its module.

The file is evaluated with its OWN kcl.mod package root as the working
directory, so a relative import (` + "`import lib.foo`" + `) resolves exactly as it does
for a shell script that cd'd there first. It does not matter which directory
you run this from: the answer is the same from the project root and from a
subdirectory.

-S takes a dotted path, kcl-style. A numeric segment indexes a list
(` + "`pools.0.name`" + `). Repeat -S to get an object keyed by selector. Unlike
` + "`kcl run -S`" + `, a selected LIST comes back as one list rather than as one YAML
document per element, and a path that does not exist is an ERROR naming what
the document does have — not an empty result that a caller reads as a value.

--format raw prints a scalar's own text: no quotes, no trailing newline. It is
what makes ` + "`$(...)`" + ` capture the value exactly, replacing the
` + "`| tr -d \"\\n'\\\"\"`" + ` pipelines that a YAML scalar's conditional quoting forces
(and that silently corrupt a value legitimately containing a quote). It
refuses a list or an object rather than printing JSON a caller would
interpolate without noticing.

-D binds a top-level ` + "`option()`" + `, as it does for any KCL evaluation.

The evaluation is READ-ONLY as far as forge is concerned: no port block is
claimed, no port store is written, no cluster is contacted, and no image is
built. It is not guaranteed PURE, for the same reason ` + "`env render`" + ` is not:
KCL evaluates ` + "`file.write`" + ` itself, so a file that generates a file writes it.

To evaluate an ENVIRONMENT into its Kubernetes objects, use
` + "`" + Name() + " env render <env>`" + ` instead.

Examples:
  ` + Name() + ` kcl eval deploy/kcl/lib/kata_pool.k -S sbd.image_family --format raw
  ` + Name() + ` kcl eval deploy/kcl/lib/kata_pool.k -S sbd.disk_size_gb --format raw
  ` + Name() + ` kcl eval deploy/kcl/lib/daemon_placement.k -S daemon_placement | jq -r '.prod.context'
  ` + Name() + ` kcl eval deploy/kcl/lib/platform_local.k -S cloudnative_pg.name -S cloudnative_pg.namespace
  ` + Name() + ` kcl eval deploy/kcl/lib/kata_pool.k                       # the whole document`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKCLEval(cmd, args[0], selectors, format, options)
		},
	}

	cmd.Flags().StringArrayVarP(&selectors, "select", "S", nil,
		"Print only this dotted field path (repeatable; repeated paths yield an object keyed by path)")
	cmd.Flags().StringVar(&format, "format", string(kcleval.FormatJSON),
		fmt.Sprintf("Output format: %v (raw prints a scalar's own text, unquoted and with no trailing newline)", kcleval.Formats()))
	cmd.Flags().StringArrayVarP(&options, "option", "D", nil,
		"Bind a top-level KCL option, key=value (repeatable)")

	return cmd
}

// runKCLEval evaluates the file and writes the selection to stdout.
//
// STDOUT CARRIES ONLY THE VALUE. Every diagnostic — the devstack banner, kpm's
// progress, a declined write — goes to stderr, because the documented use is
// `V="$(forge kcl eval … --format raw)"` and one stray line of prose there
// becomes part of V. `env render` learned this the hard way when a `Note:` on
// stdout became the first YAML document of a stream piped to kubectl.
func runKCLEval(cmd *cobra.Command, file string, selectors []string, format string, options []string) error {
	projectDir, err := evalProjectRoot()
	if err != nil {
		return err
	}

	armReadOnlyKCLContext(projectDir)

	res, err := kcleval.Eval(kcleval.Request{
		ProjectDir: projectDir,
		File:       file,
		Selectors:  selectors,
		Options:    options,
	})
	if err != nil {
		// A selector miss is the user's typo, not a forge failure, and
		// cobra's usage dump would bury the message that names the fields
		// the document actually has.
		if errors.Is(err, kcleval.ErrNoSuchField) {
			cmd.SilenceUsage = true
		}
		return err
	}

	out, err := kcleval.Render(res, kcleval.Format(format))
	if err != nil {
		return err
	}
	if _, err := cmd.OutOrStdout().Write(out); err != nil {
		return err
	}
	reportDeclinedWrites(cmd.ErrOrStderr(), kclplugin.SuppressedWrites())
	return nil
}

// evalProjectRoot locates the project by WALKING UP from the resolution root,
// rather than requiring a forge.yaml in the exact cwd as most commands do.
//
// The difference matters because of who calls this. A Go test runs in its own
// package directory — `internal/operators/shared`, never the project root —
// and the pkg/kcleval helper exists precisely so such a test can read a
// declared value. Requiring an exact-cwd forge.yaml meant the helper failed
// with "forge.yaml not found in current directory" for every caller that did
// not pass an explicit -C, which is the common case and the one that most
// looks like the tool is broken. Shell scripts hit the same edge: the ones this
// replaces run from wherever the caller invoked them.
//
// Walking up is safe HERE in a way it would not be for a mutating command,
// because the file argument is resolved against the discovered root and
// refused if it escapes it: the worst a wrong ancestor can do is fail to
// contain the requested file. `forge -C <dir>` still wins outright —
// ResolutionRoot honours it — so a caller that needs to name the project
// explicitly can.
func evalProjectRoot() (string, error) {
	dir, err := cmdutil.FindProjectRoot()
	if err != nil {
		return "", err
	}
	if dir == "" {
		// Reuse the exact-cwd resolver's message rather than writing a second
		// one: it already names both fixes, and two spellings of "no project
		// here" would drift.
		return projectRoot()
	}
	return dir, nil
}

// armReadOnlyKCLContext arms the render-context globals a READ-ONLY
// evaluation may have: the ones that let fp.* builtins resolve, and none of
// the ones that write.
//
// It is deliberately not activateDevStack. That function probes whether the
// env runs on this machine (itself a render), consults forge.yaml's
// dev_stack ceiling, and arms the WRITING block allocator and port store —
// all of which are facts about an environment being brought up. This command
// has no environment: a library file is not an env, so there is no
// `ports-<env>.json` to read and no env whose blocks could be claimed.
//
// So each builtin gets its documented unarmed behaviour, which is the
// read-only one by construction: allocate_port resolves to its base port,
// resolve_port to a fresh availability-checked port that is never persisted,
// dev_stacks to empty, and write_file declines and records the path.
func armReadOnlyKCLContext(projectDir string) {
	kclplugin.UseFileWriter("")
	kclplugin.UseBlockAllocator(nil)
	kclplugin.UseDevStacks(func() ([]string, error) { return devstack.ListStacks(projectDir) })
}

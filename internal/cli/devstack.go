// Package cli — `forge env devstack` command tree.
//
// The parallel-dev-stack primitives (ADR 0003) live in internal/devstack:
// the raw git facts pushed into KCL as options, and the memoized
// forge.allocate_port(base, key) block allocator. Those primitives are
// resolved INSIDE a KCL render (under the up/deploy activation path). But a
// host launcher — a Taskfile target, a bootstrap script — needs the SAME
// allocated host port BEFORE `forge env up` renders the KCL, so it can start the
// host `reliant` process LISTENING on exactly the port the in-cluster
// controller will dial.
//
// `forge env devstack port <base>` is that single source of truth: it resolves
// the current worktree key (devstack.Worktree) and returns
// allocate_port(base, key) — base + block(key)*100 — through the SAME
// lock-guarded block registry (.forge/blocks.json) the KCL builtin uses, so
// the launcher and the render can never disagree on the port. On the PRIMARY
// checkout the key is "" ⇒ block 0 ⇒ the base is returned unchanged (no
// registry/lock touch), so the default dev loop is byte-identical to today.
package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/devstack"
)

// newDevStackCmd builds the `forge env devstack` parent command — the host-side
// surface of the parallel-dev-stack primitives. The KCL-side surface is the
// option("worktree")/option("branch") seam + the forge.allocate_port builtin;
// this command lets a launcher resolve the SAME values without a render.
func newDevStackCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devstack",
		Short: "Parallel-dev-stack host helpers (worktree key + port allocation)",
		Long: `Host-side helpers for forge's parallel-dev-stack primitives (ADR 0003).

A launcher (Taskfile target, bootstrap script) that starts a host process
BEFORE 'forge env up' renders the KCL needs the SAME host port the render will
allocate. 'forge env devstack port' resolves it through the same lock-guarded
block registry (.forge/blocks.json) the forge.allocate_port KCL builtin uses,
so the launcher and the render can never drift.

On the PRIMARY checkout the worktree key is "" so every port is returned
unchanged (block 0) — the default dev loop is byte-identical to today. A
linked git worktree gets its own stable 100-port block.

Examples:
  forge env devstack port 3091     # the reliant-api host port for this worktree
  forge env devstack key           # the worktree key ("" on the primary checkout)`,
	}
	cmd.AddCommand(newDevStackPortCmd())
	cmd.AddCommand(newDevStackKeyCmd())
	cmd.AddCommand(newDevStackListCmd())
	cmd.AddCommand(newDevStackPruneCmd())
	cmd.AddCommand(newDevStackReleaseCmd())
	return cmdutil.StrictGroup(cmd)
}

// newDevStackListCmd: `forge env devstack list` → the registered worktree keys,
// one per line, sorted by block index. This is the source a DECLARATIVE
// per-stack config generator reads to enumerate the active stacks WITHOUT
// re-implementing worktree detection or the registry format.
//
// The DEFAULT stack (key "") is implicit — it is never stored in the registry
// and is NOT printed here. A generator MUST always emit the default's config
// itself; this command lists only the NAMED worktree stacks layered on top.
//
// Empty output (no named worktrees registered yet, or a missing registry) is
// the normal primary-checkout-only case and exits 0.
func newDevStackListCmd() *cobra.Command {
	var stacksOnly bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every holder of a port block, labelled by kind (--stacks-only for the machine-readable worktree roster)",
		Long: `Print EVERY entry in the lock-guarded block registry (.forge/blocks.json),
sorted by block index and labelled with what holds it.

This is the diagnostic view, and showing everything is the point. The ceiling
(dev_stack.max_stacks) counts BLOCKS, so a plain port-block key consumes the
cluster's pre-mapped host-port range exactly as a worktree does. This command
used to print only worktree stacks, which meant a reader who hit "8-block
ceiling" and ran it saw ONE line accounting for eight blocks — the command
recommended by the error message contradicted the error message.

Three kinds of holder are labelled, and the label says whether prune can ever
reclaim it:

  dev stack            the key IS a worktree name. Reclaimable once that
                       worktree is gone.
  derived port-block   the key was COMPOSED from a worktree, e.g. "prod-wt-x"
                       from 'fp.allocate_port(3000, "prod-" + option("worktree"))'.
                       Reclaimable once that worktree is gone; the origin
                       worktree is named in the label.
  standalone           tied to no worktree (e.g. "prod", allocated from the
                       primary checkout). NEVER reclaimable — nothing on disk
                       can make it dead, and reclaiming it would move a live
                       stack's port.

--stacks-only restores the old output: just the DEV-STACK keys, one per line,
no labels. That is the machine-readable roster a per-stack config generator
consumes, and it must stay strictly stacks: a generator that enumerated the raw
registry and treated every key as a worktree emitted a dev NATS account for a
prod web port into a tracked config file. Those keys are the EXACT values
option("worktree") renders to in KCL, so a generator's per-key derivation (NATS
user/password, DB name, …) can be made byte-identical to the KCL's.

The DEFAULT stack (the primary checkout, key "") is shown in the full listing
when it holds an entry, and is never included in --stacks-only — a generator
always emits the default's config itself.

Inside KCL, prefer the fp.dev_stacks() builtin over shelling out to this
command: it returns the same roster during the render (and EMPTY when forge
generate / forge ci render without one). Write the generated file with
fp.write_file(path, content), not KCL's file.write: file.write fires on every
evaluation, so ci, lint, doctor and env render would rewrite it from whatever
roster they saw; fp.write_file writes only on forge env up and an applying
forge env deploy of a local env. A roster file that SHARED infrastructure
mounts (one NATS for every stack) takes fp.write_file(path, content,
shared=True): it lands in the primary checkout, so every stack's render
updates the one copy the shared server reads.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if stacksOnly {
				keys, err := devstack.ListStacks(projectDirForKCL())
				if err != nil {
					return fmt.Errorf("read block registry: %w", err)
				}
				for _, key := range keys {
					_, _ = fmt.Fprintln(cmd.OutOrStdout(), key)
				}
				return nil
			}
			blocks, err := devstack.List(projectDirForKCL())
			if err != nil {
				return fmt.Errorf("read block registry: %w", err)
			}
			if len(blocks) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no port blocks allocated yet (.forge/blocks.json is empty or absent)")
				return nil
			}
			for _, b := range blocks {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "block %d: %-28s %s\n", b.Index, b.DisplayKey(), b.Kind())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&stacksOnly, "stacks-only", false,
		"Print only the dev-stack (worktree) keys, one per line, unlabelled — the machine-readable roster for a per-stack config generator")
	return cmd
}

// newDevStackPortCmd: `forge env devstack port <base>` → base + block(key)*100,
// keyed on the current worktree, allocating the block on first use. This is
// the exact value forge.allocate_port(base, option("worktree")) renders to,
// so a launcher can match the host listen port to the rendered contract port.
func newDevStackPortCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "port <base>",
		Short: "Resolve the worktree-allocated host port for a base port",
		Long: `Print base + block(worktree)*100 — the host port forge.allocate_port(base,
option("worktree")) renders to for the CURRENT worktree.

The block is read from (or allocated into) .forge/blocks.json under the same
file lock the KCL builtin uses, so the printed port is identical to what
'forge env up'/'forge env deploy' renders for this worktree. On the primary checkout
the key is "" so <base> is returned unchanged.

Used by the dev launcher to start the host 'reliant' process listening on the
exact port the in-cluster workspace-controller will dial.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("base port %q is not an integer: %w", args[0], err)
			}
			projectDir := projectDirForKCL()
			key := devstack.Worktree(projectDir)
			port, err := devstack.AllocatePort(projectDir, base, key)
			if err != nil {
				return fmt.Errorf("allocate port for worktree %q: %w", key, err)
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), port)
			return nil
		},
	}
}

// newDevStackKeyCmd: `forge env devstack key` → the current worktree key ("" on
// the primary checkout). Lets a launcher derive the namespace suffix / DB
// suffix without re-implementing the worktree detection.
func newDevStackKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "key",
		Short: "Print the current worktree key (\"\" on the primary checkout)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), devstack.Worktree(projectDirForKCL()))
			return nil
		},
	}
}

// newDevStackPruneCmd: `forge env devstack prune` → reclaim dev-stack blocks
// whose worktree is gone from disk, so the dense block range (see
// dev_stack.max_stacks) doesn't fill up with leaked entries from deleted
// worktrees.
func newDevStackPruneCmd() *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Reclaim dev-stack port blocks for worktrees that no longer exist on disk",
		Long: `Reclaim entries in the block registry (.forge/blocks.json) for git
worktrees that have been removed from disk.

Nothing is ever removed from this registry automatically — a deleted
worktree's block stays held forever unless something reclaims it. Blocks are
DENSE and the reachable range is finite (see dev_stack.max_stacks in
forge.yaml, default 8), so leaked entries from old worktrees are exactly how
a project runs out of dev stacks.

What gets reclaimed: any entry TIED TO A WORKTREE that no longer appears in
'git worktree list --porcelain'. Two kinds of entry are tied to one:
  - A DEV-STACK key (one per git worktree, e.g. "wt-feature-x") — the key is
    the worktree's name.
  - A DERIVED port-block key — one COMPOSED from a worktree, e.g.
    'fp.allocate_port(3000, "prod-" + option("worktree"))' allocating
    "prod-wt-feature-x". The origin worktree is recorded when the block is
    allocated, so reclaiming never has to guess it from the key's name.

What is NEVER reclaimed, no matter how "dead" it looks:
  - The default key "" (block 0) — the primary checkout's implicit block.
  - A STANDALONE port-block key — e.g. "prod", which prod's reliant-web
    dev-server port allocates under on the primary checkout, where there is
    no worktree to derive from. It is tied to no worktree at all, so it can
    never legitimately look dead; reclaiming one would silently move a live,
    running stack's port. This is the single most important correctness
    property of this command, and it is why the distinction is drawn from
    what forge RECORDED at allocation rather than from how composed the key
    looks: "prod" and "prod-wt-x" are indistinguishable by shape.
  - An entry allocated by a forge older than the origin field, until the next
    render from its own worktree records where it came from. Leaving such a
    block held is the deliberately safe failure: a held block wastes a slot,
    while a wrongly-moved port breaks a k3d host mapping and the dev IdP's
    baked-in issuer.

If enumerating live worktrees fails for any reason (git missing, this isn't
a git checkout, the git command errors), prune reclaims NOTHING and reports
the failure — deleting a block that might actually still be live would move
a running stack's ports.

Freeing a block lets the NEXT new worktree take it (blocks are filled
densely, lowest free index first), so pruning is what keeps the block range
from being exhausted by worktrees nobody remembers to clean up.

By default this only PRINTS what would be reclaimed and changes nothing —
pass --apply to actually rewrite the registry. This mutates machine-local
state that other running dev stacks depend on (a concurrent 'forge env up'
is briefly locked out while the rewrite happens), so making it opt-in to
apply is the safer default for a command most people will run interactively
to see what's accumulated.

Examples:
  forge env devstack prune            # show what would be reclaimed
  forge env devstack prune --apply    # actually reclaim it`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dryRun := !apply
			pruned, err := devstack.Prune(projectDirForKCL(), dryRun)
			if err != nil {
				return fmt.Errorf("prune block registry: %w", err)
			}
			out := cmd.OutOrStdout()
			if len(pruned) == 0 {
				_, _ = fmt.Fprintln(out, "nothing to prune — every block tied to a worktree still has its worktree")
			} else {
				verb := "would reclaim"
				if apply {
					verb = "reclaimed"
				}
				for _, p := range pruned {
					_, _ = fmt.Fprintf(out, "%s: %s (block %d) — worktree %q is gone\n",
						verb, p.Key, p.Index, p.Worktree)
				}
				if dryRun {
					_, _ = fmt.Fprintf(out, "\n%d block(s) would be freed. Re-run with --apply to reclaim them.\n", len(pruned))
				} else {
					_, _ = fmt.Fprintf(out, "\n%d block(s) freed.\n", len(pruned))
				}
			}
			return reportUnattributed(out, projectDirForKCL())
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually rewrite the registry (default is dry-run: print what would be reclaimed)")
	return cmd
}

// reportUnattributed prints the blocks forge cannot classify — non-stack
// entries with no recorded origin — after a prune.
//
// These are the terminal case of the conservative migration, and without this
// report they are invisible. An entry written before forge recorded origins
// learns one from the next render that allocates it from its own worktree, but
// when that worktree is ALREADY gone no such render will ever happen. The block
// is then held forever, and — because the ceiling counts blocks — it keeps
// consuming a slot that makes new stacks impossible to allocate.
//
// forge reports rather than reclaims because it genuinely does not know. Which
// of these is a dead worktree's leftover and which is a live standalone key is
// a fact only the person who created them holds, and the two mistakes are not
// equally bad: leaving one held wastes a slot, while releasing a live one moves
// a running stack's ports and breaks its cluster mapping and IdP issuer.
func reportUnattributed(out io.Writer, projectDir string) error {
	unattributed, err := devstack.Unattributed(projectDir)
	if err != nil {
		return fmt.Errorf("report unattributed blocks: %w", err)
	}
	if len(unattributed) == 0 {
		return nil
	}
	_, _ = fmt.Fprintf(out, "\n%d block(s) forge cannot classify — allocated before forge recorded which\n"+
		"worktree a key came from, so prune has no evidence to act on:\n\n", len(unattributed))
	for _, u := range unattributed {
		note := "no live worktree matches this key"
		if u.LikelyLiveOrigin != "" {
			note = fmt.Sprintf("LIKELY IN USE — live worktree %q matches; do NOT release", u.LikelyLiveOrigin)
		}
		_, _ = fmt.Fprintf(out, "  block %d: %-28s %s\n", u.Index, u.Key, note)
	}
	_, _ = fmt.Fprint(out, "\nforge will not guess which of these are dead: a key composed from a worktree\n"+
		"and a standalone key are indistinguishable by name, and releasing a live one\n"+
		"moves that stack's ports (breaking its k3d host mapping and the dev IdP's\n"+
		"baked-in issuer). Each will classify itself the next time its own worktree\n"+
		"renders — but a key whose worktree is already deleted can never do that, so\n"+
		"those need a decision from you:\n\n"+
		"  forge env devstack release <key> --apply   # only if you know it is dead\n")
	return nil
}

// newDevStackReleaseCmd: `forge env devstack release <key>` → reclaim exactly
// one named block on the operator's assertion that it is dead.
//
// Deliberately separate from prune. Prune acts only on evidence forge recorded
// itself and must stay safe to run blind; this acts on a human assertion about
// an entry forge has no evidence for. Folding the two together would make the
// command people run to LOOK at their registry capable of moving a live stack's
// ports.
func newDevStackReleaseCmd() *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "release <key>",
		Short: "Reclaim one named port block that you know is no longer in use",
		Long: `Release the block held by exactly one named key.

This is the manual counterpart to 'prune'. Prune reclaims blocks forge can
PROVE are dead — a dev stack or a derived port-block key whose worktree is gone
— and will never touch an entry it has no evidence for. Release is for those
remaining entries: typically blocks allocated before forge recorded which
worktree a key came from, whose worktree was deleted before it could ever be
recorded. 'forge env devstack prune' lists them.

forge cannot make this call itself. A key composed from a worktree
("prod-wt-x") and a standalone key ("prod") are indistinguishable by name once
the worktree is gone, and the two possible mistakes are not equally bad:
leaving a block held wastes one slot of dev_stack.max_stacks, while releasing a
live one moves that stack's ports — invalidating its k3d host-port mapping and
the dev IdP's baked-in 'iss' claim and redirect URIs, which surfaces much later
as an unrelated-looking failure.

So only release a key when you know which worktree it belonged to and that the
worktree is gone. If a listed key matches a worktree that still exists, leave
it: the next render from that worktree records its origin, after which prune
handles it automatically.

Releasing frees the block for the next new key (blocks fill densely, lowest
free index first).

By default this only PRINTS what it would do — pass --apply to rewrite.

Examples:
  forge env devstack release prod-cp-obs           # show what would happen
  forge env devstack release prod-cp-obs --apply   # actually release it`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			out := cmd.OutOrStdout()
			projectDir := projectDirForKCL()

			if !apply {
				blocks, err := devstack.List(projectDir)
				if err != nil {
					return fmt.Errorf("read block registry: %w", err)
				}
				for _, b := range blocks {
					if b.Key != key {
						continue
					}
					_, _ = fmt.Fprintf(out, "would release: %s (block %d) — %s\n", b.DisplayKey(), b.Index, b.Kind())
					_, _ = fmt.Fprintln(out, "\nRe-run with --apply to release it. Only do so if you know this block is dead:\n"+
						"releasing a live one moves that stack's ports.")
					return nil
				}
				return fmt.Errorf("no block is registered for key %q (see `forge env devstack list`)", key)
			}

			index, err := devstack.ReleaseKey(projectDir, key)
			if err != nil {
				return fmt.Errorf("release block: %w", err)
			}
			_, _ = fmt.Fprintf(out, "released: %s (block %d)\n", key, index)
			return nil
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "Actually rewrite the registry (default is dry-run)")
	return cmd
}

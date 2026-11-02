// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	forgecli "github.com/reliant-labs/forge/cli"
)

// forgeCmdName is the subcommand forge's command tree is mounted under.
const forgeCmdName = "forge"

func newForgeCmd() *cobra.Command {
	root := forgecli.NewRootCmd()
	// forge's command carries its own Version line but no template, so cobra
	// resolves the template up the tree and finds reliant's, which renders an
	// annotation only reliant's root has: `reliant forge --version` printed
	// nothing and exited 0.
	root.SetVersionTemplate(`{{with .DisplayName}}{{printf "%s " .}}{{end}}{{printf "version %s" .Version}}
`)
	// `reliant forge …` is signed in wherever Reliant is: point forge's
	// credential helper at this binary before any forge command runs. Done
	// in the hook, not here — this constructor runs for EVERY reliant
	// invocation, and a daemon must export its own pinned helper, not
	// inherit an unpinned one from building the command tree.
	inner := root.PersistentPreRunE
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		useReliantSessionForForge()
		if inner != nil {
			return inner(cmd, args)
		}
		return nil
	}
	return root
}

// forgeHandOff is forge's own decision of which forge runs a command: the seam
// the tests replace.
var forgeHandOff = forgecli.HandOffToPinned

// handOffToPinnedForge runs `reliant forge …` under the forge the project in
// scope pins, when that is not the forge compiled into this binary. It
// returns only when this binary's forge is the one to run, or with the reason
// the pinned forge could not be had — in which case nothing may run.
//
// WHY. The forge compiled into reliant is whatever reliant was built with,
// and every project pins its own (forge.yaml forge_version). Forge report
// 86c6fd64: `reliant forge generate` in control-plane, pinned to v1.0.4, ran
// the embedded v0.1.47 and rewrote hundreds of generated lines. The decision
// — which forge, from where, and when a missing one is fatal — is forge's
// (cli.HandOffToPinned, shared with the standalone `forge` binary); this is
// where reliant asks it, before the command tree parses the line, so a
// command or flag only the pinned forge has still reaches it. With no pin,
// the embedded forge runs and says which version it is.
func handOffToPinnedForge(root *cobra.Command, args []string, stderr io.Writer) error {
	fargs, ok := forgeArgs(root, args)
	if !ok {
		return nil
	}
	// The pinned forge is exec'd with this process's environment, so it
	// needs the session helper now, not in the tree's PersistentPreRunE.
	useReliantSessionForForge()
	_, err := forgeHandOff(context.Background(), fargs, stderr)
	return err
}

// forgeArgs reports forge's own arguments when args (reliant's, without the
// program name) invoke `reliant forge …`: everything after the `forge` word
// that names the subcommand, which a flag value spelled "forge" does not.
func forgeArgs(root *cobra.Command, args []string) ([]string, bool) {
	for i, a := range args {
		if a != forgeCmdName {
			continue
		}
		if c, _, err := root.Find(args[:i+1]); err == nil && c.Name() == forgeCmdName && c.Parent() == root {
			return args[i+1:], true
		}
	}
	return nil, false
}

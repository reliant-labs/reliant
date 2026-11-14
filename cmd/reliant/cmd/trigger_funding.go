// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// Which AI accounts an automation's runs may spend (control-plane
// docs/design/org-provider-accounts.md §11.3, §11.5). `trigger get` prints
// the stamp of a trigger of an org, and `trigger update <t>
// --org-accounts-only` tightens it to the org's own accounts. The CLI only
// tightens: letting an automation spend its owner's own accounts is a
// person's decision, made in the app (and refused there too in an org with
// other members unless its policy admits it).

// orgAccountsOnlyFlag is update's flag that tightens a trigger to its org's accounts.
const orgAccountsOnlyFlag = "org-accounts-only"

var errLoosenInApp = errors.New("--org-accounts-only=false is not taken from the CLI: letting an automation spend your own AI accounts is done in the app (Workflows, open the workflow, its trigger, AI accounts)")

// unattendedFundingLabel is Trigger.unattended_funding in the words of the
// stamp, with what it means. A server that predates the field reads as any,
// which is how such a trigger runs.
func unattendedFundingLabel(f reliantv1.UnattendedFunding) string {
	if f == reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY {
		return "org_only (its runs spend only the organization's AI accounts)"
	}
	return "any (its runs may also spend your own AI accounts)"
}

// printFundingWarning prints a write's funding warning, if it carries one:
// the automation was stored, but no account of its org can run its model yet.
func printFundingWarning(out io.Writer, w *reliantv1.TriggerFundingWarning) {
	if w.GetMessage() == "" {
		return
	}
	fmt.Fprintf(out, "\nWarning: %s\n", w.GetMessage())
}

// onlyFundingChanged reports whether --org-accounts-only is the one
// definition flag an update was given, so the rest of the definition is the
// stored one rather than a full replacement from flags.
func onlyFundingChanged(cmd *cobra.Command) bool {
	if !cmd.Flags().Changed(orgAccountsOnlyFlag) {
		return false
	}
	// Inherited flags (--daemon-session, the connection's) choose how to
	// connect, not what to write.
	others := false
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if f.Name != orgAccountsOnlyFlag && cmd.InheritedFlags().Lookup(f.Name) == nil {
			others = true
		}
	})
	return !others
}

// updateDefinition is what `trigger update` sends: the stored definition when
// --org-accounts-only is the only flag, else the full replacement from flags
// (fromFlags), with the stamp set to ORG_ONLY whenever the flag was given.
func updateDefinition(
	cmd *cobra.Command,
	orgAccountsOnly bool,
	existing *reliantv1.Trigger,
	fromFlags func() (*reliantv1.TriggerDefinition, error),
) (*reliantv1.TriggerDefinition, error) {
	changed := cmd.Flags().Changed(orgAccountsOnlyFlag)
	if changed && !orgAccountsOnly {
		return nil, errLoosenInApp
	}
	var def *reliantv1.TriggerDefinition
	if onlyFundingChanged(cmd) {
		def = triggers.StoredDefinition(existing)
	} else {
		var err error
		if def, err = fromFlags(); err != nil {
			return nil, err
		}
	}
	if changed {
		def.UnattendedFunding = reliantv1.UnattendedFunding_UNATTENDED_FUNDING_ORG_ONLY
	}
	return def, nil
}

// Package cli — `forge why`: the reasons to use forge, each with the
// benefits that back it, read from internal/benefits/benefits.yaml.
//
// WHY THIS EXISTS. `forge project benefits` answers "what does forge do?"
// with an inventory, one layer per entry. The question a skeptical engineer,
// or the agent working for one, actually asks first is "why forge at all?",
// and its answer is the cross-layer join no single entry states. The reasons
// registry writes that join down with its evidence; this verb prints it, so
// the answer an agent quotes is the checked one, never a paraphrase.
//
// It reads nothing from the directory it runs in: the reasons are the same
// in a forge project, in any other repo, and with no repo at all.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// whySchema names the `forge why --json` document.
const whySchema = "forge.why/v1"

// whyReport is `forge why --json` (and `forge project benefits --why
// --json`, and the MCP why tool).
type whyReport struct {
	Schema  string      `json:"schema"`
	Reasons []whyReason `json:"reasons"`
	// Next is the command to run after reading this.
	Next string `json:"next"`
	// Limits is the command that prints what forge does not do, beside
	// the comparison with other platforms.
	Limits string `json:"limits"`
}

// whyReason is one reason with its evidence expanded.
type whyReason struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Thesis       string                `json:"thesis"`
	Mechanism    string                `json:"mechanism"`
	Availability benefits.Availability `json:"availability"`
	// Show is the full command that demonstrates the reason, runnable as
	// printed; empty when the reason names none.
	Show     string       `json:"show,omitempty"`
	Benefits []whyBenefit `json:"benefits"`
}

// whyBenefit is a cited benefit: enough to quote it and to find it
// (docs/benefits.md#<id>, `forge project benefits --json`).
type whyBenefit struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Claim        string                `json:"claim"`
	Category     string                `json:"category"`
	Status       benefits.Status       `json:"status"`
	Availability benefits.Availability `json:"availability"`
}

func newWhyCmd() *cobra.Command {
	var (
		jsonOut bool
		verbose bool
	)
	cmd := &cobra.Command{
		Use:   "why [reason]",
		Short: "Why forge: the reasons to use it, each with the checked benefits that back it",
		Long: `Why forge, in a dozen reasons. Each is an outcome a stitched-together stack
cannot give you because no one tool in it knows the others' facts, the
mechanism that makes it work ("Because forge knows X, it can Y"), the command
that shows it, and the benefits that back it, from forge's benefits registry
(docs/why-forge.md is the same list, rendered). A test resolves every one of
those benefits against this binary, so a reason cannot claim what forge does
not ship.

A reason tagged hosted needs Reliant hosting; the rest are open-source forge
on any infrastructure. Works anywhere: it reads nothing from the directory.

  forge why                  every reason: title, mechanism, the command that shows it
  forge why --verbose        also each thesis and the claim of every benefit it cites
  forge why <reason>         one reason in full (ids: forge why --json | jq '.reasons[].id')
  forge why --json           for machines (the MCP why tool); always the full text

Next, 'forge project benefits --all': every benefit, one layer at a time.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := benefits.Load()
			if err != nil {
				return err
			}
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			rep, err := buildWhyReport(reg, id)
			if err != nil {
				return err
			}
			return writeWhyOutput(cmd.OutOrStdout(), rep, jsonOut, verbose || id != "")
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the reasons as JSON (forge.why/v1)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Print each thesis and the claim of every benefit a reason cites")
	return cmd
}

// buildWhyReport expands every reason (only the one named, when id is set)
// with the benefits it cites.
func buildWhyReport(reg *benefits.Registry, id string) (whyReport, error) {
	name := cmdutil.Name()
	rep := whyReport{
		Schema:  whySchema,
		Reasons: []whyReason{},
		Next:    name + " project benefits --all",
		Limits:  name + " skill load compare",
	}
	reasons := reg.Reasons
	if id != "" {
		rs, ok := reg.Reason(id)
		if !ok {
			return whyReport{}, cliutil.UserErr(name+" why "+id,
				fmt.Sprintf("no reason %q", id), "",
				"use one of: "+strings.Join(reg.ReasonIDs(), ", "))
		}
		reasons = []benefits.Reason{rs}
	}
	for _, rs := range reasons {
		wr := whyReason{
			ID:           rs.ID,
			Title:        rs.Title,
			Thesis:       oneLineText(rs.Thesis),
			Mechanism:    oneLineText(rs.Mechanism),
			Availability: rs.Availability,
			Benefits:     []whyBenefit{},
		}
		if rs.Show != "" {
			wr.Show = name + " " + rs.Show
		}
		for _, bid := range rs.Benefits {
			b, _ := reg.Benefit(bid) // Validate resolved every id
			wr.Benefits = append(wr.Benefits, whyBenefit{
				ID: b.ID, Title: b.Title, Claim: oneLineText(b.Claim),
				Category: b.Category, Status: b.Status, Availability: b.Availability,
			})
		}
		rep.Reasons = append(rep.Reasons, wr)
	}
	return rep, nil
}

// writeWhyOutput prints the report as JSON, or as text: short by default,
// with each thesis and cited claim when verbose.
func writeWhyOutput(w io.Writer, rep whyReport, jsonOut, verbose bool) error {
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	return writeWhy(w, rep, verbose)
}

// whyWidth is the column the text output wraps at.
const whyWidth = 80

// whyTag marks what a reason or a cited benefit needs beyond open-source
// forge.
var whyTag = map[benefits.Availability]string{
	benefits.AvailabilityHosted:  "hosted",
	benefits.AvailabilityReliant: "Reliant",
}

func writeWhy(w io.Writer, rep whyReport, verbose bool) error {
	var b strings.Builder
	if len(rep.Reasons) > 1 {
		fmt.Fprintf(&b, "Why forge: %d reasons, each backed by benefits checked against this binary.\n"+
			"A reason tagged [hosted] needs Reliant hosting; the rest are open-source forge.\n\n", len(rep.Reasons))
	}
	for i, rs := range rep.Reasons {
		head := fmt.Sprintf("%d. %s", i+1, rs.Title)
		if len(rep.Reasons) == 1 {
			head = rs.Title
		}
		if tag := whyTag[rs.Availability]; tag != "" {
			head += " [" + tag + "]"
		}
		fmt.Fprintf(&b, "%s\n   id: %s\n", head, rs.ID)
		if verbose {
			b.WriteString("\n" + benefits.WrapText(rs.Thesis, whyWidth, "   ") + "\n")
		}
		b.WriteString(benefits.WrapText(rs.Mechanism, whyWidth, "   "))
		if rs.Show != "" {
			fmt.Fprintf(&b, "   See it: %s\n", rs.Show)
		}
		if verbose {
			b.WriteString("   Backed by:\n")
			for _, e := range rs.Benefits {
				line := e.Title + " (" + e.ID
				if tag := whyTag[e.Availability]; tag != "" {
					line += ", " + tag
				}
				line += "): " + e.Claim
				text := benefits.WrapText(line, whyWidth, "       ")
				b.WriteString("     - " + strings.TrimPrefix(text, "       "))
			}
		} else {
			ids := make([]string, len(rs.Benefits))
			for j, e := range rs.Benefits {
				ids[j] = e.ID
				if tag := whyTag[e.Availability]; tag != "" {
					ids[j] += " (" + tag + ")"
				}
			}
			b.WriteString(benefits.WrapText("Backed by: "+strings.Join(ids, ", "), whyWidth, "   "))
		}
		b.WriteString("\n")
	}
	if !verbose {
		fmt.Fprintf(&b, "Each thesis and every cited claim: `%s why --verbose` (or `%s why <id>`).\n",
			cmdutil.Name(), cmdutil.Name())
	}
	fmt.Fprintf(&b, "Every benefit: `%s`.\nWhat forge does not do, beside other platforms: `%s`.\n", rep.Next, rep.Limits)
	_, err := io.WriteString(w, b.String())
	return err
}

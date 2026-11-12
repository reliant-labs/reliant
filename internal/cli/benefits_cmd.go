// Package cli — `forge project benefits`: what forge does for the repo you
// are in, read from internal/benefits/benefits.yaml.
//
// WHY THIS EXISTS. An agent meets forge in a repo that is not a forge project
// far more often than in one that is, and every introspection verb it could
// reach answered a forge project's question ("what does THIS project have?").
// This one answers the question that agent actually has: what can forge do
// here, now, without a rewrite, how do I start, and how long will it take?
// The answer is the registry's scope: any entries, each with its entry recipe
// and an agent-time estimate the agent can quote to its user. In a forge
// project it is every shipped benefit.
//
// Nothing here is transcribed: the list is the registry, whose evidence
// TestBenefitsEvidenceExists resolves against this binary.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/benefits"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cliutil"
)

// benefitsReport is `forge project benefits --json`.
type benefitsReport struct {
	// ForgeProject is whether a forge.yaml was found from the directory.
	ForgeProject bool `json:"forge_project"`
	// Benefits are the ones that apply (all shipped ones with --all).
	Benefits []benefits.Benefit `json:"benefits"`
	// More counts the shipped benefits that apply once the repo is a forge
	// project and are not listed; 0 in a forge project or with --all.
	More int `json:"more"`
	// Next is the command to run after reading this.
	Next string `json:"next"`
}

func newBenefitsCmd() *cobra.Command {
	var (
		jsonOut  bool
		all      bool
		category string
	)
	cmd := &cobra.Command{
		Use:   "benefits",
		Short: "What forge does for the repo you are in: each benefit, how to start, and the agent time to first value",
		Long: `What forge does for the repo you are in, from forge's benefits registry
(docs/benefits.md is the same list, rendered).

Outside a forge project (no forge.yaml from here up) it lists the benefits that
work in ANY repo, in any language, with no forge.yaml: the linters that run
standalone, the runtime libraries, the playbooks, the no-account static-site
path. Each row is the first step and the agent's time to first value, worded so
an agent can quote it to its user. In a forge project it lists every shipped
benefit.

  forge project benefits                 what applies here
  forge project benefits --all           every shipped benefit, wherever you are
  forge project benefits --category run  one category (build, data, test, dev,
                                         guardrails, security, ship, host, run, agents)
  forge project benefits --json          for machines (the MCP benefits tool)

Next, 'forge skill load adopt': the survey, the estimate rubric and every entry
point, for bringing forge into an existing app without a rewrite.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := benefits.Load()
			if err != nil {
				return err
			}
			if category != "" && !reg.HasCategory(category) {
				return cliutil.UserErr("forge project benefits --category "+category,
					fmt.Sprintf("no benefit category %q", category), "",
					"use one of: "+categoryIDs(reg))
			}
			// A lookup that fails (an unreadable working directory) is an
			// error, never "not a forge project": that would offer the
			// any-repo catalog for a directory forge could not even read.
			root, err := cmdutil.FindProjectRoot()
			if err != nil {
				return fmt.Errorf("finding the project root: %w", err)
			}
			rep := buildBenefitsReport(reg, root != "", all, category)
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			return writeBenefits(cmd.OutOrStdout(), reg, rep, all)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the benefits as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "List every shipped benefit, not only the ones that apply here")
	cmd.Flags().StringVar(&category, "category", "", "Only this category (build, data, test, dev, guardrails, security, ship, host, run, agents)")
	return cmd
}

// buildBenefitsReport selects what applies: scope: any outside a forge
// project, every shipped benefit inside one or with all.
func buildBenefitsReport(reg *benefits.Registry, forgeProject, all bool, category string) benefitsReport {
	inCategory := func(bs []benefits.Benefit) []benefits.Benefit {
		if category == "" {
			return bs
		}
		var out []benefits.Benefit
		for _, b := range bs {
			if b.Category == category {
				out = append(out, b)
			}
		}
		return out
	}
	rep := benefitsReport{ForgeProject: forgeProject, Next: cmdutil.Name() + " skill load adopt"}
	everything := inCategory(reg.AppliesTo(true))
	if forgeProject || all {
		rep.Benefits = everything
	} else {
		rep.Benefits = inCategory(reg.AppliesTo(false))
		rep.More = len(everything) - len(rep.Benefits)
	}
	if forgeProject {
		rep.Next = cmdutil.Name() + " skill load forge"
	}
	if rep.Benefits == nil {
		rep.Benefits = []benefits.Benefit{}
	}
	return rep
}

func writeBenefits(w io.Writer, reg *benefits.Registry, rep benefitsReport, all bool) error {
	name := cmdutil.Name()
	if !rep.ForgeProject && !all {
		_, _ = fmt.Fprintf(w, "No forge.yaml here: these work in this repo as it is, in any language.\n"+
			"Each row is the first step and the agent time to first value — quote it to your user.\n\n")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, b := range rep.Benefits {
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", b.Title, b.Estimate, oneLineText(b.Adopt))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "\nNext: `%s` — every entry point (linters, playbooks, monitoring, runtime SDKs,\n"+
			"deploys, migrations, secrets, hosting, codegen, CI), with the survey and estimate rubric.\n", rep.Next)
		if rep.More > 0 {
			_, _ = fmt.Fprintf(w, "%d more apply once this repo is a forge project: `%s project benefits --all`.\n", rep.More, name)
		}
		return nil
	}
	for _, c := range reg.Categories {
		var rows []benefits.Benefit
		for _, b := range rep.Benefits {
			if b.Category == c.ID {
				rows = append(rows, b)
			}
		}
		if len(rows) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s\n", strings.ToUpper(c.Title))
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, b := range rows {
			tag := ""
			if b.Scope == benefits.ScopeAny {
				tag = "any repo"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", b.ID, b.Title, tag)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(w)
	}
	_, _ = fmt.Fprintf(w, "%d benefits. Each one's claim, use and evidence: docs/benefits.md#<id>, or --json.\n", len(rep.Benefits))
	return nil
}

func categoryIDs(reg *benefits.Registry) string {
	ids := make([]string, 0, len(reg.Categories))
	for _, c := range reg.Categories {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ", ")
}

// oneLineText collapses YAML prose onto one line for a table row.
func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

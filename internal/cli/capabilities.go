// Package cli — `forge project capabilities`: everything forge can do for
// you, in one call, derived from forge itself.
//
// WHY THIS EXISTS. A dogfood run measured "researching how forge wants
// things done" as the single largest time bucket on the way to a green
// gate. The surface is not hidden — every verb is under some `--help` —
// but reaching it costs one invocation per command group, so an agent that
// does not already suspect `forge scaffold rpc` exists writes the handler
// stub by hand. This command answers "what does forge already do?" in one
// call, which is the only shape that competes with guessing.
//
// Everything here is ENUMERATED, never transcribed:
//
//   - commands   the real cobra tree (cli.NewRootCmd), walked depth-first
//     with each command's own Short. A verb that is added,
//     renamed or removed changes this dump with no edit here.
//   - analyzers  every flag on `forge lint`, INCLUDING the maintainer flags
//     hidden from --help — a hidden analyzer is still a
//     capability, and `--wire-coverage` / `--scaffolds` are
//     documented nowhere else an agent will look.
//   - markers    markerSpecs(), which a package test pins against the real
//     recognizers in the proto scanner.
//
// The deliberate omission is prose about WHEN to reach for each verb. That
// is what the skills are for; a transcribed opinion here would be the
// hand-maintained list this command exists to avoid.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// CommandSpec is one node of the command tree: the full invocation path
// with its own one-line summary.
type CommandSpec struct {
	Path  string `json:"path"`  // e.g. "forge scaffold entity"
	Short string `json:"short"` // the command's own Short
	Group bool   `json:"group"` // true when it only groups subcommands
}

// AnalyzerSpec is one `forge lint` analyzer flag. Hidden reports whether it
// is a maintainer flag suppressed from `forge lint --help` (still fully
// functional, listed by `forge lint --help-dev`).
type AnalyzerSpec struct {
	Flag   string `json:"flag"`
	Usage  string `json:"usage"`
	Hidden bool   `json:"hidden"`
}

// CapabilitiesSpec is the whole dump.
type CapabilitiesSpec struct {
	Commands  []CommandSpec  `json:"commands,omitempty"`
	Analyzers []AnalyzerSpec `json:"analyzers,omitempty"`
	Markers   []MarkerSpec   `json:"markers,omitempty"`
}

// newCapabilitiesCmd builds `forge project capabilities`. Like
// `annotations`, the answer is forge's own surface rather than project
// state, so it reads no project files and runs anywhere.
func newCapabilitiesCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "List everything forge can do — every command, analyzer, and marker, in one call",
		Long: `List everything forge can do, in one call.

Emits forge's whole surface, enumerated from forge itself:

  commands    every verb in the command tree, with its one-line summary
  analyzers   every 'forge lint' analyzer, including the maintainer flags
              hidden from --help
  markers     every // forge:* comment marker the proto scanner reads

Read this BEFORE hand-writing something forge already scaffolds. Two
sibling dumps cover the rest of the vocabulary:

  forge project annotations   the entity-authoring spec (the proto->column
                              mapping, projected buf.validate rules,
                              proto options)
  forge skill list            the skill catalog; 'forge skill load <name>'
                              prints the copy THIS binary ships

Examples:
  forge project capabilities
  forge project capabilities --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCapabilities(cmd.OutOrStdout(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the inventory as JSON")
	return cmd
}

func runCapabilities(w io.Writer, asJSON bool) error {
	spec := buildCapabilitiesSpec()
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(spec)
	}
	return writeCapabilitiesText(w, spec)
}

func buildCapabilitiesSpec() CapabilitiesSpec {
	root := NewRootCmd()
	return CapabilitiesSpec{
		Commands:  commandSpecs(root),
		Analyzers: analyzerSpecs(root),
		Markers:   markerSpecs(),
	}
}

// commandSpecs walks the real cobra tree depth-first. Hidden commands and
// cobra's own `help` / `completion` scaffolding are skipped: they are not
// forge capabilities.
func commandSpecs(root *cobra.Command) []CommandSpec {
	var out []CommandSpec
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		for _, child := range cmd.Commands() {
			if child.Hidden || child.Name() == "help" || child.Name() == "completion" {
				continue
			}
			kids := 0
			for _, g := range child.Commands() {
				if !g.Hidden {
					kids++
				}
			}
			out = append(out, CommandSpec{
				Path:  child.CommandPath(),
				Short: child.Short,
				Group: kids > 0,
			})
			walk(child)
		}
	}
	walk(root)
	return out
}

// analyzerSpecs reads every flag off the real `forge lint` command. The
// hidden ones matter most: they are functional analyzers that `forge lint
// --help` deliberately omits, so nothing else an agent reads mentions them.
func analyzerSpecs(root *cobra.Command) []AnalyzerSpec {
	lint := findChildCommand(root, "lint")
	if lint == nil {
		return nil
	}
	var out []AnalyzerSpec
	lint.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" || f.Name == "help-dev" {
			return
		}
		out = append(out, AnalyzerSpec{
			Flag:   "--" + f.Name,
			Usage:  firstSentence(f.Usage),
			Hidden: f.Hidden,
		})
	})
	return out
}

func findChildCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// firstSentence trims authored prose down to its opening claim so the
// inventory stays scannable. Truncating loses detail but never invents any.
//
// A "." inside backticks or parentheses does not end a sentence: the
// --generated-drift usage quotes the literal banner "Code generated by
// forge. DO NOT EDIT." and a naive split cut the flag's description in half.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	depth, inTick := 0, false
	for i := 0; i+1 < len(s); i++ {
		switch s[i] {
		case '`':
			inTick = !inTick
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		case '.':
			if s[i+1] == ' ' && depth == 0 && !inTick && i > 0 {
				return s[:i+1]
			}
		}
	}
	return s
}

func writeCapabilitiesText(w io.Writer, spec CapabilitiesSpec) error {
	p := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	if err := p("COMMANDS — every verb forge has (run any with --help for its flags)\n"); err != nil {
		return err
	}
	for _, c := range spec.Commands {
		depth := strings.Count(c.Path, " ") - 1
		label := c.Path
		if c.Group {
			label += " …"
		}
		if err := p("  %s%-38s %s\n", strings.Repeat("  ", depth), label, c.Short); err != nil {
			return err
		}
	}

	if len(spec.Analyzers) > 0 {
		if err := p("\nLINT ANALYZERS (`forge lint <flag>`; ✱ = hidden from --help, fully functional)\n"); err != nil {
			return err
		}
		for _, a := range spec.Analyzers {
			mark := " "
			if a.Hidden {
				mark = "✱"
			}
			if err := p("  %s %-26s %s\n", mark, a.Flag, a.Usage); err != nil {
				return err
			}
		}
	}

	if len(spec.Markers) > 0 {
		if err := p("\nMARKERS (`// forge:*` comments forge reads out of your proto and Go source)\n"); err != nil {
			return err
		}
		for _, m := range spec.Markers {
			if err := p("  %-18s (%s) %s\n", m.Name, m.AppliesTo, firstSentence(m.Effect)); err != nil {
				return err
			}
		}
	}

	return p("\nEntity-authoring vocabulary: `forge project annotations`.\n" +
		"Skills (authoritative, shipped inside this binary): `forge skill list`, `forge skill load <name>`.\n")
}

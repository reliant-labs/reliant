// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/workflow/lint"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func newWorkflowLintCmd() *cobra.Command {
	var (
		workflowDir string
		jsonOutput  bool
		listRules   bool
	)
	cmd := &cobra.Command{
		Use:   "lint [path]",
		Short: "Check workflows for behaviour validation cannot see",
		Long: `Lints workflow YAML files: rules about whether a workflow that validates will
also behave when nobody is watching. Does every run end, does a failure reach
someone, can a run hold a max_running slot forever, does a step fetch into the
shared checkout, is a secret written into the file, do the scenarios fire every
trigger and expect every ending.

Every finding names its rule and links to the rule's section of the docs. An
error fails (exit 1); a warning is reported. ` + "`reliant workflow validate`" + ` runs the same
rules, so a repository's gate that validates its workflows lints them too.

Silence a finding you have judged where it is, with the reason, in a comment
on the node (or on a top-level key, for the whole workflow):

  # lint:allow action-error-unread — best effort: a 404 means already removed
  - id: clear_label

--rules lists every rule with its severity.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if listRules {
				return printLintRules(jsonOutput)
			}
			return runWorkflowLint(args, workflowDir, jsonOutput)
		},
	}
	cmd.Flags().StringVar(&workflowDir, "dir", ".reliant/workflows", "Directory containing workflow YAML files")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (for CI)")
	cmd.Flags().BoolVar(&listRules, "rules", false, "List the rules instead of linting")
	return cmd
}

type lintFileResult struct {
	File     string         `json:"file"`
	Name     string         `json:"name,omitempty"`
	Error    string         `json:"error,omitempty"`
	Findings []lint.Finding `json:"findings"`
}

func runWorkflowLint(args []string, dir string, jsonOut bool) error {
	var files []string
	if len(args) == 1 {
		info, err := os.Stat(args[0])
		if err != nil {
			return fmt.Errorf("cannot access %s: %w", args[0], err)
		}
		if info.IsDir() {
			dir = args[0]
		} else {
			dir, files = filepath.Dir(args[0]), []string{args[0]}
		}
	}
	if !filepath.IsAbs(dir) {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		dir = abs
	}
	layout, err := readWorkflowLayout(dir)
	if err != nil {
		return err
	}
	if files == nil {
		if files, err = workflowFilesIn(dir); err != nil {
			return err
		}
	}

	results := make([]lintFileResult, 0, len(files))
	failed := false
	for _, file := range files {
		r := lintFileResult{File: filepath.Base(file), Findings: []lint.Finding{}}
		data, err := os.ReadFile(file)
		if err == nil {
			var parseErr error
			if wf, perr := wfyaml.ParseWorkflow(data); perr != nil {
				parseErr = perr
			} else {
				r.Name = wf.GetName()
				r.Findings = append(r.Findings, lintWorkflowSource(data, layout)...)
			}
			err = parseErr
		}
		if err != nil {
			r.Error = err.Error()
			failed = true
		}
		if lint.HasErrors(r.Findings) {
			failed = true
		}
		results = append(results, r)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		errs, warns := 0, 0
		for _, r := range results {
			status := "\u2713"
			if r.Error != "" || lint.HasErrors(r.Findings) {
				status = "\u2717"
			}
			fmt.Printf("  %s %s", status, r.File)
			if r.Name != "" {
				fmt.Printf(" (%s)", r.Name)
			}
			fmt.Println()
			if r.Error != "" {
				fmt.Printf("      Error: %s\n", r.Error)
			}
			printLintFindings("      ", r.Findings)
			for _, f := range r.Findings {
				if f.Severity == lint.SeverityError {
					errs++
				} else {
					warns++
				}
			}
		}
		fmt.Printf("\n%d workflow(s): %d lint error(s), %d warning(s)\n", len(results), errs, warns)
	}
	if failed {
		return fmt.Errorf("lint failed")
	}
	return nil
}

// lintWorkflowSource lints one project workflow file with the project's
// scenarios for it and its refs resolved as the app resolves them. A file
// that does not parse yields nothing here: validation reports why.
func lintWorkflowSource(data []byte, layout *workflowref.Layout) []lint.Finding {
	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil || wf == nil {
		return nil
	}
	in := lint.Input{Workflow: wf, Source: data}
	if layout != nil {
		loader := projectWorkflowLoader(layout.Workflows)
		in.LoadRef = loader
		for _, sf := range layout.ScenariosFor(wf.GetName()) {
			scenarios, err := wfscenario.ParseScenarioYAML(sf.Content)
			if err != nil {
				continue // the scenario runner reports a file that does not parse
			}
			in.Scenarios = append(in.Scenarios, scenarios...)
		}
	}
	return lint.Check(in)
}

func printLintFindings(indent string, findings []lint.Finding) {
	for _, f := range findings {
		label := "Lint warning"
		if f.Severity == lint.SeverityError {
			label = "Lint error"
		}
		fmt.Printf("%s%s: %s\n", indent, label, f.String())
	}
}

func printLintRules(jsonOut bool) error {
	rules := lint.Rules()
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rules)
	}
	for _, r := range rules {
		fmt.Printf("%-28s %-8s %s\n", r.ID, r.Severity, r.Summary)
	}
	fmt.Printf("\nEach rule's section: %s#<rule-id>\n", lint.DocsBase)
	return nil
}

// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage and validate workflows",
		Long:  `Commands for validating, listing, and running Reliant workflows.`,
	}

	cmd.AddCommand(newWorkflowValidateCmd())
	cmd.AddCommand(newWorkflowListCmd())
	cmd.AddCommand(newWorkflowRunCmd())

	return cmd
}

func newWorkflowValidateCmd() *cobra.Command {
	var (
		workflowDir     string
		verboseValidate bool
		failFast        bool
		jsonOutput      bool
		includeBuiltins bool
	)

	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate workflow YAML files",
		Long: `Validates workflow YAML files against the Reliant workflow schema. Runs
static analysis including structural validation, CEL expression checking,
input/output type verification, and cross-workflow contract validation.

If a specific file path is given, validates that file only. Otherwise,
validates all *.yaml files in the workflow directory.

Exit code 0 if all workflows are valid, 1 if any errors are found.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowValidate(cmd, args, workflowDir, verboseValidate, failFast, jsonOutput, includeBuiltins)
		},
	}

	cmd.Flags().StringVar(&workflowDir, "dir", ".reliant/workflows", "Directory containing workflow YAML files")
	cmd.Flags().BoolVarP(&verboseValidate, "verbose", "V", false, "Show detailed output for each workflow")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Stop on first validation error")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (for CI)")
	cmd.Flags().BoolVar(&includeBuiltins, "include-builtins", false, "Also validate builtin workflows")

	return cmd
}

func newWorkflowListCmd() *cobra.Command {
	var (
		jsonOutput   bool
		builtinsOnly bool
		projectOnly  bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List available workflows",
		Long:  `Lists workflows in the current project and builtin workflows.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var workflows []workflowListInfo

			// Collect project workflows
			if !builtinsOnly {
				cwd, _ := os.Getwd()
				projectDir := filepath.Join(cwd, ".reliant", "workflows")
				files, err := collectYAMLFiles(projectDir)
				if err == nil {
					for _, f := range files {
						info := parseWorkflowInfo(f, "project")
						workflows = append(workflows, info)
					}
				}
			}

			// Collect builtin workflows
			if !projectOnly {
				builtinDir := findBuiltinDir()
				if builtinDir != "" {
					files, err := collectYAMLFiles(builtinDir)
					if err == nil {
						for _, f := range files {
							info := parseWorkflowInfo(f, "builtin")
							workflows = append(workflows, info)
						}
					}
				}
			}

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(workflows)
			}

			if len(workflows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No workflows found")
				return nil
			}

			// Print table
			fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-10s %5s  %s\n", "NAME", "SOURCE", "NODES", "INPUTS")
			fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-10s %5s  %s\n", "────", "──────", "─────", "──────")
			for _, wf := range workflows {
				inputStr := strings.Join(wf.Inputs, ", ")
				if len(inputStr) > 40 {
					inputStr = inputStr[:37] + "..."
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s %-10s %5d  %s\n", wf.Name, wf.Source, wf.Nodes, inputStr)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d workflow(s)\n", len(workflows))
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&builtinsOnly, "builtins-only", false, "Only show builtin workflows")
	cmd.Flags().BoolVar(&projectOnly, "project-only", false, "Only show project workflows")

	return cmd
}

type workflowListInfo struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	File   string   `json:"file"`
	Nodes  int      `json:"nodes"`
	Inputs []string `json:"inputs,omitempty"`
}

func parseWorkflowInfo(path, source string) workflowListInfo {
	info := workflowListInfo{
		File:   filepath.Base(path),
		Source: source,
	}

	data, err := os.ReadFile(path)
	if err != nil {
		info.Name = info.File
		return info
	}

	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		info.Name = info.File
		return info
	}

	info.Name = wf.GetName()
	if info.Name == "" {
		info.Name = info.File
	}
	info.Nodes = len(wf.GetNodes())

	for name := range wf.GetInputs() {
		info.Inputs = append(info.Inputs, name)
	}

	return info
}

func newWorkflowRunCmd() *cobra.Command {
	var (
		inputs    []string
		inputFile string
		follow    bool
		projectID string
	)

	cmd := &cobra.Command{
		Use:   "run <workflow-name>",
		Short: "Run a workflow",
		Long: `Triggers a workflow execution via the Reliant cloud API.

Requires authentication (run 'reliant auth login' first).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowName := args[0]

			// Ensure authenticated
			accessToken, err := auth.ReadAccessTokenFromAuthFile()
			if err != nil || accessToken == "" {
				return fmt.Errorf("not authenticated — run 'reliant auth login' first")
			}

			// Build input map
			inputMap := make(map[string]interface{})

			// From --input flags
			for _, kv := range inputs {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid input format %q — expected key=value", kv)
				}
				inputMap[parts[0]] = parts[1]
			}

			// From --input-file
			if inputFile != "" {
				data, err := os.ReadFile(inputFile)
				if err != nil {
					return fmt.Errorf("reading input file: %w", err)
				}
				var fileInputs map[string]interface{}
				if err := json.Unmarshal(data, &fileInputs); err != nil {
					return fmt.Errorf("parsing input file: %w", err)
				}
				for k, v := range fileInputs {
					inputMap[k] = v
				}
			}

			// Build request payload
			payload := map[string]interface{}{
				"workflow_name": workflowName,
				"inputs":        inputMap,
			}
			if projectID != "" {
				payload["project_id"] = projectID
			}

			reqBody, err := json.Marshal(payload)
			if err != nil {
				return fmt.Errorf("encoding request: %w", err)
			}

			// POST to cloud API
			url := serverURL + "/api/v1/workflows/run"
			req, err := http.NewRequestWithContext(cmd.Context(), "POST", url, bytes.NewReader(reqBody))
			if err != nil {
				return fmt.Errorf("creating request: %w", err)
			}
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("API request failed: %w", err)
			}
			defer resp.Body.Close()

			respBody, _ := io.ReadAll(resp.Body)

			if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
				return fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
			}

			var result struct {
				WorkflowID  string `json:"workflow_id"`
				ExecutionID string `json:"execution_id"`
			}
			if err := json.Unmarshal(respBody, &result); err != nil {
				// Still show raw response if we can't parse
				fmt.Fprintf(cmd.OutOrStdout(), "Workflow triggered. Response: %s\n", string(respBody))
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Workflow %q triggered\n", workflowName)
			if result.ExecutionID != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  Execution ID: %s\n", result.ExecutionID)
			}

			if follow {
				fmt.Fprintln(cmd.OutOrStdout(), "  --follow is not yet implemented")
			}

			return nil
		},
	}

	cmd.Flags().StringArrayVarP(&inputs, "input", "i", nil, "Workflow input as key=value (repeatable)")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "JSON file containing workflow inputs")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Stream workflow execution output")
	cmd.Flags().StringVar(&projectID, "project-id", "", "Project ID (default: from .reliant/config)")

	return cmd
}

// --- workflow validate implementation ---

type validateResult struct {
	File     string   `json:"file"`
	Name     string   `json:"name,omitempty"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Nodes    int      `json:"nodes,omitempty"`
	Edges    int      `json:"edges,omitempty"`
}

func runWorkflowValidate(_ *cobra.Command, args []string, dir string, verbose, failFast, jsonOut, includeBuiltins bool) error {
	var files []string

	// Single file or directory from positional arg
	if len(args) == 1 {
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("cannot access %s: %w", path, err)
		}
		if info.IsDir() {
			f, err := collectYAMLFiles(path)
			if err != nil {
				return err
			}
			files = append(files, f...)
		} else {
			files = append(files, path)
		}
	} else {
		// Default directory mode
		if !filepath.IsAbs(dir) {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("could not get working directory: %w", err)
			}
			dir = filepath.Join(cwd, dir)
		}
		f, err := collectYAMLFiles(dir)
		if err != nil {
			return err
		}
		files = append(files, f...)
	}

	// Include builtins
	if includeBuiltins {
		builtinDir := findBuiltinDir()
		if builtinDir != "" {
			f, err := collectYAMLFiles(builtinDir)
			if err == nil {
				files = append(files, f...)
			}
		}
	}

	if len(files) == 0 {
		if jsonOut {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No workflow files found")
		return nil
	}

	if !jsonOut {
		fmt.Printf("Validating %d workflow(s)\n\n", len(files))
	}

	var results []validateResult
	hasErrors := false

	for _, file := range files {
		result := validateWorkflowFile(file)
		results = append(results, result)

		if !jsonOut {
			if verbose {
				printVerboseValidateResult(result)
			} else {
				printCompactValidateResult(result)
			}
		}

		if !result.Valid {
			hasErrors = true
			if failFast {
				break
			}
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return fmt.Errorf("failed to encode JSON: %w", err)
		}
	} else {
		printValidateSummary(results)
	}

	if hasErrors {
		return fmt.Errorf("validation failed")
	}
	return nil
}

func collectYAMLFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("directory not found: %s", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}

	yamlFiles, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	ymlFiles, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	all := append(yamlFiles, ymlFiles...)

	var filtered []string
	for _, f := range all {
		base := filepath.Base(f)
		if strings.HasSuffix(base, "_test.yaml") || strings.HasSuffix(base, "_test.yml") {
			continue
		}
		filtered = append(filtered, f)
	}
	return filtered, nil
}

func findBuiltinDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	path := filepath.Join(cwd, "internal", "workflow", "builtin")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return ""
}

func validateWorkflowFile(path string) validateResult {
	result := validateResult{
		File: filepath.Base(path),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("failed to read file: %v", err))
		return result
	}

	// Parse without validation to extract metadata
	wf, parseErr := wfyaml.ParseWorkflow(data)
	if parseErr != nil {
		result.Errors = append(result.Errors, parseErr.Error())
		return result
	}

	result.Name = wf.GetName()
	result.Nodes = len(wf.GetNodes())
	result.Edges = len(wf.GetEdges())

	// Run full validation
	valResult, valErr := runtime.ValidateYAMLResult(data, nil)
	if valErr != nil {
		result.Errors = append(result.Errors, valErr.Error())
	} else if valResult != nil {
		for _, e := range valResult.Errors() {
			result.Errors = append(result.Errors, e.Error())
		}
		for _, w := range valResult.Warnings() {
			result.Warnings = append(result.Warnings, w.Error())
		}
	}

	result.Valid = len(result.Errors) == 0
	return result
}

func printCompactValidateResult(r validateResult) {
	status := "\u2713"
	if !r.Valid {
		status = "\u2717"
	}

	warningIndicator := ""
	if len(r.Warnings) > 0 {
		warningIndicator = fmt.Sprintf(" (%d warnings)", len(r.Warnings))
	}

	if r.Name == "" {
		fmt.Printf("  %s %s%s\n", status, r.File, warningIndicator)
	} else {
		fmt.Printf("  %s %s (%s)%s\n", status, r.File, r.Name, warningIndicator)
	}

	if !r.Valid {
		for _, e := range r.Errors {
			fmt.Printf("      Error: %s\n", e)
		}
	}
}

func printVerboseValidateResult(r validateResult) {
	fmt.Println("\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
	fmt.Printf("File: %s\n", r.File)

	if r.Name != "" {
		fmt.Printf("Name: %s\n", r.Name)
		fmt.Printf("Nodes: %d, Edges: %d\n", r.Nodes, r.Edges)
	}

	if r.Valid {
		fmt.Println("Status: \u2713 Valid")
	} else {
		fmt.Println("Status: \u2717 Invalid")
		for _, e := range r.Errors {
			fmt.Printf("  Error: %s\n", e)
		}
	}

	if len(r.Warnings) > 0 {
		fmt.Println("Warnings:")
		for _, w := range r.Warnings {
			fmt.Printf("  - %s\n", w)
		}
	}

	fmt.Println()
}

func printValidateSummary(results []validateResult) {
	valid := 0
	invalid := 0
	totalWarnings := 0

	for _, r := range results {
		if r.Valid {
			valid++
		} else {
			invalid++
		}
		totalWarnings += len(r.Warnings)
	}

	fmt.Println()
	fmt.Println("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550")
	fmt.Println("Summary")
	fmt.Println("\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
	fmt.Printf("  Total:    %d workflows\n", len(results))
	fmt.Printf("  Valid:    %d\n", valid)
	fmt.Printf("  Invalid:  %d\n", invalid)
	fmt.Printf("  Warnings: %d\n", totalWarnings)
	fmt.Println("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550")

	if invalid > 0 {
		fmt.Println("\n\u274c Validation failed")
	} else if totalWarnings > 0 {
		fmt.Println("\n\u26a0\ufe0f  Validation passed with warnings")
	} else {
		fmt.Println("\n\u2705 All workflows valid")
	}
}

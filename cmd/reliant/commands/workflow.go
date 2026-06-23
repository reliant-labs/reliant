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
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/auth"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/simulator"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage and validate workflows",
		Long:  `Commands for validating, listing, and running Reliant workflows.`,
	}

	cmd.AddCommand(newWorkflowValidateCmd())
	cmd.AddCommand(newWorkflowValidateTreeCmd())
	cmd.AddCommand(newWorkflowListCmd())
	cmd.AddCommand(newWorkflowRunCmd())
	cmd.AddCommand(newWorkflowScenarioCmd())

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

func newWorkflowValidateTreeCmd() *cobra.Command {
	var (
		workflowDir     string
		presetDir       string
		includeBuiltins bool
		inputs          []string
		inputFile       string
		verboseOutput   bool
		jsonOutput      bool
	)

	cmd := &cobra.Command{
		Use:   "validate-tree <path-or-builtin-ref>",
		Short: "Validate a workflow tree with preset-aware cross-workflow checks",
		Long: `Validates a workflow and all recursively reachable child workflows using
the same static analysis that runs server-side at CreateChat time. Wires a
PresetLoader alongside the WorkflowLoader so preset-param mismatches (for
example a preset setting params that aren't declared inputs on the target
workflow) are surfaced offline.

The reference may be either a filesystem path to a workflow YAML file, or a
builtin reference like "builtin://get-it-right".

Exit code 0 if no errors, 1 if any errors are found.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowValidateTree(cmd, args[0], workflowDir, presetDir, includeBuiltins, inputs, inputFile, verboseOutput, jsonOutput)
		},
	}

	cmd.Flags().StringVar(&presetDir, "preset-dir", ".reliant/presets", "Directory containing project preset YAML files")
	cmd.Flags().StringVar(&workflowDir, "dir", ".reliant/workflows", "Directory containing workflow YAML files")
	cmd.Flags().BoolVar(&includeBuiltins, "include-builtins", true, "Also resolve and validate references into builtin workflows")
	cmd.Flags().StringArrayVarP(&inputs, "input", "i", nil, "Input binding as key=value (repeatable)")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "JSON file containing workflow inputs")
	cmd.Flags().BoolVarP(&verboseOutput, "verbose", "V", false, "Show detailed output")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (for CI)")

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
			dir = path
			f, err := collectYAMLFiles(path)
			if err != nil {
				return err
			}
			files = append(files, f...)
		} else {
			dir = filepath.Dir(path)
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
		result := validateWorkflowFile(file, dir)
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

func buildCLIWorkflowLoader(dir string) runtime.WorkflowLoader {
	return func(ref string) (*reliantv1.Workflow, error) {
		name := ref
		if strings.HasPrefix(name, "builtin://") {
			name = strings.TrimPrefix(name, "builtin://")
		}

		// Try builtin embedded FS first
		if data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml"); err == nil {
			return wfyaml.ParseWorkflow(data)
		}

		// Fall back to local workflow directory
		if dir != "" {
			localPath := filepath.Join(dir, name+".yaml")
			if data, err := os.ReadFile(localPath); err == nil {
				return wfyaml.ParseWorkflow(data)
			}
		}

		// Not found — allow validation to continue gracefully
		return nil, nil
	}
}

func validateWorkflowFile(path string, workflowDir string) validateResult {
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

	// Run full validation with cross-workflow loader
	loader := buildCLIWorkflowLoader(workflowDir)
	valResult, valErr := runtime.ValidateYAMLResult(data, loader)
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

// --- workflow validate-tree implementation ---

func runWorkflowValidateTree(cmd *cobra.Command, ref, workflowDir, presetDir string, includeBuiltins bool, inputs []string, inputFile string, verbose, jsonOut bool) error {
	// Resolve the workflow YAML: either a path or a builtin:// ref.
	data, displayName, err := loadWorkflowTreeSource(ref)
	if err != nil {
		return err
	}

	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		return fmt.Errorf("parse workflow: %w", err)
	}

	// Build loaders.
	var wfLoader validation.WorkflowLoader
	baseLoader := buildCLIWorkflowLoader(workflowDir)
	if includeBuiltins {
		wfLoader = func(r string) (*reliantv1.Workflow, error) {
			return baseLoader(r)
		}
	} else {
		wfLoader = func(r string) (*reliantv1.Workflow, error) {
			if strings.HasPrefix(r, "builtin://") {
				return nil, nil
			}
			return baseLoader(r)
		}
	}

	presetLoader := buildCLIPresetLoader(presetDir)

	// Run static analysis with both loaders wired.
	opts := &validation.ValidationOptions{
		WorkflowLoader:       wfLoader,
		PresetLoader:         presetLoader,
		CanonicalWorkflowRef: canonicalWorkflowRef(ref),
	}
	staticResult := validation.StaticAnalysisWithOptions(wf, opts)

	result := validateResult{
		File:  displayName,
		Name:  wf.GetName(),
		Nodes: len(wf.GetNodes()),
		Edges: len(wf.GetEdges()),
	}
	if staticResult != nil {
		for _, e := range staticResult.Errors() {
			result.Errors = append(result.Errors, e.Error())
		}
		for _, w := range staticResult.Warnings() {
			result.Warnings = append(result.Warnings, w.Error())
		}
	}

	// Optional input binding validation.
	if len(inputs) > 0 || inputFile != "" {
		inputMap, ierr := buildCLIInputMap(inputs, inputFile)
		if ierr != nil {
			return ierr
		}
		inputResult := validation.ValidateInputs(wf, inputMap)
		if inputResult != nil {
			for _, e := range inputResult.Errors() {
				result.Errors = append(result.Errors, e.Error())
			}
			for _, w := range inputResult.Warnings() {
				result.Warnings = append(result.Warnings, w.Error())
			}
		}
	}

	result.Valid = len(result.Errors) == 0

	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("failed to encode JSON: %w", err)
		}
	} else {
		if verbose {
			printVerboseValidateResult(result)
		} else {
			printCompactValidateResult(result)
		}
		printValidateSummary([]validateResult{result})
	}

	if !result.Valid {
		return fmt.Errorf("validation failed")
	}
	return nil
}

// loadWorkflowTreeSource reads the workflow YAML from either a file path or a
// builtin:// reference and returns the data plus a display label.
func loadWorkflowTreeSource(ref string) ([]byte, string, error) {
	if strings.HasPrefix(ref, "builtin://") {
		name := strings.TrimPrefix(ref, "builtin://")
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml")
		if err != nil {
			return nil, "", fmt.Errorf("builtin workflow not found: %s", ref)
		}
		return data, ref, nil
	}

	data, err := os.ReadFile(ref)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read %s: %w", ref, err)
	}
	return data, filepath.Base(ref), nil
}

// canonicalWorkflowRef returns a loadable ref suitable for
// ValidationOptions.CanonicalWorkflowRef. For builtin:// refs we preserve the
// ref; for filesystem paths we leave it empty so validation falls back to
// wf.name.
func canonicalWorkflowRef(ref string) string {
	if strings.HasPrefix(ref, "builtin://") {
		return ref
	}
	return ""
}

// buildCLIPresetLoader constructs a validation.PresetLoader backed by the
// project preset directory (falls back to builtin presets via preset.Loader).
func buildCLIPresetLoader(presetDir string) validation.PresetLoader {
	loader := preset.NewLoader(presetDir)
	return func(name string) (map[string]interface{}, error) {
		p, err := loader.Load(name)
		if err != nil {
			return nil, err
		}
		return p.Params, nil
	}
}

// buildCLIInputMap merges --input key=value pairs and --input-file JSON into a
// single map, parsing values that look like JSON literals.
func buildCLIInputMap(inputs []string, inputFile string) (map[string]interface{}, error) {
	inputMap := make(map[string]interface{})

	if inputFile != "" {
		data, err := os.ReadFile(inputFile)
		if err != nil {
			return nil, fmt.Errorf("reading input file: %w", err)
		}
		if err := json.Unmarshal(data, &inputMap); err != nil {
			return nil, fmt.Errorf("parsing input file: %w", err)
		}
	}

	for _, kv := range inputs {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid input format %q \u2014 expected key=value", kv)
		}
		inputMap[parts[0]] = parseCLIInputValue(parts[1])
	}

	return inputMap, nil
}

// parseCLIInputValue attempts to parse the raw value as JSON if it looks like a
// JSON literal; otherwise returns the raw string.
func parseCLIInputValue(raw string) interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	first := trimmed[0]
	looksJSON := first == '{' || first == '[' || first == '"' || first == '-' || (first >= '0' && first <= '9')
	if !looksJSON {
		switch trimmed {
		case "true", "false", "null":
			looksJSON = true
		}
	}
	if !looksJSON {
		return raw
	}
	var v interface{}
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return raw
	}
	return v
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

// --- workflow scenario commands ---

func newWorkflowScenarioCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scenario",
		Short: "Run and manage workflow scenarios",
		Long:  `Commands for running and listing workflow scenario tests.`,
	}

	cmd.AddCommand(newWorkflowScenarioRunCmd())
	cmd.AddCommand(newWorkflowScenarioListCmd())

	return cmd
}

func newWorkflowScenarioRunCmd() *cobra.Command {
	var (
		workflowDir     string
		verboseOutput   bool
		failFast        bool
		jsonOutput      bool
		includeBuiltins bool
		filterScenario  string
	)

	cmd := &cobra.Command{
		Use:   "run [workflow-path]",
		Short: "Run scenario tests against workflows",
		Long: `Runs scenario tests against workflow definitions using the simulator engine.
Scenarios are discovered from co-located *_scenarios.yaml files or from
scenarios/<workflow-name>/ directories.

If a specific workflow file is given, runs scenarios for that workflow only.
Otherwise, discovers all workflows in the workflow directory and runs their
associated scenarios.

Examples:
  reliant workflow scenario run                               # run all project scenarios
  reliant workflow scenario run my-workflow.yaml              # run scenarios for one workflow
  reliant workflow scenario run --include-builtins            # include builtin workflow scenarios
  reliant workflow scenario run --filter happy_path           # run only matching scenarios
  reliant workflow scenario run --json                        # JSON output for CI

Exit code 0 if all scenarios pass, 1 if any fail.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowScenarios(cmd, args, workflowDir, verboseOutput, failFast, jsonOutput, includeBuiltins, filterScenario)
		},
	}

	cmd.Flags().StringVar(&workflowDir, "dir", ".reliant/workflows", "Directory containing workflow YAML files")
	cmd.Flags().BoolVarP(&verboseOutput, "verbose", "V", false, "Show detailed output for each scenario")
	cmd.Flags().BoolVar(&failFast, "fail-fast", false, "Stop on first scenario failure")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format (for CI)")
	cmd.Flags().BoolVar(&includeBuiltins, "include-builtins", false, "Also run builtin workflow scenarios")
	cmd.Flags().StringVar(&filterScenario, "filter", "", "Only run scenarios whose name contains this string")

	return cmd
}

func newWorkflowScenarioListCmd() *cobra.Command {
	var (
		workflowDir     string
		jsonOutput      bool
		includeBuiltins bool
	)

	cmd := &cobra.Command{
		Use:   "list [workflow-path]",
		Short: "List available scenarios for workflows",
		Long: `Lists scenarios discovered from co-located *_scenarios.yaml files or from
scenarios/<workflow-name>/ directories.

Examples:
  reliant workflow scenario list                               # list all project scenarios
  reliant workflow scenario list my-workflow.yaml              # list scenarios for one workflow
  reliant workflow scenario list --include-builtins            # include builtin workflow scenarios`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflowScenarioList(cmd, args, workflowDir, jsonOutput, includeBuiltins)
		},
	}

	cmd.Flags().StringVar(&workflowDir, "dir", ".reliant/workflows", "Directory containing workflow YAML files")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	cmd.Flags().BoolVar(&includeBuiltins, "include-builtins", false, "Also list builtin workflow scenarios")

	return cmd
}

// workflowWithScenarios pairs a workflow file with its discovered scenarios.
type workflowWithScenarios struct {
	WorkflowFile string
	WorkflowName string
	Source       string // "project" or "builtin"
	Scenarios    []*simulator.Scenario
}

// scenarioRunResult captures the result of running scenarios for one workflow.
type scenarioRunResult struct {
	Workflow  string                  `json:"workflow"`
	File      string                  `json:"file"`
	Source    string                  `json:"source"`
	Scenarios []scenarioResultSummary `json:"scenarios"`
}

type scenarioResultSummary struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	DurationMs int64    `json:"duration_ms"`
	Mismatches []string `json:"mismatches,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type scenarioListEntry struct {
	Workflow    string `json:"workflow"`
	Source      string `json:"source"`
	Scenario    string `json:"scenario"`
	Description string `json:"description,omitempty"`
	Events      int    `json:"events"`
	HasExpect   bool   `json:"has_expect"`
}

func runWorkflowScenarios(_ *cobra.Command, args []string, dir string, verbose, failFast, jsonOut, includeBuiltins bool, filter string) error {
	workflows, err := discoverWorkflowsWithScenarios(args, dir, includeBuiltins)
	if err != nil {
		return err
	}

	if len(workflows) == 0 {
		if jsonOut {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No workflows with scenarios found")
		return nil
	}

	totalScenarios := 0
	for _, wf := range workflows {
		totalScenarios += len(wf.Scenarios)
	}

	if !jsonOut {
		fmt.Printf("Running %d scenario(s) across %d workflow(s)\n\n", totalScenarios, len(workflows))
	}

	var allResults []scenarioRunResult
	hasFailures := false
	stopped := false

	for _, wf := range workflows {
		if stopped {
			break
		}

		// Load the workflow proto
		engine, err := loadWorkflowProto(wf)
		if err != nil {
			result := scenarioRunResult{
				Workflow: wf.WorkflowName,
				File:     filepath.Base(wf.WorkflowFile),
				Source:   wf.Source,
				Scenarios: []scenarioResultSummary{{
					Name:   "(load)",
					Status: string(simulator.StatusError),
					Error:  err.Error(),
				}},
			}
			allResults = append(allResults, result)
			hasFailures = true
			if !jsonOut {
				fmt.Printf("  \u2717 %s: failed to load workflow: %v\n", wf.WorkflowName, err)
			}
			if failFast {
				stopped = true
			}
			continue
		}
		wfResult := scenarioRunResult{
			Workflow: wf.WorkflowName,
			File:     filepath.Base(wf.WorkflowFile),
			Source:   wf.Source,
		}

		printedHeader := false
		for _, scenario := range wf.Scenarios {
			if stopped {
				break
			}

			// Apply filter
			if filter != "" && !strings.Contains(scenario.Name, filter) {
				continue
			}

			if !jsonOut && verbose && !printedHeader {
				fmt.Printf("\u2500\u2500\u2500 %s (%s) \u2500\u2500\u2500\n", wf.WorkflowName, wf.Source)
				printedHeader = true
			}

			start := time.Now()
			result := engine.RunScenario(scenario)
			duration := time.Since(start).Milliseconds()

			summary := scenarioResultSummary{
				Name:       scenario.Name,
				Status:     string(result.Status),
				DurationMs: duration,
			}

			if result.Status != simulator.StatusPassed {
				hasFailures = true
				summary.Mismatches = result.Mismatches
				if result.Execution.Error != nil {
					summary.Error = result.Execution.Error.Message
				}
			}

			wfResult.Scenarios = append(wfResult.Scenarios, summary)

			if !jsonOut {
				printScenarioResult(summary, wf.WorkflowName, verbose)
			}

			if result.Status != simulator.StatusPassed && failFast {
				stopped = true
			}
		}

		allResults = append(allResults, wfResult)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(allResults); err != nil {
			return fmt.Errorf("failed to encode JSON: %w", err)
		}
	} else {
		printScenarioSummary(allResults)
	}

	if hasFailures {
		return fmt.Errorf("scenarios failed")
	}
	return nil
}

func runWorkflowScenarioList(_ *cobra.Command, args []string, dir string, jsonOut, includeBuiltins bool) error {
	workflows, err := discoverWorkflowsWithScenarios(args, dir, includeBuiltins)
	if err != nil {
		return err
	}

	var entries []scenarioListEntry
	for _, wf := range workflows {
		for _, s := range wf.Scenarios {
			entries = append(entries, scenarioListEntry{
				Workflow:    wf.WorkflowName,
				Source:      wf.Source,
				Scenario:    s.Name,
				Description: s.Description,
				Events:      len(s.Events),
				HasExpect:   s.Expect != nil,
			})
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	if len(entries) == 0 {
		fmt.Println("No scenarios found")
		return nil
	}

	fmt.Fprintf(os.Stdout, "%-24s %-10s %-36s %6s\n", "WORKFLOW", "SOURCE", "SCENARIO", "EVENTS")
	fmt.Fprintf(os.Stdout, "%-24s %-10s %-36s %6s\n", "\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500", "\u2500\u2500\u2500\u2500\u2500\u2500", "\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500", "\u2500\u2500\u2500\u2500\u2500\u2500")
	for _, e := range entries {
		fmt.Fprintf(os.Stdout, "%-24s %-10s %-36s %6d\n", e.Workflow, e.Source, e.Scenario, e.Events)
	}
	fmt.Fprintf(os.Stdout, "\n%d scenario(s) across %d workflow(s)\n", len(entries), len(workflows))

	return nil
}

// discoverWorkflowsWithScenarios finds all workflow + scenario pairings.
func discoverWorkflowsWithScenarios(args []string, dir string, includeBuiltins bool) ([]workflowWithScenarios, error) {
	var results []workflowWithScenarios

	// Collect project workflow files
	var projectFiles []string
	if len(args) == 1 {
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("cannot access %s: %w", path, err)
		}
		if info.IsDir() {
			f, err := collectYAMLFiles(path)
			if err != nil {
				return nil, err
			}
			projectFiles = append(projectFiles, f...)
		} else {
			projectFiles = append(projectFiles, path)
		}
	} else {
		resolvedDir := dir
		if !filepath.IsAbs(resolvedDir) {
			cwd, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("could not get working directory: %w", err)
			}
			resolvedDir = filepath.Join(cwd, resolvedDir)
		}
		f, err := collectYAMLFiles(resolvedDir)
		if err == nil {
			projectFiles = append(projectFiles, f...)
		}
	}

	// For each project workflow, find associated scenarios
	for _, wfFile := range projectFiles {
		scenarios := findScenariosForWorkflow(wfFile)
		if len(scenarios) == 0 {
			continue
		}

		wfName := workflowNameFromFile(wfFile)
		results = append(results, workflowWithScenarios{
			WorkflowFile: wfFile,
			WorkflowName: wfName,
			Source:       "project",
			Scenarios:    scenarios,
		})
	}

	// Include builtins
	if includeBuiltins {
		builtinResults := discoverBuiltinScenarios()
		results = append(results, builtinResults...)
	}

	return results, nil
}

// findScenariosForWorkflow discovers scenarios for a given workflow file.
// Checks:
// 1. Co-located <name>_scenarios.yaml
// 2. scenarios/<name>/ directory
func findScenariosForWorkflow(workflowFile string) []*simulator.Scenario {
	dir := filepath.Dir(workflowFile)
	base := filepath.Base(workflowFile)
	name := strings.TrimSuffix(base, filepath.Ext(base))

	var allScenarios []*simulator.Scenario

	// Check co-located <name>_scenarios.yaml
	for _, ext := range []string{".yaml", ".yml"} {
		colocated := filepath.Join(dir, name+"_scenarios"+ext)
		if scenarios, err := simulator.LoadScenariosFromFile(colocated); err == nil {
			allScenarios = append(allScenarios, scenarios...)
		}
	}

	// Check scenarios/<name>/ directory
	scenarioDir := filepath.Join(dir, "scenarios", name)
	if info, err := os.Stat(scenarioDir); err == nil && info.IsDir() {
		if scenarios, err := simulator.LoadScenariosFromDir(scenarioDir); err == nil {
			allScenarios = append(allScenarios, scenarios...)
		}
	}

	return allScenarios
}

// discoverBuiltinScenarios loads scenarios for all builtin workflows from the embedded FS.
func discoverBuiltinScenarios() []workflowWithScenarios {
	var results []workflowWithScenarios

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || (!strings.HasSuffix(entry.Name(), ".yaml") && !strings.HasSuffix(entry.Name(), ".yml")) {
			continue
		}

		wfName := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		scenarioFile := "testdata/" + wfName + "_scenarios.yaml"

		data, err := builtin.BuiltinScenariosFS.ReadFile(scenarioFile)
		if err != nil {
			continue
		}

		scenarios, err := simulator.ParseScenarioYAML(data)
		if err != nil || len(scenarios) == 0 {
			continue
		}

		results = append(results, workflowWithScenarios{
			WorkflowFile: entry.Name(),
			WorkflowName: wfName,
			Source:       "builtin",
			Scenarios:    scenarios,
		})
	}

	return results
}

// loadWorkflowProto loads a workflow into its proto representation.
// Handles both project files (from disk) and builtins (from embedded FS).
func loadWorkflowProto(wf workflowWithScenarios) (*simulator.Engine, error) {
	var data []byte
	var err error

	if wf.Source == "builtin" {
		data, err = builtin.BuiltinWorkflowsFS.ReadFile(wf.WorkflowFile)
	} else {
		data, err = os.ReadFile(wf.WorkflowFile)
	}
	if err != nil {
		return nil, fmt.Errorf("reading workflow: %w", err)
	}

	parsedWf, err := runtime.ParseWorkflowProtoBytes(data)
	if err != nil {
		return nil, fmt.Errorf("parsing workflow: %w", err)
	}

	return simulator.NewEngine(parsedWf), nil
}

// workflowNameFromFile extracts a workflow name from the YAML, falling back to filename.
func workflowNameFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil || wf.GetName() == "" {
		return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	return wf.GetName()
}

func printScenarioResult(s scenarioResultSummary, workflowName string, verbose bool) {
	switch simulator.ScenarioStatus(s.Status) {
	case simulator.StatusPassed:
		fmt.Printf("  \u2713 %s/%s (%dms)\n", workflowName, s.Name, s.DurationMs)
	case simulator.StatusFailed:
		fmt.Printf("  \u2717 %s/%s (%dms)\n", workflowName, s.Name, s.DurationMs)
		if verbose {
			for _, m := range s.Mismatches {
				fmt.Printf("      Mismatch: %s\n", m)
			}
		} else if len(s.Mismatches) > 0 {
			fmt.Printf("      %s\n", s.Mismatches[0])
			if len(s.Mismatches) > 1 {
				fmt.Printf("      ... and %d more (use --verbose)\n", len(s.Mismatches)-1)
			}
		}
	case simulator.StatusError:
		fmt.Printf("  ! %s/%s (%dms)\n", workflowName, s.Name, s.DurationMs)
		if s.Error != "" {
			fmt.Printf("      Error: %s\n", s.Error)
		}
		if verbose {
			for _, m := range s.Mismatches {
				fmt.Printf("      %s\n", m)
			}
		}
	}
}

func printScenarioSummary(results []scenarioRunResult) {
	passed, failed, errored := 0, 0, 0
	for _, wf := range results {
		for _, s := range wf.Scenarios {
			switch simulator.ScenarioStatus(s.Status) {
			case simulator.StatusPassed:
				passed++
			case simulator.StatusFailed:
				failed++
			case simulator.StatusError:
				errored++
			}
		}
	}
	total := passed + failed + errored

	fmt.Println()
	fmt.Println("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550")
	fmt.Println("Scenario Summary")
	fmt.Println("\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
	fmt.Printf("  Total:   %d scenarios\n", total)
	fmt.Printf("  Passed:  %d\n", passed)
	fmt.Printf("  Failed:  %d\n", failed)
	fmt.Printf("  Errors:  %d\n", errored)
	fmt.Println("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550")

	if failed+errored > 0 {
		fmt.Println("\n\u274c Scenarios failed")
	} else {
		fmt.Println("\n\u2705 All scenarios passed")
	}
}

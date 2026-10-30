// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/repo"
	skillscatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/lint"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
	wfscenario "github.com/reliant-labs/reliant/internal/workflow/scenario"
	"github.com/reliant-labs/reliant/internal/workflow/scenario/runner"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Manage and validate workflows",
		Long:  `Commands for validating, listing, and running Reliant workflows.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	cmd.AddCommand(newWorkflowValidateCmd())
	cmd.AddCommand(newWorkflowLintCmd())
	cmd.AddCommand(newWorkflowValidateTreeCmd())
	cmd.AddCommand(newWorkflowEvalCmd())
	cmd.AddCommand(newWorkflowListCmd())
	cmd.AddCommand(newWorkflowRunCmd())
	cmd.AddCommand(newWorkflowFollowCmd())
	cmd.AddCommand(newWorkflowScenarioCmd())

	// Live supervision surface (Connect RPCs + context credential; no DB).
	cmd.AddCommand(newWorkflowWatchCmd())
	cmd.AddCommand(newWorkflowWaitForGateCmd())
	cmd.AddCommand(newWorkflowStatusCmd())
	cmd.AddCommand(newWorkflowQuestionsCmd())
	cmd.AddCommand(newWorkflowAnswerCmd())
	cmd.AddCommand(newWorkflowTerminateCmd())
	cmd.AddCommand(newWorkflowPauseCmd())
	cmd.AddCommand(newWorkflowResumeCmd())

	// ps, node, analyze and forensics read the database directly, so they are
	// not part of this CLI — they live in tools/reliant-dev.

	return cmd
}

func newWorkflowValidateCmd() *cobra.Command {
	var (
		workflowDir     string
		verboseValidate bool
		failFast        bool
		jsonOutput      bool
		includeBuiltins bool
		triggerPayload  string
		triggerSender   string
		activationParam string
		ownerGitHubID   string
		base            string
	)

	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate workflow YAML files",
		Long: `Validates workflow YAML files against the Reliant workflow schema. Runs
static analysis including structural validation, CEL expression checking,
input/output type verification, and cross-workflow contract validation.

If a specific file path is given, validates that file only. Otherwise,
validates all *.yaml files in the workflow directory.

With --trigger-payload, each declared trigger is also run against that sample
event: its filter, its inputs mapping and its prompt are evaluated the way a
real firing would, and anything that fails (or that the integration's declared
payload rules out) is reported as an error. For an integration trigger the
payload is merged over a sample generated from the event's declared payload, so
give only the fields your expressions read. The JSON may be a file: @event.json.
--activation-params supplies trigger.activation.params, as an activation of the workflow
would set them (params it leaves out read as the workflow's input defaults), and
--owner-github-id trigger.activation.owner.github_id.

With --base <git revision> (in CI, the branch the change merges into), the
declared triggers are also compared with the ones at that revision. An edit that
existing activations cannot follow — a workflow with triggers deleted, a trigger
renamed or removed, a trigger changed to another kind or integration — is
reported under "Breaks activations": each activation of it would turn BROKEN
and fire nothing. These are warnings, since the CLI cannot see whether anyone
activated the trigger; the server lists any enabled activation a synced change
broke in its owner's Inbox.

Each project workflow is also linted (reliant workflow lint --rules lists the
rules): a lint error fails validation, a lint warning is reported.

Exit code 0 if all workflows are valid, 1 if any errors are found.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sample, err := triggerSampleFromFlags(triggerPayload, triggerSender, activationParam, ownerGitHubID)
			if err != nil {
				return err
			}
			return runWorkflowValidate(cmd, args, workflowDir, verboseValidate, failFast, jsonOutput, includeBuiltins, sample, base)
		},
	}
	cmd.Flags().StringVar(&triggerPayload, "trigger-payload", "", "Sample trigger.payload (JSON or @file.json): also run each declared trigger's filter, inputs and prompt against it")
	cmd.Flags().StringVar(&triggerSender, "trigger-sender", "", "Sample trigger.sender (JSON: kind, id, display_name, verified), used with --trigger-payload")
	cmd.Flags().StringVar(&activationParam, "activation-params", "", "Sample trigger.activation.params (JSON or @file.json), used with --trigger-payload")
	cmd.Flags().StringVar(&ownerGitHubID, "owner-github-id", "", "Sample trigger.activation.owner.github_id (a string), used with --trigger-payload")
	cmd.Flags().StringVar(&base, "base", "", "Git revision the workflows are changing from: also report trigger changes that would break existing activations")

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
the same static analysis that runs server-side at StartChat time. Wires a
PresetLoader alongside the WorkflowLoader so preset-param mismatches (for
example a preset setting params that aren't declared inputs on the target
workflow) are surfaced offline.

The reference may be a filesystem path to a workflow YAML file, a builtin
reference like "builtin://get-it-right", or a project reference like
"project://deploy", which names a workflow in --dir by its name: field (the
way the app resolves it — never by file name).

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
				files, err := workflowFilesIn(filepath.Join(cwd, filepath.FromSlash(workflowref.Dir)))
				if err != nil {
					return err
				}
				for _, f := range files {
					workflows = append(workflows, parseWorkflowInfo(f, "project"))
				}
			}

			// Collect builtin workflows
			if !projectOnly {
				files, err := builtinWorkflowFiles()
				if err != nil {
					return err
				}
				for _, f := range files {
					workflows = append(workflows, workflowInfoFrom(f.name, f.data, "builtin"))
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
	data, err := os.ReadFile(path)
	if err != nil {
		return workflowListInfo{Name: filepath.Base(path), File: filepath.Base(path), Source: source}
	}
	return workflowInfoFrom(filepath.Base(path), data, source)
}

func workflowInfoFrom(file string, data []byte, source string) workflowListInfo {
	info := workflowListInfo{
		File:   file,
		Source: source,
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
		inputs      []string
		inputFile   string
		message     string
		title       string
		follow      bool
		projectID   string
		projectPath string
		followFlags followFlags
	)

	cmd := &cobra.Command{
		Use:   "run <workflow-name>",
		Short: "Run a workflow",
		Long: `Triggers a workflow execution by creating a chat bound to the workflow,
via the Reliant ChatService.StartChat Connect RPC. A run IS a chat: sending
the first user message kicks the root workflow.

The run is recorded as a command-line run (launch kind cli.run): it is listed
under Runs, not among your conversations in the sidebar, the same as a run an
agent starts with start_run. Open it from Runs, or by the chat id printed
below; adopting it moves it into the sidebar. Its title is --title, else
"<workflow> · <first line of the message>", else "<workflow> · <start time>".

The target server is --server (else RELIANT_SERVER_URL, else the default);
the bearer is RELIANT_TOKEN, else the login 'reliant auth login' stored for
that server. Either way it is an rlat_ access token.

A run executes against a project. Supply it by ID (--project-id) or by path
(--project-path); with --project-path the project is resolved by its path —
reused if one already exists there, or created on the fly — so no manual
project setup is needed. --project-id takes precedence when both are given.

Bare workflow names that match a builtin are normalized to builtin://<name>
(like the web app); other names resolve as drafts / project workflows
server-side. The first user message is taken from --message, then
inputs.message, then inputs.prompt, then a generic kick message. All --input
values land on the workflow_params plane (message/prompt excluded).

With --follow, streams NDJSON lifecycle events until the workflow reaches
a terminal state (see 'reliant workflow follow --help' for the event
format, exit codes, and --hook).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowName := args[0]

			// Resolve server + credentials (context-aware; legacy JWT fallback)
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			// Resolve the project. --project-id wins; otherwise --project-path
			// finds-or-creates a project by path so no manual CreateProject is
			// needed.
			resolvedProjectID := projectID
			if resolvedProjectID == "" && projectPath != "" {
				resolvedProjectID, err = resolveProjectPathToID(cmd.Context(), conn, projectPath)
				if err != nil {
					return err
				}
			}
			if resolvedProjectID == "" {
				return fmt.Errorf("a project is required — pass --project-id or --project-path")
			}

			// Build input map: --input flags then --input-file overlay.
			inputMap := make(map[string]interface{})
			for _, kv := range inputs {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid input format %q — expected key=value", kv)
				}
				// Read as `workflow validate --input` reads it: asap=true is
				// a boolean and every=10 a number, which a boolean or number
				// input requires (the server never parses text into one).
				inputMap[parts[0]] = parseCLIInputValue(parts[1])
			}
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

			params, err := workflowParamsFromInputs(inputMap)
			if err != nil {
				return err
			}

			workflowRef := normalizeRunWorkflowRef(workflowName)
			runMessage, messageGiven := resolveRunMessage(workflowName, message, inputMap)
			createReq := &reliantv1.StartChatRequest{
				ProjectId: resolvedProjectID,
				Workflow:  workflowRef,
				Messages: []*reliantv1.InputMessage{{
					Role:    reliantv1.MessageRole_MESSAGE_ROLE_USER,
					Content: runMessage,
				}},
				WorkflowParams: params,
				CliRun:         proto.Bool(true),
			}
			switch {
			case title != "":
				createReq.Title = proto.String(title)
			case !messageGiven:
				// The kick message is not a subject; without one the server's
				// "<workflow> · <first line>" would title every run with it.
				createReq.Title = proto.String(launch.RunTitle(workflowRef, "", time.Now()))
			}

			// Connect client authenticated by the context bearer, mirroring how
			// `reliant workflow follow` builds its ChatService client.
			chatClient := reliantv1connect.NewChatServiceClient(conn.httpClient(), conn.ServerURL)

			resp, err := chatClient.StartChat(cmd.Context(), connect.NewRequest(createReq))
			if err != nil {
				return conn.annotate(fmt.Errorf("starting workflow: %w", err))
			}

			// chat_id == execution_id: the follow surface is chat-scoped.
			chatID := resp.Msg.GetChat().GetId()
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Workflow %q triggered\n", workflowName)
			if chatID != "" {
				fmt.Fprintf(out, "  Chat/Execution ID: %s\n", chatID)
			}
			if t := resp.Msg.GetChat().GetTitle(); t != "" {
				fmt.Fprintf(out, "  Title:             %s (listed under Runs)\n", t)
			}
			if wid := resp.Msg.GetWorkflowId(); wid != "" {
				fmt.Fprintf(out, "  Workflow ID:       %s\n", wid)
			}
			if rid := resp.Msg.GetRunId(); rid != "" {
				fmt.Fprintf(out, "  Run ID:            %s\n", rid)
			}

			if follow {
				if chatID == "" {
					return fmt.Errorf("cannot follow: StartChat did not return a chat ID")
				}
				return runWorkflowFollow(cmd, chatID, &followFlags)
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVarP(&inputs, "input", "i", nil, "Workflow input as key=value (repeatable)")
	cmd.Flags().StringVar(&inputFile, "input-file", "", "JSON file containing workflow inputs")
	cmd.Flags().StringVarP(&message, "message", "m", "", "First chat message that kicks the workflow (falls back to inputs.message/prompt, then a default)")
	cmd.Flags().StringVar(&title, "title", "", "Title for the run (default: \"<workflow> · <first line of the message>\")")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow the execution and stream NDJSON lifecycle events")
	cmd.Flags().StringVar(&projectID, "project-id", "", "Project ID (takes precedence over --project-path)")
	cmd.Flags().StringVar(&projectPath, "project-path", "", "Project directory path; resolves (find-or-create) to a project ID when --project-id is absent")
	followFlags.register(cmd)

	return cmd
}

// reservedMessageKeys are inputs that belong to the chat-message plane
// (consumed by resolveRunMessage), not the workflow_params plane. Passing them
// through as params would fail a workflow's strict unknown-input validation
// (e.g. `-i message=…` on a workflow that declares no `message` input), so
// they are stripped when building workflow_params.
var reservedMessageKeys = map[string]bool{"message": true, "prompt": true}

// normalizeRunWorkflowRef maps bare builtin names ("forge-one-shot") to the
// builtin://<name> refs the web app sends. Refs that already carry a scheme,
// and names that do not match a builtin (user drafts / project workflows,
// which StartChat resolves by slug), pass through unchanged.
func normalizeRunWorkflowRef(name string) string {
	if strings.Contains(name, "://") {
		return name
	}
	if _, err := builtin.BuiltinWorkflowsFS.ReadFile(name + ".yaml"); err == nil {
		return "builtin://" + name
	}
	return name
}

// resolveRunMessage picks the chat's first user message: explicit --message,
// then inputs.message, then inputs.prompt, then a generic kick message
// (StartChat requires at least one user message; input-driven workflows read
// their params, not the message). given is false for the kick message, which
// names no subject.
func resolveRunMessage(workflowName, explicit string, inputs map[string]interface{}) (message string, given bool) {
	if explicit != "" {
		return explicit, true
	}
	for _, key := range []string{"message", "prompt"} {
		if s, ok := inputs[key].(string); ok && s != "" {
			return s, true
		}
	}
	return fmt.Sprintf("Run the %s workflow with the provided inputs.", workflowName), false
}

// workflowParamsFromInputs converts the CLI input map to the proto
// workflow_params plane, excluding the reserved message-plane keys. Values
// from --input are strings; --input-file may carry arbitrary JSON. structpb
// conversion only fails on values JSON can't represent.
func workflowParamsFromInputs(inputs map[string]interface{}) (map[string]*structpb.Value, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	params := make(map[string]*structpb.Value, len(inputs))
	for k, v := range inputs {
		if reservedMessageKeys[k] {
			continue
		}
		pv, err := structpb.NewValue(v)
		if err != nil {
			return nil, fmt.Errorf("input %q is not representable: %v", k, err)
		}
		params[k] = pv
	}
	return params, nil
}

// --- workflow validate implementation ---

type validateResult struct {
	File     string   `json:"file"`
	Name     string   `json:"name,omitempty"`
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// ActivationBreaks are the declared-trigger changes since --base that
	// existing activations cannot follow (activationBreaks). Warnings, kept
	// apart so they are always printed.
	ActivationBreaks []string `json:"activation_breaks,omitempty"`
	// Lint is what `reliant workflow lint` finds in a project workflow; an
	// error-severity finding makes the result invalid.
	Lint  []lint.Finding `json:"lint,omitempty"`
	Nodes int            `json:"nodes,omitempty"`
	Edges int            `json:"edges,omitempty"`
}

func runWorkflowValidate(_ *cobra.Command, args []string, dir string, verbose, failFast, jsonOut, includeBuiltins bool, sample *triggerSample, base string) error {
	var files []string

	// Single file or directory from positional arg
	singleFile := false
	if len(args) == 1 {
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("cannot access %s: %w", path, err)
		}
		if info.IsDir() {
			dir = path
			f, err := workflowFilesIn(path)
			if err != nil {
				return err
			}
			files = append(files, f...)
		} else {
			dir = filepath.Dir(path)
			files = append(files, path)
			singleFile = true
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
		f, err := workflowFilesIn(dir)
		if err != nil {
			return err
		}
		files = append(files, f...)
	}

	// Builtins come from the binary, not from a source checkout.
	var builtins []embeddedWorkflow
	if includeBuiltins {
		var err error
		if builtins, err = builtinWorkflowFiles(); err != nil {
			return err
		}
	}

	// Refs resolve against the project the files belong to, read once.
	layout, err := readWorkflowLayout(dir)
	if err != nil {
		return err
	}

	// What the edit since --base breaks, by workflow name; a deleted workflow
	// is its own result (a single-file run is not about the others).
	var breaks map[string][]string
	var deleted []validateResult
	if base != "" {
		if breaks, deleted, err = activationBreaks(dir, base, layout); err != nil {
			return err
		}
		if singleFile {
			deleted = nil
		}
	}

	total := len(files) + len(builtins) + len(deleted)
	if total == 0 {
		if jsonOut {
			fmt.Println("[]")
			return nil
		}
		fmt.Println("No workflow files found")
		return nil
	}

	if !jsonOut {
		fmt.Printf("Validating %d workflow(s)\n\n", total)
	}

	var results []validateResult
	hasErrors := false

	// record reports one result and says whether to stop (--fail-fast).
	record := func(result validateResult) (stop bool) {
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
			return failFast
		}
		return false
	}

	stopped := false
	for _, file := range files {
		result := validateWorkflowFileIn(file, dir, layout)
		dryRunTriggers(&result, file, sample)
		if result.Name != "" {
			result.ActivationBreaks = breaks[result.Name]
		}
		if stopped = record(result); stopped {
			break
		}
	}
	for _, d := range deleted {
		if stopped {
			break
		}
		stopped = record(d)
	}
	for _, b := range builtins {
		if stopped {
			break
		}
		result := validateWorkflowYAML(b.data, b.name, nil, layout)
		dryRunTriggersOn(&result, b.data, sample)
		stopped = record(result)
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

// readWorkflowLayout reads a workflows directory the way the daemon reads a
// project's .reliant/workflows for the app (workflowref.ReadLayout), so the
// CLI sees exactly the workflows and scenarios the app does.
func readWorkflowLayout(dir string) (*workflowref.Layout, error) {
	var layout *workflowref.Layout
	conventional := filepath.Base(filepath.Dir(filepath.Clean(dir))) == ".reliant" && filepath.Base(filepath.Clean(dir)) == "workflows"
	if conventional {
		var err error
		layout, err = workflowref.ReadSourceLayout(context.Background(), filepath.Dir(filepath.Dir(filepath.Clean(dir))))
		if err != nil {
			return nil, err
		}
	} else {
		root, err := os.OpenRoot(dir)
		if os.IsNotExist(err) {
			return workflowref.NewLayout(nil), nil
		}
		if err != nil {
			return nil, err
		}
		layout, err = workflowref.ReadLayout(root.FS())
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close workflow directory %s: %w", dir, closeErr))
		}
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", dir, err)
		}
	}
	// An explicit arbitrary --dir remains one directory. The conventional
	// project workflows path also includes the same nested repos the daemon
	// discovers for memories, skills and workflow sync.
	if conventional {
		projectPath := filepath.Dir(filepath.Dir(filepath.Clean(dir)))
		found, err := repo.Discover(context.Background(), projectPath, 0)
		if err != nil {
			return nil, err
		}
		nested := map[string]*workflowref.Layout{}
		for _, r := range found {
			if r.RelativePath == "" || r.RelativePath == "." {
				continue
			}
			l, err := workflowref.ReadSourceLayout(context.Background(), filepath.Join(projectPath, r.RelativePath))
			if err != nil {
				return nil, err
			}
			nested[filepath.ToSlash(r.RelativePath)] = l
		}
		layout = workflowref.MergeLayouts(layout, nested)
	}
	return layout, nil
}

// workflowFilesIn returns the workflow files in a workflows directory — its
// top-level YAML, as the app indexes it — as paths on disk.
func workflowFilesIn(dir string) ([]string, error) {
	layout, err := readWorkflowLayout(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range layout.Workflows.Entries() {
		files = append(files, filepath.Join(dir, filepath.FromSlash(workflowref.FilePath(e.Repo, e.Path))))
	}
	return files, nil
}

// embeddedWorkflow is a builtin workflow file as the binary carries it.
type embeddedWorkflow struct {
	name string // file name, e.g. "agent.yaml"
	data []byte
}

// builtinWorkflowFiles returns the builtin workflows embedded in this binary
// (internal/workflow/builtin), in file-name order. They are read from the
// binary, never from disk, so they are the same workflows the app runs
// wherever the CLI is invoked.
func builtinWorkflowFiles() ([]embeddedWorkflow, error) {
	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("reading embedded builtin workflows: %w", err)
	}
	files := make([]embeddedWorkflow, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := builtin.BuiltinWorkflowsFS.ReadFile(e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading embedded builtin workflow %s: %w", e.Name(), err)
		}
		files = append(files, embeddedWorkflow{name: e.Name(), data: data})
	}
	return files, nil
}

// projectWorkflowLoader resolves refs exactly as the app does
// (workflowref.Resolve), against a project's workflows. The CLI has no user
// workflows, so a project ref resolves in the project or not at all — and a
// ref that does not resolve is an error, not a silent pass: a broken ref
// fails here rather than at run time.
func projectWorkflowLoader(project *workflowref.Index) runtime.WorkflowLoader {
	return func(ref string) (*reliantv1.Workflow, error) {
		resolved, err := workflowref.Resolve(ref, workflowref.Sources{Project: project})
		if err != nil {
			return nil, err
		}
		return resolved.Workflow, nil
	}
}

// cliSkillResolver is built once per process: enumerating the catalog walks
// disk and forge's embedded templates, and `workflow validate` runs it against
// every file in a directory.
var cliSkillResolver = sync.OnceValue(func() *validation.SkillResolver {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	paths := skillscatalog.WorkflowSkillPaths(cwd)
	if len(paths) == 0 {
		// Nothing to check against. Returning a resolver here would fail every
		// skill name in every workflow; the layer is skipped instead.
		return nil
	}
	return &validation.SkillResolver{
		Names:   paths,
		Resolve: func(p string) bool { return skillscore.ResolveSkillPathIndex(paths, p) >= 0 },
	}
})

func buildCLISkillResolver() *validation.SkillResolver { return cliSkillResolver() }

// validateWorkflowFile validates one workflow file, resolving its refs against
// the workflows in workflowDir.
func validateWorkflowFile(path string, workflowDir string) validateResult {
	layout, err := readWorkflowLayout(workflowDir)
	if err != nil {
		return validateResult{File: filepath.Base(path), Errors: []string{err.Error()}}
	}
	return validateWorkflowFileIn(path, workflowDir, layout)
}

func validateWorkflowFileIn(path, workflowDir string, layout *workflowref.Layout) validateResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return validateResult{
			File:   filepath.Base(path),
			Errors: []string{fmt.Sprintf("failed to read file: %v", err)},
		}
	}

	// A file the project index cannot address — its name: is another file's
	// too, or crosses another file's file name — fails here, as every ref to
	// it would. (A missing name: is the validator's own "name is required".)
	var addressProblem error
	if rel, relErr := filepath.Rel(workflowDir, path); relErr == nil {
		location := filepath.ToSlash(rel)
		location = strings.TrimPrefix(location, "../../")
		if entry := layout.Workflows.EntryAt(location); entry != nil && entry.Name != "" && entry.Problem != nil {
			addressProblem = entry.Problem
		}
	}
	result := validateWorkflowYAML(data, filepath.Base(path), addressProblem, layout)
	if result.Name != "" {
		result.Lint = lintWorkflowSource(data, layout)
		if lint.HasErrors(result.Lint) {
			result.Valid = false
		}
	}
	return result
}

// validateWorkflowYAML validates one workflow definition, shown as file,
// resolving its refs against layout's project workflows. addressProblem is why
// the project index cannot address the file, if it cannot.
func validateWorkflowYAML(data []byte, file string, addressProblem error, layout *workflowref.Layout) validateResult {
	result := validateResult{
		File: file,
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

	if addressProblem != nil {
		result.Errors = append(result.Errors, addressProblem.Error())
	}

	// Run full validation with the app's ref resolution, which follows refs
	// transitively, and a real skill catalog.
	valResult, valErr := runtime.ValidateYAMLResultWithOptions(data, projectWorkflowLoader(layout.Workflows), &validation.ValidationOptions{
		SkillResolver: buildCLISkillResolver(),
		RootLabel:     result.File,
	})
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
	for _, b := range r.ActivationBreaks {
		fmt.Printf("      Breaks activations: %s\n", b)
	}
	printLintFindings("      ", r.Lint)
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
	if len(r.ActivationBreaks) > 0 {
		fmt.Println("Breaks activations:")
		for _, b := range r.ActivationBreaks {
			fmt.Printf("  - %s\n", b)
		}
	}
	if len(r.Lint) > 0 {
		fmt.Println("Lint:")
		printLintFindings("  ", r.Lint)
	}

	fmt.Println()
}

// --- workflow validate-tree implementation ---

func runWorkflowValidateTree(cmd *cobra.Command, ref, workflowDir, presetDir string, includeBuiltins bool, inputs []string, inputFile string, verbose, jsonOut bool) error {
	layout, err := readWorkflowLayout(workflowDir)
	if err != nil {
		return err
	}

	// Resolve the workflow YAML: a path, or a builtin:// / project:// ref.
	data, displayName, err := loadWorkflowTreeSource(ref, layout.Workflows)
	if err != nil {
		return err
	}

	wf, err := wfyaml.ParseWorkflow(data)
	if err != nil {
		return fmt.Errorf("parse workflow: %w", err)
	}

	// Build loaders.
	baseLoader := projectWorkflowLoader(layout.Workflows)
	wfLoader := validation.WorkflowLoader(baseLoader)
	if !includeBuiltins {
		wfLoader = func(r string) (*reliantv1.Workflow, error) {
			if parsed, err := workflowref.Parse(r); err == nil && parsed.Kind == workflowref.Builtin {
				return nil, nil // not followed: --include-builtins=false
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
		RootLabel:            displayName,
		Authoring:            true,
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

// loadWorkflowTreeSource reads the workflow YAML from a file path, or from a
// builtin:// or project:// reference resolved the way the app resolves it,
// and returns the data plus a display label.
func loadWorkflowTreeSource(ref string, project *workflowref.Index) ([]byte, string, error) {
	if strings.HasPrefix(ref, workflowref.BuiltinScheme) || strings.HasPrefix(ref, workflowref.ProjectScheme) {
		resolved, err := workflowref.Resolve(ref, workflowref.Sources{Project: project})
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", ref, err)
		}
		return resolved.YAML, ref, nil
	}

	data, err := os.ReadFile(ref)
	if err != nil {
		return nil, "", fmt.Errorf("cannot read %s: %w", ref, err)
	}
	return data, filepath.Base(ref), nil
}

// canonicalWorkflowRef returns a loadable ref suitable for
// ValidationOptions.CanonicalWorkflowRef. A ref is kept; for filesystem
// paths it is left empty so validation falls back to wf.name.
func canonicalWorkflowRef(ref string) string {
	if strings.HasPrefix(ref, workflowref.BuiltinScheme) || strings.HasPrefix(ref, workflowref.ProjectScheme) {
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

// parseCLIInputValue reads a --input value: a JSON literal (true, 10, 0.5,
// null, "text", [..], {..}) as that value, anything else as text. A number is
// read only when written canonically: 1.10 and 1e3 stay text, since reading
// them as numbers would lose what was typed if the input is a string. Quote a
// value to keep it text: --input 'sha="0123"'.
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
	if f, isNumber := v.(float64); isNumber && strconv.FormatFloat(f, 'f', -1, 64) != trimmed {
		return raw
	}
	return v
}

func printValidateSummary(results []validateResult) {
	valid := 0
	invalid := 0
	totalWarnings := 0
	breaks := 0

	for _, r := range results {
		if r.Valid {
			valid++
		} else {
			invalid++
		}
		totalWarnings += len(r.Warnings) + len(r.ActivationBreaks)
		breaks += len(r.ActivationBreaks)
	}

	fmt.Println()
	fmt.Println("\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550\u2550")
	fmt.Println("Summary")
	fmt.Println("\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500\u2500")
	fmt.Printf("  Total:    %d workflows\n", len(results))
	fmt.Printf("  Valid:    %d\n", valid)
	fmt.Printf("  Invalid:  %d\n", invalid)
	fmt.Printf("  Warnings: %d\n", totalWarnings)
	if breaks > 0 {
		fmt.Printf("  Breaks activations: %d (each activation of these triggers would turn BROKEN)\n", breaks)
	}
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
		Long: `Runs scenario tests against workflow definitions on the real workflow runtime
(DynamicWorkflow in an in-memory Temporal environment; only activities are mocked).
` + scenarioLayoutHelp + `
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
		Long: `Lists the scenarios of project workflows.
` + scenarioLayoutHelp + `
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

// scenarioLayoutHelp is where `workflow scenario` looks, for its help text.
const scenarioLayoutHelp = `
A project workflow's scenarios live in .reliant/workflows/<slug>/scenarios/,
one scenario per .yaml file, where <slug> is the workflow's name: as a slug
(blog.yaml declaring "name: blog-content-pipeline" keeps its scenarios in
blog-content-pipeline/scenarios/). That is the layout the app indexes, so the
CLI and the app see the same scenarios. A sub-workflow ref (project://<name>)
resolves by name: too, never by file name. Scenarios in the retired layouts
(scenarios/<file>/, <file>_scenarios.yaml) are an error naming where they go.
`

// workflowWithScenarios pairs a workflow file with its discovered scenarios.
type workflowWithScenarios struct {
	WorkflowFile string
	WorkflowName string
	Source       string // "project" or "builtin"
	Scenarios    []*wfscenario.Scenario
	// Project is the index the workflow's project:// refs resolve in. Nil
	// for a builtin, and read from the workflow file's directory when unset.
	Project *workflowref.Index
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
	workflows = filterScenarios(workflows, filter)

	if len(workflows) == 0 {
		if jsonOut {
			fmt.Println("[]")
			return nil
		}
		if filter != "" {
			fmt.Printf("No scenarios match --filter %q\n", filter)
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

		// Load the workflow and its scenario runner
		scenarioRunner, err := loadScenarioRunner(wf)
		if err != nil {
			result := scenarioRunResult{
				Workflow: wf.WorkflowName,
				File:     filepath.Base(wf.WorkflowFile),
				Source:   wf.Source,
				Scenarios: []scenarioResultSummary{{
					Name:   "(load)",
					Status: string(wfscenario.StatusError),
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

			if !jsonOut && verbose && !printedHeader {
				fmt.Printf("\u2500\u2500\u2500 %s (%s) \u2500\u2500\u2500\n", wf.WorkflowName, wf.Source)
				printedHeader = true
			}

			start := time.Now()
			result := scenarioRunner.Run(scenario)
			duration := time.Since(start).Milliseconds()

			summary := scenarioResultSummary{
				Name:       scenario.Name,
				Status:     string(result.Status),
				DurationMs: duration,
			}

			if result.Status != wfscenario.StatusPassed {
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

			if result.Status != wfscenario.StatusPassed && failFast {
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

// filterScenarios keeps the scenarios whose name contains filter, and the
// workflows left with any. It runs before anything is counted or run, so the
// "Running N scenario(s)" line and the run agree.
func filterScenarios(workflows []workflowWithScenarios, filter string) []workflowWithScenarios {
	if filter == "" {
		return workflows
	}
	var kept []workflowWithScenarios
	for _, wf := range workflows {
		var matching []*wfscenario.Scenario
		for _, s := range wf.Scenarios {
			if strings.Contains(s.Name, filter) {
				matching = append(matching, s)
			}
		}
		if len(matching) > 0 {
			wf.Scenarios = matching
			kept = append(kept, wf)
		}
	}
	return kept
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
//
// Project scenarios are located by workflowref — the locator the daemon uses
// to index them for the app — so the CLI runs exactly the scenarios the app
// lists. Scenarios the app would never see (a retired layout, a directory
// that names no workflow) are an error, not a silent skip. includeBuiltins
// adds the builtin workflows' scenarios, laid out the same way and embedded in
// the binary (workflowref.BuiltinLayout).
func discoverWorkflowsWithScenarios(args []string, dir string, includeBuiltins bool) ([]workflowWithScenarios, error) {
	workflowsDir, onlyFile := dir, ""
	if len(args) == 1 {
		path := args[0]
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("cannot access %s: %w", path, err)
		}
		if info.IsDir() {
			workflowsDir = path
		} else {
			workflowsDir, onlyFile = filepath.Dir(path), filepath.Base(path)
		}
	} else if !filepath.IsAbs(workflowsDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("could not get working directory: %w", err)
		}
		workflowsDir = filepath.Join(cwd, workflowsDir)
	}

	layout, err := readWorkflowLayout(workflowsDir)
	if err != nil {
		return nil, err
	}
	results, err := layoutScenarios(layout, "project", workflowsDir, onlyFile)
	if err != nil {
		return nil, err
	}

	if includeBuiltins {
		builtins, err := workflowref.BuiltinLayout()
		if err != nil {
			return nil, fmt.Errorf("reading builtin workflows: %w", err)
		}
		builtinResults, err := layoutScenarios(builtins, "builtin", "", "")
		if err != nil {
			return nil, err
		}
		results = append(results, builtinResults...)
	}

	return results, nil
}

// layoutScenarios pairs each workflow of a layout with its scenarios — a
// project's workflows directory (dir is where it is on disk), or the builtins
// the binary embeds (dir is "": their files are read from builtin.BuiltinWorkflowsFS).
// Both obey one layout, so both are read by this one function. onlyFile, when
// set, keeps just the workflow in that file.
func layoutScenarios(layout *workflowref.Layout, source, dir, onlyFile string) ([]workflowWithScenarios, error) {
	where := dir
	if source == "builtin" {
		where = "the builtin workflows"
	}
	if problems := layout.ScenarioProblems(); len(problems) > 0 {
		msgs := make([]string, len(problems))
		for i, p := range problems {
			msgs[i] = "  - " + p.Error()
		}
		return nil, fmt.Errorf("scenarios in %s are not where the app reads them:\n%s", where, strings.Join(msgs, "\n"))
	}

	var results []workflowWithScenarios
	for _, entry := range layout.Workflows.Entries() {
		if onlyFile != "" && entry.Path != onlyFile {
			continue
		}
		files := layout.ScenariosFor(entry.Address)
		if entry.Slug == "" || len(files) == 0 {
			continue
		}
		scenarios := make([]*wfscenario.Scenario, 0, len(files))
		for _, f := range files {
			sc, err := wfscenario.ParseScenarioFile(f.Content, f.Name)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", filepath.Join(dir, filepath.FromSlash(workflowref.FilePath(f.Repo, f.Path))), err)
			}
			scenarios = append(scenarios, sc)
		}
		wf := workflowWithScenarios{
			// A builtin's file is read back from the embedded FS, which is
			// slash-separated and rooted at the builtin directory.
			WorkflowFile: entry.Path,
			WorkflowName: entry.Name,
			Source:       source,
			Scenarios:    scenarios,
		}
		if source != "builtin" {
			wf.WorkflowFile = filepath.Join(dir, filepath.FromSlash(workflowref.FilePath(entry.Repo, entry.Path)))
			wf.Project = layout.Workflows
		}
		results = append(results, wf)
	}
	return results, nil
}

// loadScenarioRunner loads a workflow and returns the scenario runner for it,
// which executes scenarios on the real DynamicWorkflow. Handles both project
// files (from disk) and builtins (from embedded FS); a project workflow's
// project:// refs resolve by name: among the workflows beside it, exactly as
// the app resolves them.
func loadScenarioRunner(wf workflowWithScenarios) (*runner.Runner, error) {
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

	var loader runner.WorkflowLoader
	if wf.Source != "builtin" {
		project := wf.Project
		if project == nil {
			layout, err := readWorkflowLayout(filepath.Dir(wf.WorkflowFile))
			if err != nil {
				return nil, err
			}
			project = layout.Workflows
		}
		loader = runner.WorkflowLoader(projectWorkflowLoader(project))
	}
	return runner.New(parsedWf, runner.Options{Loader: loader}), nil
}

func printScenarioResult(s scenarioResultSummary, workflowName string, verbose bool) {
	switch wfscenario.ScenarioStatus(s.Status) {
	case wfscenario.StatusPassed:
		fmt.Printf("  \u2713 %s/%s (%dms)\n", workflowName, s.Name, s.DurationMs)
	case wfscenario.StatusFailed:
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
	case wfscenario.StatusError:
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
			switch wfscenario.ScenarioStatus(s.Status) {
			case wfscenario.StatusPassed:
				passed++
			case wfscenario.StatusFailed:
				failed++
			case wfscenario.StatusError:
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

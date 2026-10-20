// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/celeval"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func newWorkflowEvalCmd() *cobra.Command {
	var (
		kind, scope                     string
		integration, event              string
		triggerKind                     string
		triggerPayload, triggerSender   string
		activationParams, ownerGitHubID string
		inputs, nodes                   string
		inputKV, nodeKV                 []string
		jsonOut                         bool
	)
	cmd := &cobra.Command{
		Use:   "eval [expression]",
		Short: "Evaluate a CEL expression or {{ }} template against a sample context",
		Long: `Evaluates a CEL expression or {{ }} template the way a workflow would, against a
context you supply, and prints the value and its type, or a precise error. Use it
to try a trigger filter, an input mapping or a condition in one command, instead
of activating a trigger and waiting for an event.

The expression is the argument, or standard input when the argument is "-".

Context:
  --integration github --event issues.opened   trigger.payload is GENERATED from the
                                               payload that integration declares for
                                               the event (the schema validation checks
                                               filters against)
  --trigger-payload '{"data":{...}}'           trigger.payload; with --integration it
                                               is merged over the generated sample, so
                                               give only what the expression reads
                                               (a JSON string, or @file.json)
  --trigger-sender '{"id":"583231","verified":true}'
  --activation-params '{"repos":["acme/web"]}' trigger.activation.params (scope trigger)
  --owner-github-id 583231                     trigger.activation.owner.github_id
  --inputs '{"n":1}', --input k=v              inputs.*
  --nodes '{"start":{"stdout":"{}"}}'          nodes.<id>.*

With no expression, prints the trigger the event delivers.

Scope: --scope trigger (a trigger's filter, inputs mapping, prompt) sees only
trigger and activation; the default, run, is a node's field and also sees inputs
and nodes.

Examples:
  reliant workflow eval --integration github --event issues.opened \
      "!('wontfix' in trigger.payload.data.issue.labels)"
  reliant workflow eval --scope trigger --trigger-payload '{"body":{"status":"firing"}}' \
      "{{ trigger.payload.body.status == 'firing' }}"

Exit code 0 when it evaluated, 1 when it did not.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expr := ""
			if len(args) == 1 {
				expr = args[0]
				if expr == "-" {
					b, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return fmt.Errorf("read expression: %w", err)
					}
					expr = strings.TrimRight(string(b), "\n")
				}
			}
			req := celeval.Request{
				Expression:  expr,
				Kind:        celeval.Kind(kind),
				Integration: integration,
				Event:       event,
				TriggerKind: triggerKind,
			}
			if scope == "trigger" {
				req.Scope = celeval.ScopeTrigger
			} else if scope != "" && scope != "run" {
				return fmt.Errorf("--scope must be run or trigger, not %q", scope)
			}
			var err error
			if req.TriggerPayload, err = jsonObjectFlag("--trigger-payload", triggerPayload); err != nil {
				return err
			}
			if req.TriggerSender, err = jsonObjectFlag("--trigger-sender", triggerSender); err != nil {
				return err
			}
			if req.ActivationParams, err = jsonObjectFlag("--activation-params", activationParams); err != nil {
				return err
			}
			req.OwnerGitHubID = strings.TrimSpace(ownerGitHubID)
			if req.Inputs, err = jsonObjectFlag("--inputs", inputs); err != nil {
				return err
			}
			if req.Nodes, err = jsonObjectFlag("--nodes", nodes); err != nil {
				return err
			}
			if req.Inputs, err = mergeKV(req.Inputs, inputKV); err != nil {
				return err
			}
			if len(nodeKV) > 0 {
				return fmt.Errorf("--node is not supported; give node outputs as JSON with --nodes")
			}

			res := celeval.Evaluate(req)
			out := cmd.OutOrStdout()
			if jsonOut {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(res); err != nil {
					return err
				}
			} else {
				printEvalResult(out, res)
			}
			if res.Error != nil {
				return errSilent
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "How to read the expression: expression (raw CEL) or template ({{ }}); default: template when it has a {{ outside a string literal")
	cmd.Flags().StringVar(&scope, "scope", "", "What it can see: run (default; a node's field) or trigger (a trigger's filter, inputs mapping or prompt)")
	cmd.Flags().StringVar(&integration, "integration", "", "Generate trigger.payload from this integration's declared payload (with --event)")
	cmd.Flags().StringVar(&event, "event", "", "The integration event to generate trigger.payload for, e.g. issues.opened")
	cmd.Flags().StringVar(&triggerKind, "trigger-kind", "", "trigger.kind: webhook, integration, schedule or workflow_event")
	cmd.Flags().StringVar(&triggerPayload, "trigger-payload", "", "trigger.payload as JSON, or @file.json; merged over the generated sample")
	cmd.Flags().StringVar(&triggerSender, "trigger-sender", "", "trigger.sender as JSON: kind, id, display_name, verified")
	cmd.Flags().StringVar(&activationParams, "activation-params", "", "trigger.activation.params as JSON, or @file.json (scope trigger)")
	cmd.Flags().StringVar(&ownerGitHubID, "owner-github-id", "", "trigger.activation.owner.github_id (scope trigger); a string, like trigger.sender.id")
	cmd.Flags().StringVar(&inputs, "inputs", "", "inputs.* as JSON, or @file.json")
	cmd.Flags().StringArrayVar(&inputKV, "input", nil, "One input as key=value (repeatable; the value is JSON when it parses, else a string)")
	cmd.Flags().StringVar(&nodes, "nodes", "", "nodes.<id>.* as JSON keyed by node id, or @file.json")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the result as JSON")
	return cmd
}

// errSilent makes a command exit 1 after it has already printed why.
var errSilent = fmt.Errorf("evaluation failed")

func printEvalResult(w io.Writer, res celeval.Result) {
	if res.Source != "" && res.Trigger != nil && res.Error == nil && res.Value == nil && res.Type == "" {
		fmt.Fprintf(w, "trigger (%s):\n", res.Source)
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res.Trigger)
		return
	}
	if res.Source != "" {
		fmt.Fprintf(w, "trigger.payload: %s\n", res.Source)
	}
	if res.Error != nil {
		fmt.Fprintf(w, "✗ %s error: %s\n", res.Error.Phase, res.Error.Message)
		if res.Error.Hint != "" {
			fmt.Fprintf(w, "  hint: %s\n", res.Error.Hint)
		}
	} else {
		b, err := json.Marshal(res.Value)
		if err != nil {
			b = []byte(fmt.Sprintf("%v", res.Value))
		}
		fmt.Fprintf(w, "%s\n  type: %s (read as %s)\n", b, res.Type, res.Kind)
	}
	for _, f := range res.Findings {
		fmt.Fprintf(w, "  ! %s\n", f)
	}
}

// jsonObjectFlag parses a JSON-object flag value, or @file.json.
func jsonObjectFlag(name, value string) (map[string]any, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if strings.HasPrefix(value, "@") {
		b, err := os.ReadFile(strings.TrimPrefix(value, "@"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		value = string(b)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(value), &m); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", name, err)
	}
	return m, nil
}

func mergeKV(base map[string]any, kvs []string) (map[string]any, error) {
	if len(kvs) == 0 {
		return base, nil
	}
	if base == nil {
		base = map[string]any{}
	}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--input %q must be key=value", kv)
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			parsed = v
		}
		base[k] = parsed
	}
	return base, nil
}

// triggerSample is a sample event for `workflow validate --trigger-payload`.
// activation is the sample activation (--activation-params, --owner-github-id).
type triggerSample struct {
	payload, sender map[string]any
	activation      *celeval.ActivationSample
}

func triggerSampleFromFlags(payload, sender, params, ownerGitHubID string) (*triggerSample, error) {
	if strings.TrimSpace(payload) == "" {
		if strings.TrimSpace(sender) != "" {
			return nil, fmt.Errorf("--trigger-sender needs --trigger-payload")
		}
		if strings.TrimSpace(params) != "" || strings.TrimSpace(ownerGitHubID) != "" {
			return nil, fmt.Errorf("--activation-params and --owner-github-id need --trigger-payload")
		}
		return nil, nil
	}
	p, err := jsonObjectFlag("--trigger-payload", payload)
	if err != nil {
		return nil, err
	}
	s, err := jsonObjectFlag("--trigger-sender", sender)
	if err != nil {
		return nil, err
	}
	a, err := jsonObjectFlag("--activation-params", params)
	if err != nil {
		return nil, err
	}
	return &triggerSample{payload: p, sender: s,
		activation: &celeval.ActivationSample{Params: a, OwnerGitHubID: strings.TrimSpace(ownerGitHubID)}}, nil
}

// dryRunTriggers runs every trigger a workflow declares against the sample and
// folds what failed into the file's validation result. A trigger whose filter
// does not match the sample is reported as a note on the result, not an error.
func dryRunTriggers(result *validateResult, path string, sample *triggerSample) {
	if sample == nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	dryRunTriggersOn(result, data, sample)
}

// dryRunTriggersOn is dryRunTriggers for a definition already in hand.
func dryRunTriggersOn(result *validateResult, data []byte, sample *triggerSample) {
	if sample == nil {
		return
	}
	wf, err := parseWorkflowForDryRun(data)
	if err != nil || len(wf.GetTriggers()) == 0 {
		return
	}
	for _, wt := range wf.GetTriggers() {
		run := celeval.DryRunTrigger(wt, wf.GetInputs(), sample.payload, sample.sender, sample.activation)
		result.Errors = append(result.Errors, run.Errors()...)
		if run.Matched != nil && !*run.Matched {
			result.Warnings = append(result.Warnings, fmt.Sprintf("trigger %q: its filter does not match this sample event", run.Trigger))
		}
	}
	result.Valid = len(result.Errors) == 0
}

func parseWorkflowForDryRun(data []byte) (*reliantv1.Workflow, error) {
	return wfyaml.ParseWorkflow(data)
}

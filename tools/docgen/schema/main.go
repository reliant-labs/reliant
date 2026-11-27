// Copyright (c) 2025 Reliant Labs
//
// Workflow schema generator - generates top-level Workflow type documentation.
//
// SOURCE OF TRUTH:
//   - Proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
//
// For complex fields (inputs, nodes, etc.), the generator adds notes
// pointing to discovery tools rather than expanding the full type tree.
//
// GENERATED FILES:
//   - generated/docs-source/reference/workflow-schema.md
//
// Usage: go run ./tools/docgen/schema <md_output>

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TypeInfo holds extracted type metadata
type TypeInfo struct {
	Name        string
	Description string
	Fields      []FieldInfo
	Order       int
}

// FieldInfo holds extracted field metadata
type FieldInfo struct {
	Name        string
	Type        string
	JSONName    string
	Required    bool
	Description string
	ToolHint    string
}

// Complex types that should point to discovery tools
var toolHints = map[string]string{
	"inputs":  `Use get_schema(name="<type>") for input type details (e.g. "string", "enum", "model").`,
	"nodes":   `Use get_schema(name="<type>") for node type details (e.g. "call_llm", "router").`,
	"outputs": "CEL expressions mapping output names to values. Use get_cel_reference for CEL syntax.",
	"edges":   "See Edge type below.",
	"triggers": "WHEN the workflow runs: a list of {name, description, one source (schedule | webhook | integration | " +
		"workflow_event), filter (raw CEL over `trigger`), inputs (templates over `trigger`), prompt (template over " +
		"`trigger`: the message each run starts from)}. Declaring one fires nothing; the user enables it in the app. See the workflow-builder skill's Triggers section.",
	"automation_only": "The Chat trigger turned off: true keeps the workflow out of chat pickers and refuses a chat " +
		"start of it. Its triggers start it, and so does a deliberate start by hand: Run now, `reliant workflow run`, an " +
		"agent's start_run, or a builder test run. Unset means a chat can start it.",
	"hidden": "A building block other workflows ref: true keeps it out of every picker and default listing while it " +
		"stays resolvable by ref. With automation_only too, it is an automation's internal step and is listed nowhere. " +
		"Unset means the workflow is listed.",
	"attention": "Explicit human-attention incident correlation for root unattended launch runs, independent of lifecycle and outcome. Absent preserves legacy behavior. See WorkflowAttention below.",
	"concurrency": "Bounds trigger-launched runs: {max_running: N} (later firings wait, FIFO), {per_member: K} " +
		"(K per machine of the trigger's pool), {max_running_per_key: N, key: ...} (N per key, e.g. one run per pull " +
		"request), or a combination, optionally waiting: latest (only the newest waiting firing per key survives; older " +
		"ones are recorded skipped, \"superseded\", never failed), key (a template over `trigger` grouping firings) and " +
		"priority (a CEL integer over `trigger`: waiting firings run highest first). See WorkflowConcurrency below.",
	// WorkflowConcurrency's fields (the generated descriptors carry no comments).
	"max_running": "How many trigger-launched runs of the workflow run at once; later firings wait. Set it, " +
		"per_member, max_running_per_key, or a combination; with per_member it is a ceiling over the scaled cap. A run waiting on a provider " +
		"(a usage limit, provider capacity, no provider that can serve it) with nothing else executing does not " +
		"count: it gives its slot back, and takes one again before its next step, ahead of every waiting firing " +
		"but never above the cap.",
	"per_member": "Scales the cap with the trigger's machine pool: this many runs for each member that can take " +
		"work (installed, synced with the pool's catalog, not failed or removed), so adding a machine raises " +
		"throughput with no workflow edit. A trigger on one machine, or on none, counts as a pool of one.",
	"waiting": "What happens to waiting firings: `fifo` (the default) keeps every one, oldest first; `latest` keeps " +
		"only the newest waiting firing per key, and each older one it replaces is recorded skipped with the detail " +
		"\"superseded\" (never failed). Waiting never touches running runs; `cancel_running` does.",
	"key": "With `waiting: latest` or `max_running_per_key`: a template over `trigger`, like a webhook dedupe_key, " +
		"naming a group of firings, e.g. `'{{ trigger.payload.attributes.repository }}#{{ trigger.payload.pull_request.number }}'`. " +
		"Under waiting: latest its waiting firings replace each other (unset: one group for the whole workflow); with " +
		"max_running_per_key its running runs are capped (required). max_running still bounds the whole workflow. A " +
		"firing whose key does not render waits, is never superseded, and counts only against max_running.",
	"max_running_per_key": "How many runs of one key (`key`, required) run at once, e.g. 1 for one run per pull " +
		"request; the key's later firings wait. A firing whose key is at its cap does not hold back firings of other " +
		"keys behind it. With max_running or per_member too, both caps apply; alone, only this one does. A run " +
		"waiting on a provider gives back its max_running slot but keeps its key's.",
	"priority": "A CEL expression over `trigger` (no `{{ }}`) yielding an integer, evaluated once when a firing " +
		"starts waiting, e.g. `\"'main-red' in trigger.payload.labels ? 10 : 0\"`. Waiting firings are admitted highest " +
		"priority first, then in arrival order; running runs are never preempted. Unset, or a firing whose expression " +
		"fails: 0. A low-priority firing can wait behind higher ones until it expires, a day after it arrived.",
	"cancel_running": "Lets a newer firing of a key stop the run holding the key's slot instead of waiting behind it " +
		"(requires `key` and `max_running_per_key: 1`). `never` (the default) leaves running runs alone; " +
		"`on_new_key_event` cancels on every newer firing of the key; `on_new_head` cancels only when the firing carries " +
		"a GitHub pull request head (`trigger.payload.data.pull_request.head.sha`) that differs from the running run's " +
		"firing's, so a comment, or a firing on the same head, never cancels. The cancel is a user stop's: the run's " +
		"`finally:` runs, its firing records run status cancelled and \"superseded by event <id>\", and the new firing " +
		"takes the slot under `waiting`.",
	"check": "A GitHub check run the engine owns, one per run an event trigger fires, written as the Reliant App: " +
		"queued when the firing is recorded, in_progress when the first node starts, and concluded on every ending " +
		"(success, failure, cancelled, timed_out; never neutral or skipped). `check: <name>` or {name, repository, " +
		"head_sha, output, timeout}. See WorkflowCheck below.",
	"observe": "The default observe block for every node of this graph ({attributes, events, metrics}, templates over " +
		"each node's `output` and `node`), merged with a node's own: attributes by key and metrics by name (the node " +
		"wins), events concatenated. Not inherited by sub-workflows. See the nodes reference, Observability. This " +
		"release reads and validates it and records nothing yet.",
	// WorkflowCheck's fields.
	"repository": "A template over `trigger` naming the repository, owner/name. Unset: the event's " +
		"(trigger.payload.attributes.repository).",
	"head_sha": "A template over `trigger` naming the commit. Unset: the event's head commit (a pull request's head, " +
		"the check suite's or check run's head_sha, the workflow run's head_sha, a push's new commit).",
	"output": "A template over the ended run's scope (nodes, inputs, trigger, workflow, run.status) evaluating to " +
		"{title, summary, text?, annotations?}. Unset, or one that fails: the engine's own summary. The conclusion " +
		"comes from how the run ended, never from this.",
	"timeout": "A Go duration from 1m to 72h (default 6h), from the firing: a check still not concluded then " +
		"concludes timed_out.",
	// Qualified: WorkflowCheck.timeout above is the check's, not the run's.
	"Workflow.timeout": "Bounds a run, as a literal Go duration more than zero and at most 168h (7 days), e.g. " +
		"`timeout: 3h`, counted from the run's start and across continue-as-new; it keeps counting while the run is " +
		"paused or waits for an approval, a question, a machine or capacity. When it passes the run is stopped like a " +
		"user stop and ends FAILED with \"run timeout <d> exceeded\" (failure.error_code `run_timeout`); on_failure and " +
		"finally still run, outside it. Only the run's root workflow's applies. Unset: no bound.",
	"requires": "What a run needs from outside its definition, checked after its machine is up and before its " +
		"first node: {tools, integrations, repos}. A run missing anything fails at once, naming every " +
		"missing item with its fix. See WorkflowRequirements below.",
	// WorkflowRequirements' fields, qualified: tools means something else on
	// other types of this page.
	"WorkflowRequirements.tools": "Executables that must be on the run machine's PATH (git, gh, task, jq).",
	"WorkflowRequirements.integrations": "Integration ids (github, slack) whose connection must resolve, " +
		"authorized, for the run's owner and acting identity.",
	"WorkflowRequirements.repos": "Checkouts relative to the project root (\".\" for the root) that must exist, " +
		"reach their origin (git ls-remote, which writes nothing) and have no local tag diverged from origin's.",
	"on_failure": "A finally-style handler: an inline graph ({entry, nodes, edges}) that runs once when a run FAILS (an " +
		"unhandled node error, an agent that never produced its required response, a node that declares outcome: failure). " +
		"Never when the run completes, pauses or is cancelled. Its nodes read `failure.{node_id, node_path, error, " +
		"error_code, attempt}`, `inputs`, and the `nodes` outputs the run produced (guard them with `.?`). The run still ends " +
		"FAILED; an error in the handler is logged and never recursive. To release something however the run ends, " +
		"cancel included, use `finally`.",
	"finally": "A handler for however a run ends: an inline graph ({entry, nodes, edges}) that runs once when a run " +
		"completes, fails (after on_failure) or is cancelled. Never when it pauses or hands off to a successor run. Its " +
		"nodes read `run.status` ('completed' | 'failed' | 'cancelled'), `failure.*` (empty unless the run failed), " +
		"`inputs`, and the `nodes` outputs the run produced (guard them with `.?`). It cannot change how the run ended; " +
		"an error in it is logged and never recursive. Release leases, claims and labels here.",
	"daemon": "Which machine runs the workflow's tools: a kind (`local`, `cloud`, `any`), a machine name, a " +
		"DaemonSelector, or a CEL template producing either. A node's own `daemon:` wins over this, and this wins " +
		"over the chat's or automation's machine. `{pool: <name>}` places the run once on one member of a machine " +
		"pool instead of pinning one machine. See DaemonSelector below.",
	// DaemonSelector's fields, qualified: id, name, type and labels mean
	// something else on other types of this page.
	"DaemonSelector.id":   "Exact machine id. Pins one machine (lint: daemon-machine-pin).",
	"DaemonSelector.name": "Machine name, or id. Pins one machine (lint: daemon-machine-pin).",
	"DaemonSelector.type": "Machine kind: `local`, `cloud` or `any`.",
	"DaemonSelector.labels": "Match by the labels a machine advertises when it connects (all must match), on each " +
		"call. Alone or beside id, name or type; never placed.",
	"DaemonSelector.pool": "Place once on one member of the owner's machine pool of this name: the least-loaded " +
		"member that can take work, on the pool's agreed workflow catalog, under its max_runs_per_member (a full " +
		"pool waits for a slot). Exclusive with id, name, type and labels. A pool the owner does not have fails " +
		"validation at save.",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <md_output>\n", os.Args[0])
		os.Exit(1)
	}

	mdOutput := os.Args[1]

	types := extractTypesFromProto()

	markdown := generateMarkdown(types)
	if err := os.MkdirAll(filepath.Dir(mdOutput), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(mdOutput, []byte(markdown), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing markdown: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Generated workflow schema: %s (%d types)\n", mdOutput, len(types))
}

func extractTypesFromProto() []TypeInfo {
	return []TypeInfo{
		extractTypeFromMessage("Workflow",
			(&reliantv1.Workflow{}).ProtoReflect().Descriptor(),
			"Defines a complete workflow with nodes, edges, inputs, and outputs.", 1),
		extractTypeFromMessage("Edge",
			(&reliantv1.Edge{}).ProtoReflect().Descriptor(),
			"Connects a source node to destination(s) with conditional routing.", 2),
		extractTypeFromMessage("EdgeCase",
			(&reliantv1.EdgeCase{}).ProtoReflect().Descriptor(),
			"Defines one conditional routing path from an edge.", 3),
		extractTypeFromMessage("WorkflowConcurrency",
			(&reliantv1.WorkflowConcurrency{}).ProtoReflect().Descriptor(),
			"A trigger admission policy (`concurrency:`), not a worker task limit. Pooled per owner, project and "+
				"workflow; manual trigger fires participate, chat starts do not. It bounds runs that are running: "+
				"a run waiting on a provider holds no slot until it resumes. Example, one release at a time with "+
				"only the newest waiting merge per repository: `concurrency: {max_running: 1, waiting: latest, key: "+
				"'{{ trigger.payload.attributes.repository }}'}`. Another, at most six reviews at once and one per "+
				"pull request, main-red fixes first: `concurrency: {max_running: 6, max_running_per_key: 1, key: "+
				"'{{ trigger.payload.attributes.repository }}#{{ trigger.payload.pull_request.number }}', priority: "+
				"\"'main-red' in trigger.payload.labels ? 10 : 0\"}`.", 4),
		extractTypeFromMessage("WorkflowAttention",
			(&reliantv1.WorkflowAttention{}).ProtoReflect().Descriptor(),
			"Correlates incidents within one owner and project, never across them. Failures remain failed and emit failure events. "+
				"Only explicit on_success: resolved closes an incident; a key-only observer's success does not. "+
				"Example: `attention: {key: 'repo:{{ inputs.repo }}:pr:{{ inputs.pr }}', on_start: recovering, recovery_timeout: 30m, on_success: resolved}`.", 5),
		extractTypeFromMessage("WorkflowCheck",
			(&reliantv1.WorkflowCheck{}).ProtoReflect().Descriptor(),
			"A GitHub check run whose lifecycle the engine owns (`check:`): one per firing, posted as the Reliant "+
				"App on the event's head commit, details_url the run's page. Example: `check: {name: reliant/ci, "+
				"output: '{{ nodes.report.check }}', timeout: 2h}`.", 6),
		extractTypeFromMessage("DaemonSelector",
			(&reliantv1.DaemonSelectorProto{}).ProtoReflect().Descriptor(),
			"Where tools run (`daemon:` on the workflow or a node; node > workflow > the chat's or automation's "+
				"machine > the default machine). A string is shorthand: `local`, `cloud` or `any` is a kind, anything "+
				"else a machine name. `{pool: <name>}` places the run (workflow level) or "+
				"the node (node level, reused by every loop iteration) ONCE on one machine and holds a slot on it while "+
				"the run is live, so the run is not tied to any one machine being there and free. Example: `daemon: {pool: ci}`.", 7),
	}
}

func extractTypeFromMessage(name string, md protoreflect.MessageDescriptor, desc string, order int) TypeInfo {
	info := TypeInfo{
		Name:        name,
		Description: desc,
		Order:       order,
	}

	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		fieldName := string(fd.Name())

		// Skip UI metadata and internal fields
		if fieldName == "ui" {
			continue
		}

		fieldType := simplifyProtoType(fd)
		fieldDesc := getFieldComment(fd)

		// Check for tool hints: one qualified by the type wins over a bare one.
		toolHint, ok := toolHints[name+"."+fieldName]
		if !ok {
			toolHint = toolHints[fieldName]
		}
		// Hints are message-specific: attention.key/on_failure deliberately
		// have different semantics from concurrency.key/Workflow.on_failure.
		if name == "WorkflowAttention" {
			toolHint = map[string]string{
				"key":              "Required literal or template over finalized inputs only. Evaluated once before graph execution; must produce a nonempty string of at most 512 bytes. Invalid dynamic keys fail visibly with legacy attention as a fail-safe.",
				"on_start":         "Absent, recovering, or action_required. Recovering requires recovery_timeout. Only an actually started recovery run suppresses attention.",
				"recovery_timeout": "Go duration in [1s, 24h], allowed only with on_start: recovering. Fixed from run start; retries never extend it. Expiry escalates through the durable inbox read, without a push notification.",
				"on_success":       "Absent, resolved, or action_required. No implicit resolution on success. A recovery finishing without explicit successful resolution escalates.",
				"on_failure":       "Absent (defaults to action_required), or action_required. Crashes, cancellation, and unsuccessful outcomes never resolve an incident.",
			}[fieldName]
		}

		info.Fields = append(info.Fields, FieldInfo{
			Name:        fieldName,
			Type:        fieldType,
			JSONName:    fieldName,
			Required:    (fieldName == "name" && name != "DaemonSelector") || fieldName == "from" || (name == "WorkflowAttention" && fieldName == "key"),
			Description: fieldDesc,
			ToolHint:    toolHint,
		})
	}

	return info
}

func simplifyProtoType(fd protoreflect.FieldDescriptor) string {
	if fd.IsMap() {
		keyType := simplifyKind(fd.MapKey().Kind())
		valType := simplifyKind(fd.MapValue().Kind())
		if fd.MapValue().Kind() == protoreflect.MessageKind {
			valName := string(fd.MapValue().Message().Name())
			switch valName {
			case "V2Input":
				return "map[string]Input"
			case "Param":
				return "map[string]Input"
			}
			return "map[" + keyType + "]" + valName
		}
		return "map[" + keyType + "]" + valType
	}

	if fd.IsList() {
		if fd.Kind() == protoreflect.MessageKind {
			msgName := string(fd.Message().Name())
			return simplifyMessageName(msgName) + "[]"
		}
		return simplifyKind(fd.Kind()) + "[]"
	}

	if fd.Kind() == protoreflect.MessageKind {
		msgName := string(fd.Message().Name())
		return simplifyMessageName(msgName)
	}

	return simplifyKind(fd.Kind())
}

func simplifyKind(k protoreflect.Kind) string {
	switch k {
	case protoreflect.StringKind:
		return "string"
	case protoreflect.BoolKind:
		return "boolean"
	case protoreflect.Int32Kind, protoreflect.Int64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return "integer"
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return "number"
	case protoreflect.MessageKind:
		return "object"
	case protoreflect.EnumKind:
		return "enum"
	default:
		return "any"
	}
}

func simplifyMessageName(name string) string {
	switch name {
	case "V2Node":
		return "Node"
	case "V2Edge":
		return "Edge"
	case "V2EdgeCase":
		return "EdgeCase"
	case "V2WorkflowUI":
		return "WorkflowUI"
	case "V2PresetsConfig":
		return "PresetsConfig"
	case "V2Input":
		return "Input"
	case "DirectCelBool":
		return "string"
	case "CelString":
		return "string"
	default:
		// Strip V2 prefix if present
		if strings.HasPrefix(name, "V2") {
			return name[2:]
		}
		return name
	}
}

func getFieldComment(fd protoreflect.FieldDescriptor) string {
	loc := fd.ParentFile().SourceLocations().ByDescriptor(fd)
	if loc.LeadingComments != "" {
		return cleanComment(loc.LeadingComments)
	}
	return ""
}

func cleanComment(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}
	return strings.Join(cleaned, " ")
}

func generateMarkdown(types []TypeInfo) string {
	var sb strings.Builder

	sb.WriteString(`<!--
=============================================================================
GENERATED FILE - DO NOT EDIT DIRECTLY

Source: proto messages V2Workflow, V2Edge, V2EdgeCase in workflow_v2.proto
Generator: tools/docgen/schema
Regenerate: task generate:docs:schema
=============================================================================
-->

# Workflow Schema Reference

`)

	for _, t := range types {
		fmt.Fprintf(&sb, "## %s\n\n", t.Name)

		if t.Description != "" {
			sb.WriteString(t.Description + "\n\n")
		}

		if len(t.Fields) > 0 {
			sb.WriteString("### Fields\n\n")
			sb.WriteString("| Field | Type | Required | Description |\n")
			sb.WriteString("|-------|------|----------|-------------|\n")

			for _, f := range t.Fields {
				required := "No"
				if f.Required {
					required = "Yes"
				}

				desc := strings.Split(f.Description, "\n")[0]
				if f.ToolHint != "" {
					if desc != "" {
						desc += " "
					}
					desc += "*" + f.ToolHint + "*"
				}
				if desc == "" {
					desc = "-"
				}
				desc = strings.ReplaceAll(desc, "|", "\\|")

				fmt.Fprintf(&sb, "| `%s` | %s | %s | %s |\n",
					f.JSONName, f.Type, required, desc)
			}
			sb.WriteString("\n")
		}

		sb.WriteString("---\n\n")
	}

	// Syntax sugar section — these are YAML-only conveniences that desugar at parse time,
	// so they don't appear in the auto-generated proto schema above.
	sb.WriteString(`## Syntax Sugar (YAML-only)

These fields are not part of the proto schema — they are YAML convenience features that desugar to standard nodes, edges, and entry at parse time.

### ` + "`sequence:`" + `

Top-level workflow field. Replaces ` + "`entry:`" + ` + ` + "`nodes:`" + ` + sequential ` + "`edges:`" + ` for linear chains. Cannot coexist with ` + "`entry:`" + `.

### ` + "`type: parallel`" + ` (node type)

A node with ` + "`type: parallel`" + ` and ` + "`branches:`" + ` expands into branch nodes + a join node + fan-out/fan-in edges. The parallel node's ` + "`id`" + ` becomes the join node's id.

See workflow documentation for full examples and usage patterns.

`)

	return sb.String()
}

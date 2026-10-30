// Copyright (c) 2025 Reliant Labs
//
// Workflow builder skill assembler - generates workflow-builder/SKILL.md.
//
// Auto-generates content from:
//   - internal/workflow/reference (populated from proto descriptors at init)
//   - internal/workflow/builtin/*.yaml (builtin workflow list)
//   - generated/docs-source/reference/workflow-schema.md (top-level schema)
//   - generated/docs-source/reference/scenario-schema.md (scenario testing schema)
//
// GENERATED FILE:
//   - internal/skills/catalog/builtin/workflow-builder/SKILL.md
//
// Usage: go run ./tools/docgen/assembler <docs_dir> <output_file>

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/reference"
	"gopkg.in/yaml.v3"
)

const skillFrontmatter = `---
name: workflow-builder
description: Build a bot or agent that runs on a schedule or event (cron, webhook, twice a day, daily digest, monitor, triage); also multi-agent Reliant workflows. Use to create, edit, test or debug any workflow.
compatibility: reliant
metadata:
  category: workflow
  owner: reliant
---`

// BuiltinWorkflow represents minimal workflow info for listing.
type BuiltinWorkflow struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <docs_dir> <output_file>\n", os.Args[0])
		os.Exit(1)
	}

	docsDir := os.Args[1]
	outputFile := os.Args[2]

	skill, err := assembleSkill(docsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error assembling workflow-builder skill: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(outputFile, []byte(skill), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing output: %v\n", err)
		os.Exit(1)
	}

	tokenEstimate := len(skill) / 4
	fmt.Printf("Generated workflow-builder skill: %s (~%d tokens)\n", outputFile, tokenEstimate)
}

func assembleSkill(docsDir string) (string, error) {
	sections := []string{
		generateHeader(),
		generateKeyConcepts(),
		generateSyntaxSugar(),
		generateTriggers(),
		generateGotchas(),
		generateAvailableTools(),
		generateCELQuickReference(),
	}

	builtins, err := generateBuiltinWorkflowsList()
	if err != nil {
		return "", fmt.Errorf("generating builtin workflows: %w", err)
	}
	sections = append(sections, builtins)

	sections = append(sections, generateNodeTypesSummary())
	sections = append(sections, generateInputTypesSummary())

	schemaPath, err := resolveReferenceFile(docsDir, "workflow-schema.md")
	if err != nil {
		return "", err
	}
	schema, err := extractTopLevelSchema(schemaPath)
	if err != nil {
		return "", fmt.Errorf("extracting schema: %w", err)
	}
	sections = append(sections, schema)

	scenarioPath, err := resolveReferenceFile(docsDir, "scenario-schema.md")
	if err != nil {
		return "", err
	}
	scenarios, err := extractScenarioEssentials(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("extracting scenarios: %w", err)
	}
	sections = append(sections, scenarios)
	sections = append(sections, generateReferences())

	body := strings.Join(sections, "\n\n")
	return skillFrontmatter + "\n" + strings.TrimSpace(body) + "\n", nil
}

func generateHeader() string {
	return `# Workflow Builder

You build Reliant workflows. Your goal: create working workflows that solve the user's problem.

Threading, context, loops, parallelization, inputs, prompts, error handling and composition
have their own do's-and-don'ts reference: ` + "`skill(action=\"load\", path=\"workflow-builder/design-patterns\")`" + `.
Load it before designing a workflow's control flow.

## When to use

- Building an agent or automation that runs on its own: on a schedule ("every morning", "twice a day", cron), on a webhook or an app event — a bot, monitor, digest or triage job, even when it sounds like "just a script"
- Creating a new Reliant workflow from scratch
- Editing or extending an existing workflow
- Debugging workflow execution issues
- Writing scenario tests for workflows
- Understanding workflow patterns and best practices

## Approach

Every workflow tool that targets an existing workflow (` + "`get_workflow`" + `, ` + "`edit_workflow`" + `, ` + "`write_workflow`" + `, and the scenario tools) requires an explicit
` + "`id`" + ` — a UUID, slug, or name. Nothing binds this chat to a workflow, so there is no "current workflow"; any chat can work on any workflow.

Starting from scratch? Call ` + "`create_workflow`" + ` first — it returns the new draft's ` + "`id`" + `, ` + "`name`" + `, and ` + "`slug`" + `. Pass that ` + "`id`" + ` to every
subsequent call. Omit both ` + "`name`" + ` and ` + "`content`" + ` to get a random name and the default agent template; pass ` + "`content`" + `
with complete workflow YAML to start from a specific design.

Editing an existing workflow? Find it with ` + "`list_workflows`" + ` and pass its id or slug. If the user's message names a
workflow (e.g. "Workflow ` + "`swift-fox-a1b2`" + `: ..."), use that as the ` + "`id`" + `.

Follow this process:

1. **Setup** — New workflow: call ` + "`create_workflow`" + `. Existing workflow: call ` + "`get_workflow`" + ` with its ` + "`id`" + ` to see current content.
2. **Understand** — Ask clarifying questions about the user's goal
3. **Learn** — Use ` + "`list_workflows`" + ` to see examples and patterns, ` + "`list_presets`" + `/` + "`get_preset`" + ` to see what
   agent presets exist before inventing a system prompt from scratch. Calling an external service?
   ` + "`search_integrations`" + ` finds the action ref and ` + "`get_integration_schema`" + ` its params before you write the node
4. **Explore** — Read the user's codebase (test commands, code patterns). Note: references to "workflows" and "nodes" in user code are unlikely to be Reliant-specific.
5. **Build** — Use ` + "`edit_workflow`" + ` for small changes, ` + "`write_workflow`" + ` for larger rewrites
6. **Test** — Create and run scenarios (aim for 3+ covering positive, negative, and edge cases). Try to break your workflow. It's frustrating for users to run a workflow for an hour and hit a bug at the end—scenarios catch this early.

Working in a chat that isn't running this preset? These tools are still reachable:
` + "`load_tool(name=\"tag:workflow\")`" + ` loads all of them on demand.`
}

func generateKeyConcepts() string {
	return `## Key concepts

### Workflows are DAGs

Every workflow must be a directed acyclic graph. You cannot create cycles in the graph structure. Use **loop nodes** (with ` + "`while`" + ` conditions) for iterative behavior.

### CEL expressions

Two syntax modes:

- **` + "`condition`" + ` / ` + "`while`" + ` fields**: Pure CEL — no ` + "`{{}}`" + ` wrapping
  ` + "```yaml" + `
  condition: "nodes.llm.stop_reason == 'tool_use'"
  while: "outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted'] && iter.iteration < 50"
  ` + "```" + `
- **All other fields**: Template interpolation with ` + "`{{}}`" + `
  ` + "```yaml" + `
  model: "{{inputs.model}}"
  system_prompt: "You are helping with {{inputs.task}}"
  ` + "```" + `

Always use ` + "`has()`" + ` or safe navigation (` + "`object.?field`" + `) before accessing optional fields.

**Template rules.** A ` + "`{{ }}`" + ` is read as CEL, so a brace inside a string literal is data
(` + "`{{ x.startsWith('{') }}`" + ` works) and a newline inside it is whitespace (a folded ` + "`>-`" + ` scalar is fine). A ` + "`{{`" + `
that never closes, an empty ` + "`{{ }}`" + ` and an expression that does not parse are validation errors with the
field path and line/column, never literal text; write a literal ` + "`{{`" + ` as ` + "`{{ '{{' }}`" + `. Raw-CEL fields
(` + "`condition`" + `, ` + "`while`" + `, a trigger ` + "`filter`" + `) take no ` + "`{{ }}`" + `. Inside an inline ` + "`type: workflow`" + ` body, templates are
evaluated in the BODY's scope when it runs (its own ` + "`inputs`" + ` and ` + "`nodes`" + `), not the parent's.

**Deterministic logic belongs in the workflow, not a script.** Use a ` + "`type: compute`" + ` node (below) for
labels, size buckets, lease expiry, id validation and parsing a node's JSON stdout. Expressions have
Strings, Lists, Sets, Math, Encoders, Bindings, Regex, optional types, timestamps and durations, and
` + "`json.decode(s)`" + ` (tolerant: an optional, ` + "`.?status.orValue('error')`" + `) beside the strict ` + "`parseJson(s)`" + `. Evaluation is
cost-bounded (a runaway expression fails with ` + "`cost limit exceeded`" + `). The CEL reference has a table of
worked helper expressions. **Test an expression with ` + "`cel_eval`" + ` before writing it into a workflow.**

### Compute node

` + "`type: compute`" + ` computes named outputs from CEL in the workflow: no model, no shell, no worker, no mock needed in a
scenario. ` + "`outputs:`" + ` maps a name to a value; a value that is exactly one ` + "`{{ }}`" + ` keeps its type, text around it makes a
string, a non-string is a literal. Read them as ` + "`nodes.<id>.<name>`" + ` (validation checks the names; values are dyn, so compare a boolean output in a ` + "`condition`" + `: ` + "`nodes.facts.ready == true`" + `). Outputs cannot
read each other: chain two compute nodes. A failing expression fails the node.

` + "```yaml" + `
- id: facts
  type: compute
  outputs:
    size: "{{ inputs.adds + inputs.dels < 60 ? 'size:S' : 'size:M' }}"
    ready: "{{ 'agent:ready' in inputs.labels }}"
    status: "{{ json.decode(nodes.start.stdout).?status.orValue('error') }}"
` + "```" + `

### Publishing a branch

` + "`type: git_publish`" + ` commits a worktree's changes and pushes them to a feature branch with no shell ever holding a
credential. Pair it with ` + "`credentials: none`" + ` on the editing shell.

` + "```yaml" + `
- id: publish
  type: git_publish
  args:
    worktree: "{{ nodes.wt.path }}"
    repo: "{{ inputs.repo }}"                      # owner/name
    branch: "autopilot/issue-{{ inputs.issue }}"   # or pr_head_of: "{{ inputs.pr }}"
    allow: ["autopilot/*"]                         # optional
    message: "{{ nodes.edit.commit_message }}"
` + "```" + `

Refused (the node completes with ` + "`refused: true`" + ` and a ` + "`refusal`" + `, it does not fail): the default branch, protected
branches, ` + "`main`, `master`, `release/*`" + `, names that are not plain branches, branches outside ` + "`allow`" + `, a pull
request head in a fork. Never forces: a diverged remote is refused. No changes means ` + "`pushed: false`" + ` and no error.
Outputs: ` + "`pushed`, `sha`, `branch`, `base_sha`, `files_changed`, `insertions`, `deletions`, `refused`, `refusal`" + `. Route on
them: ` + "`condition: nodes.publish.pushed == true`" + `. The push is recorded in the write ledger with its sha, so its events
carry ` + "`trigger.origin`" + `. In a scenario, mock the node's outputs like any activity.

### Shells without credentials

` + "`credentials: none`" + ` removes GitHub, git, ssh and cloud credentials from a command's environment: no
` + "`GH_TOKEN`/`GITHUB_TOKEN`" + `, no ssh agent, no cloud variables, no kubeconfig, git's credential helpers reset,
` + "`GIT_TERMINAL_PROMPT=0`" + `, an empty ` + "`GH_CONFIG_DIR`" + `. A ` + "`git push`" + ` or ` + "`gh pr merge`" + ` from it fails to
authenticate at once and never prompts. Set it for an agent's shell with ` + "`tools_config.tools.shell`" + ` (bound, so
the model is never shown it) and on a ` + "`run`" + ` node with ` + "`credentials: none`" + `.

` + "```yaml" + `
tools_config:
  tools:
    shell: {credentials: none}
` + "```" + `

On Linux (every Reliant cloud machine) the command also runs in a sandbox where the daemon's credential files are
absent (` + "`~/.reliant/daemon.json`, `~/.config/forge`, `~/.git-credentials`, `~/.ssh`, `~/.config/gh`" + `, kubeconfig and cloud files, the
container engine's socket), sees only its own processes, and has no capabilities. It shares the network and can
write its working directory, and on macOS and Windows only the environment is scrubbed. Never tell an agent to look
elsewhere for a token; publish with a ` + "`git_publish`" + ` node.

### Threading

Modes: ` + "`inherit`" + ` (default), ` + "`new`" + `, ` + "`fork`" + `

- **inherit**: Reuse parent thread (default)
- **new**: Create a fresh thread
- **fork**: Copy parent thread into a new one

Key rules:
- Use ` + "`memo`" + ` in loops to reuse the same thread across iterations
- Use ` + "`inject`" + ` to prepend a message when entering a sub-workflow
- **Never** run parallel agents on the same thread simultaneously

### Edge routing

- Multiple ` + "`cases`" + ` on one edge = exactly 1 executes (first match wins)
- All cases require a ` + "`condition`" + ` — use ` + "`default`" + ` for the fallback
- For parallelism: create multiple edges from the same source node, OR use ` + "`default: [node-a, node-b]`" + ``
}

func generateSyntaxSugar() string {
	return `## Syntactic sugar

These shorthands compile to standard nodes/edges at parse time. They are **not** in the proto — purely YAML convenience.

### ` + "`sequence:`" + ` sugar

A top-level workflow field (alongside ` + "`name:`" + `, ` + "`nodes:`" + `, ` + "`edges:`" + `) that replaces the combination of ` + "`entry:`" + `, ` + "`nodes:`" + `, and sequential ` + "`edges:`" + ` for linear chains.

Rules:
- Cannot coexist with ` + "`entry:`" + ` (it implies entry from the first node)
- CAN coexist with ` + "`nodes:`" + ` and ` + "`edges:`" + ` for mixed patterns (e.g., linear main flow with extra branches)
- Compiles to standard ` + "`entry:`" + ` + ` + "`nodes:`" + ` + ` + "`edges:`" + ` at parse time

Before (explicit):
` + "```yaml" + `
entry: [research]
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
edges:
  - from: research
    default: implement
  - from: implement
    default: review
` + "```" + `

After (sugar):
` + "```yaml" + `
sequence:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: implement
    type: workflow
    ref: builtin://agent
  - id: review
    type: workflow
    ref: builtin://structured-agent
` + "```" + `

### ` + "`type: parallel`" + ` sugar

A node type used inside ` + "`nodes:`" + ` that desugars into branch nodes + a join node + fan-out/fan-in edges.

Rules:
- Must have ` + "`id:`" + ` and ` + "`branches:`" + ` fields
- ` + "`branches:`" + ` is a list of regular node definitions
- The parallel node's ` + "`id`" + ` becomes the join node's id (so downstream edges referencing it still work)
- Join uses ` + "`condition: all`" + ` (all branches must complete)
- Incoming edges targeting the parallel node get rewritten to fan-out to all branches

Before (explicit):
` + "```yaml" + `
nodes:
  - id: research
    type: workflow
    ref: builtin://agent
  - id: design
    type: workflow
    ref: builtin://agent
  - id: explore
    type: join
    condition: all
edges:
  - from: trigger
    default: [research, design]
  - from: research
    default: explore
  - from: design
    default: explore
` + "```" + `

After (sugar):
` + "```yaml" + `
nodes:
  - id: explore
    type: parallel
    branches:
      - id: research
        type: workflow
        ref: builtin://agent
      - id: design
        type: workflow
        ref: builtin://agent
edges:
  - from: trigger
    default: explore
` + "```" + ``
}

func generateTriggers() string {
	bt := "`"
	return `## Triggers: when a workflow runs

A workflow says WHEN it runs in a top-level ` + bt + `triggers:` + bt + ` list. Declaring a trigger fires nothing.
In an interactive chat, use create_trigger_activation, update_trigger_activation, enable_trigger or pause_trigger
when asked to manage an activation. Each change raises an approval card; nothing changes until the owner approves.
Unattended runs cannot manage activations. Do not bypass approval through the CLI, HTTP or a script. Enabling says AS WHOM and WHERE:
their project, the machine whose tools the runs use, and for an integration trigger which of their connected
accounts it listens through. One declaration can have many activations (one per user or project).

` + "```yaml" + `
name: triage-new-issues
inputs:
  repos: {type: github.repository, multi: true, required: true, min_items: 1}   # typed by the integration; the user picks them when enabling
  issue_number: {type: integer, default: 0}   # required inputs must be mapped by every trigger, or set by the user when enabling
triggers:
  - name: new-issue                 # unique within the workflow: lowercase, digits, - and _
    description: A new issue
    integration:                    # exactly ONE source: schedule | webhook | integration | workflow_event
      integration: github
      events: [issues.opened]                    # "issues.*" or "*" also match
      match: {repository: {input: repos}}        # scope by an input: only the repositories the user picked. A plain value (repository: acme/app) must equal the attribute
    filter: "!('wontfix' in trigger.payload.data.issue.labels)"
    inputs:
      issue_number: "{{ trigger.payload.data.issue.number }}"
    prompt: "Triage issue #{{ trigger.payload.data.issue.number }}: label it, ask for a repro if one is missing."
  - name: nightly
    schedule: {cron: ["0 9 * * 1-5"], timezone: America/New_York}   # 5-field cron, or interval: 1h
  - name: deploy-hook
    webhook: {}                     # POST <api>/hooks/<trigger id>; add hmac: {...} to verify signatures
  - name: alertmanager
    webhook: {source: alerting}     # hosted: Enable connects the org's alerts, no token to copy; self-hosted: a plain webhook
    dedupe_key: "{{ trigger.payload.body.groupKey }}:{{ trigger.payload.body.status }}"   # see below
    dedupe_window: 4h
  - name: after-review-fails
    workflow_event: {workflows: [code-review], outcomes: [failed]}  # finished | failed | blocked
entry: [triage]
nodes: ...
` + "```" + `

**` + bt + `filter` + bt + `** is a raw CEL bool (no ` + bt + `{{ }}` + bt + `) and **` + bt + `inputs` + bt + `** values are ` + bt + `{{ }}` + bt + ` templates. Both read ONLY
` + bt + `trigger` + bt + ` — the run, its inputs and nodes do not exist yet. A schedule has no event, so no filter.
An event whose payload a filter or input cannot read is recorded as a FAILED firing: guard optional
fields with ` + bt + `has()` + bt + ` or a ternary. Each ` + bt + `inputs` + bt + ` key must be a declared workflow input; a value set there wins
over the user's params (enabling drops a param it would override).

**` + bt + `prompt` + bt + `** is the message every run the trigger starts begins from, a ` + bt + `{{ }}` + bt + ` template over ` + bt + `trigger` + bt + ` like
` + bt + `inputs` + bt + `. Write it so enabling needs no prompt: the prompt the user writes when enabling overrides it, and with
neither, enabling is refused. The event is still attached after it as untrusted data.

**` + bt + `trigger.activation` + bt + `** is the activation the event fires for, readable in a ` + bt + `filter` + bt + `, ` + bt + `inputs` + bt + `, ` + bt + `prompt` + bt + ` and ` + bt + `dedupe_key` + bt + ` (not in nodes, which read ` + bt + `inputs` + bt + `):
` + bt + `trigger.activation.params.<input>` + bt + `, ` + bt + `.id` + bt + `, ` + bt + `.owner.user_id` + bt + `, and ` + bt + `.owner.github_id` + bt + ` (the owner's GitHub user id, a string: the connection it listens through if it names one, else the owner's linked GitHub identity; empty when the owner has neither, in which case a filter on it never matches the owner and the skip reason says so). One declaration serves many activations, each with its own inputs. ` + bt + `params` + bt + ` holds the workflow's inputs except those this trigger's ` + bt + `inputs` + bt + ` map from the event, with defaults applied and declared types.
Validation types it from the workflow's inputs: ` + bt + `trigger.activation.params.repo` + bt + ` for a ` + bt + `repos` + bt + ` input is an error when you save.

**Typed inputs: let the integration say what a value is.** An integration defines value types, and an input of that type is a picker in the Enable and Run dialogs, not a text box.
` + bt + `get_integration_schema github` + bt + ` lists them; GitHub has ` + bt + `github.repository` + bt + ` (` + bt + `owner/name` + bt + `, from the repositories the Reliant GitHub App is installed on that the user can see), ` + bt + `github.label` + bt + ` (scoped to a repository), ` + bt + `github.user` + bt + ` (a numeric account id, stored as a string, never a login) and ` + bt + `github.installation` + bt + `.

` + "```" + `yaml
inputs:
  repos:       {type: github.repository, multi: true, required: true, min_items: 1, description: The repositories to review.}
  trusted_ids: {type: github.user, multi: true, default: [], description: People who may ask for a re-review, besides you.}
  skip_label:  {type: github.label, scope: {repository: repos}, default: skip-review}   # offers the labels of the picked repositories
triggers:
  - name: pr-opened
    integration:
      integration: github
      events: [pull_request.opened]
      match: {repository: {input: repos}}
` + "```" + `

- **Never ` + bt + `type: array` + bt + ` (or ` + bt + `string` + bt + `) for something an integration has a type for.** A repository list typed ` + bt + `array` + bt + ` is a comma-separated text box with nothing checking it, and the trigger cannot tell it is a repository scope. If you cannot find a type, run ` + bt + `get_integration_schema <integration>` + bt + `; validation names the types the integration does declare.
- **Values stay plain JSON**, strings for every GitHub type, so ` + bt + `inputs.repos.exists(r, r == inputs.repo)` + bt + ` and ` + bt + `trigger.sender.id in trigger.activation.params.trusted_ids` + bt + ` keep working. ` + bt + `multi: true` + bt + ` makes it a list (` + bt + `min_items` + bt + `/` + bt + `max_items` + bt + ` bound it); ` + bt + `scope: {repository: repos}` + bt + ` binds the type's scope key to another input.
- **` + bt + `required: true` + bt + `** is explicit: the user cannot turn the automation on until the input has a value, and an existing activation missing one shows "Needs setup" rather than going quiet. A required input takes no ` + bt + `default` + bt + ` (validation says so). An input with no default is required too; give a ` + bt + `default` + bt + ` (` + bt + `[]` + bt + ` for a list) only when it is truly optional, like ` + bt + `trusted_ids` + bt + `.
- **` + bt + `match: {attribute: {input: name}}` + bt + ` scopes a trigger by an input.** The event is routed only when its attribute is one of the input's values, so an event for any other repository is dropped before a firing is recorded. The attribute and the input must have the same type (` + bt + `repository` + bt + ` is a ` + bt + `github.repository` + bt + `; validation checks it), the input must not be one the trigger maps from the event, and it should be ` + bt + `required` + bt + `. It combines with literal ` + bt + `match:` + bt + ` entries and runs before ` + bt + `filter` + bt + `. Keep ` + bt + `filter` + bt + ` for what a scope cannot say: drafts, labels, trust. Do not write ` + bt + `trigger.payload.attributes.repository in trigger.activation.params.repos` + bt + ` in a filter; validation warns and points at ` + bt + `match:` + bt + `.
- **Trust by id**: ` + bt + `trigger.sender.id` + bt + ` is the GitHub numeric id, a string. ` + bt + `trigger.sender.verified && (trigger.sender.id == trigger.activation.owner.github_id || trigger.sender.id in trigger.activation.params.trusted_ids)` + bt + ` admits you and the people in a ` + bt + `github.user` + bt + ` input. The event attribute ` + bt + `sender_id` + bt + ` (a ` + bt + `github.user` + bt + `) is the same id: ` + bt + `match: {sender_id: {input: allowed}}` + bt + ` scopes a trigger to the people a ` + bt + `github.user` + bt + ` input lists, on the events that declare ` + bt + `sender_id` + bt + `. Use the filter, as above, when the owner is trusted without being listed.

**` + bt + `dedupe_key` + bt + `** (webhook triggers only) is a ` + bt + `{{ }}` + bt + ` template over ` + bt + `trigger` + bt + ` that names the event a delivery is about. Use
it for a sender with no idempotency header, such as Alertmanager, which re-sends a group with a changed body: the
default key (an idempotency header, else the body hash within one minute) would make every repeat a new run. Deliveries
whose keys render the same within one ` + bt + `dedupe_window` + bt + ` are ONE event (the first starts a run; the rest are recorded as
duplicates). The window is a fixed UTC-aligned clock bucket, default 1h, at most 24h, so it is the longest a repeat is
suppressed, not a sliding guarantee. A key that cannot render for a delivery falls back to the default key. It needs a
` + bt + `{{ }}` + bt + `, and ` + bt + `dedupe_window` + bt + ` needs a ` + bt + `dedupe_key` + bt + `.

**Test a trigger before handing it over.** ` + bt + `cel_eval` + bt + ` with ` + bt + `integration` + bt + ` + ` + bt + `event` + bt + ` generates ` + bt + `trigger.payload` + bt + ` from the payload that
event declares (add ` + bt + `trigger_payload` + bt + ` to override only the fields your expression reads), and ` + bt + `scope: trigger` + bt + ` gives a
filter's, inputs mapping's or prompt's view (only ` + bt + `trigger` + bt + `, activation included; give it ` + bt + `activation_params` + bt + ` and ` + bt + `owner_github_id` + bt + `). It returns the value and its type, or the error with its
phase and position, plus ` + bt + `findings` + bt + ` for reads the declared payload rules out. Do this instead of waiting for a real
event: evaluation does not require enabling the trigger or waiting for a real event. ` + bt + `reliant workflow eval` + bt + ` is the same on the command line, and ` + bt + `reliant workflow validate --trigger-payload` + bt + `
dry-runs every declared trigger (filter, inputs, prompt, dedupe key) against a sample event, with ` + bt + `--activation-params` + bt + ` and ` + bt + `--owner-github-id` + bt + ` for the sample activation.

**Chat is a trigger too, on by default.** Set top-level ` + bt + `automation_only: true` + bt + ` for a workflow only its
` + bt + `triggers:` + bt + ` should start: chat pickers stop offering it and starting it from a chat is refused.
A deliberate start by hand still works: Run now, ` + bt + `reliant workflow run` + bt + `, an agent's ` + bt + `start_run` + bt + ` and a
builder test run. Leave it unset for anything a person starts by chatting. A helper that only
other workflows ` + bt + `ref:` + bt + ` sets ` + bt + `hidden: true` + bt + ` as well: hidden keeps it out of every picker and listing, and
the pair marks it as an automation's internal step.

**Where an automation's runs go.** A run a trigger fires is a chat, but not one in the chat list: it is listed under
Workflows → Runs, named by its automation. Once it finishes successfully it archives itself (still in Runs, marked
Archived). A failed run, a completion into a declared ` + bt + `outcome: failure` + bt + `, a cancelled run and one awaiting input stay
unarchived so they surface. So route a blocked or punted path to a terminal node with ` + bt + `outcome: failure` + bt + ` when a person
should look at it. An automation with "notify me when it finishes" keeps its successful runs too.

What ` + bt + `trigger.payload` + bt + ` holds, by source:

| Source | trigger.payload |
|--------|-----------------|
| schedule | ` + bt + `scheduled_for` + bt + `, ` + bt + `fired_at` + bt + `, ` + bt + `trigger_name` + bt + `, ` + bt + `manual` + bt + ` (also ` + bt + `trigger.scheduled_for` + bt + `) |
| webhook | ` + bt + `body` + bt + ` (parsed JSON or text), ` + bt + `headers` + bt + `, ` + bt + `query` + bt + `, ` + bt + `content_type` + bt + ` |
| integration | ` + bt + `integration` + bt + `, ` + bt + `event` + bt + `, ` + bt + `account` + bt + `, ` + bt + `delivery_id` + bt + `, ` + bt + `attributes` + bt + `, ` + bt + `data` + bt + ` (the provider's payload) |
| workflow_event | ` + bt + `run_id` + bt + `, ` + bt + `chat_id` + bt + `, ` + bt + `workflow_name` + bt + `, ` + bt + `outcome` + bt + `, ` + bt + `summary` + bt + `, ` + bt + `error` + bt + ` |

The payload is untrusted data from outside. Read it in templates; never paste it into a system prompt.

An integration's payload is declared (` + bt + `get_integration_schema` + bt + ` shows it), and validation type-checks the filter, inputs and
prompt against it. Lists of names are strings, not objects: GitHub's ` + bt + `issue.labels` + bt + ` and ` + bt + `issue.assignees` + bt + `, and
` + bt + `data.label` + bt + ` on ` + bt + `issues.labeled` + bt + `. Write ` + bt + `'wontfix' in trigger.payload.data.issue.labels` + bt + `, never ` + bt + `l.name` + bt + `.

GitHub is the fullest integration for automations. Besides ` + bt + `issues.opened` + bt + ` and the like it delivers ` + bt + `issues.labeled` + bt + `/` + bt + `unlabeled` + bt + `/` + bt + `reopened` + bt + `,
` + bt + `pull_request.labeled` + bt + `/` + bt + `unlabeled` + bt + `/` + bt + `reopened` + bt + ` (` + bt + `match: {label: ...}` + bt + `), ` + bt + `pull_request_review_comment.created` + bt + ` (inline diff comments; ` + bt + `match` + bt + ` ` + bt + `is_reply` + bt + `
and ` + bt + `mentions_reliant` + bt + `), and ` + bt + `check_run.completed` + bt + ` / ` + bt + `check_suite.completed` + bt + ` / ` + bt + `workflow_run.completed` + bt + ` (` + bt + `conclusion` + bt + `, ` + bt + `branch` + bt + `, ` + bt + `app` + bt + `).
Its ` + bt + `type: action` + bt + ` nodes cover comments (` + bt + `issue.comment` + bt + `, ` + bt + `.update` + bt + `, ` + bt + `.list` + bt + `), atomic ` + bt + `issue.labels.add` + bt + `/` + bt + `remove` + bt + ` (prefer them to ` + bt + `issue.update` + bt + `,
which replaces the whole label list and races), ` + bt + `issue.search` + bt + `, ` + bt + `labels.create` + bt + ` (idempotent, for pack setup) / ` + bt + `labels.list` + bt + `, ` + bt + `pr.create` + bt + `/` + bt + `update` + bt + `, ` + bt + `pr.reviews.list` + bt + `, ` + bt + `pr.review_comments.list` + bt + ` and
` + bt + `pr.review_comment.reply` + bt + `. There is no merge action, on purpose. Actions that change GitHub are withheld from an unattended run unless the
step names them in its ` + bt + `tools` + bt + `. A trigger on a label event also fires for a label a run itself adds, so guard it.

**` + bt + `trigger.sender` + bt + `** is who sent the event, set by the receiver from what the source authenticated, never from the
payload: ` + bt + `kind` + bt + ` (slack, github, email, sms, webhook, workflow, schedule, user), ` + bt + `id` + bt + `, ` + bt + `display_name` + bt + `, ` + bt + `verified` + bt + `.
Slack's ` + bt + `id` + bt + ` is the user id, GitHub's the numeric user id as a string (` + bt + `"583231"` + bt + `; the login is ` + bt + `display_name` + bt + `, and
since a login can be renamed and then registered by someone else, never allowlist logins), email's the lowercased address (verified only when
Gmail's DMARC, or DKIM aligned with the From domain, passed), sms the From number (never verified). To let only
certain people start runs, filter on it and require ` + bt + `verified` + bt + `:
` + bt + `trigger.sender.verified && trigger.sender.id in ["U123", "U456"]` + bt + `.

**` + bt + `trigger.origin` + bt + `** says whether Reliant itself caused the event, so a workflow can tell its own echoes from other people's. Every
key is always present (an unattributed event reads ` + bt + `caused_by_reliant: false, confidence: 'none'` + bt + `), in ` + bt + `filter` + bt + `, ` + bt + `inputs` + bt + ` and ` + bt + `prompt` + bt + `:

| Key | Meaning |
|-----|---------|
| ` + bt + `caused_by_reliant` + bt + ` | Reliant wrote the thing the event reports (any confidence) |
| ` + bt + `confidence` + bt + ` | ` + bt + `exact` + bt + ` (the event's own object id is one a run's write produced), ` + bt + `intent` + bt + ` (matches a recent write on target and content), ` + bt + `app` + bt + ` (from Reliant's GitHub App, run unknown), ` + bt + `claimed` + bt + ` (a ` + bt + `Reliant-Run:` + bt + ` commit trailer; anyone can write one, so it is only ever a reason to ignore), ` + bt + `none` + bt + ` |
| ` + bt + `run_id` + bt + `, ` + bt + `workflow` + bt + `, ` + bt + `trigger_name` + bt + ` | The run that wrote, its workflow, and the declared trigger that launched it. Empty unless it is YOUR run |
| ` + bt + `same_workflow` + bt + ` | That run is a run of this trigger's own workflow: the event is its own echo |
| ` + bt + `actor` + bt + ` | Who the write was made as: ` + bt + `app` + bt + `, ` + bt + `owner` + bt + ` or ` + bt + `unknown` + bt + ` |
| ` + bt + `lineage_depth` + bt + ` | How many event-driven hops led to the run that wrote (a chain stops at 5) |

A trigger's **` + bt + `origin_filter` + bt + `** is a raw CEL bool evaluated BEFORE ` + bt + `filter` + bt + `. Its default ignores a workflow's own echoes:
` + bt + `!(trigger.origin.same_workflow && trigger.origin.confidence in ['exact', 'intent'])` + bt + `, so a reviewer does not re-run on its own
review. ` + bt + `origin_filter: "true"` + bt + ` receives everything; ` + bt + `!trigger.origin.same_workflow || trigger.origin.trigger_name == 'responder-push'` + bt + `
allows one deliberate re-trigger. A skipped event is recorded with the reason (` + bt + `origin_filter rejected: caused by run X of this
workflow` + bt + `). The lineage depth cap (5) is a hard loop guard no expression overrides, so two workflows that re-trigger each other
still stop. Only writes made through integration actions (` + bt + `type: action` + bt + ` nodes, integration tools) are attributed
exactly; an agent's own shell ` + bt + `gh` + bt + `/` + bt + `git` + bt + ` writes look like the person who owns the machine.

**The loop for an integration trigger:** ` + bt + `search_integrations(kind: trigger)` + bt + ` → ` + bt + `get_integration_schema` + bt + `
(its events, the ` + bt + `match` + bt + ` attributes, the payload schema, and a ready ` + bt + `triggers:` + bt + ` block) → write it with
` + bt + `edit_workflow` + bt + ` (validation reports bad cron, filters, inputs, events and match keys like any other error) →
test its filter and inputs with ` + bt + `cel_eval` + bt + ` and its graph with ` + bt + `run_scenario` + bt + ` → tell the user the workflow is ready (below).

**When the workflow is ready**, say it is saved and validated. If asked to activate it in an interactive chat,
use create_trigger_activation with its declared trigger name, typed params, project_id, explicit machine_id (or
no_machine) and connection_id. The approval card shows the resolved workflow, identity, machine and privileges.
Use update_trigger_activation to re-point an existing activation, enable_trigger to resume, and pause_trigger
to pause. Never claim success before approval and a successful tool result. list_triggers shows the result and health.
The app remains an alternative: **Workflows → Library → open the workflow → Enable** turns on all declared triggers.
Unattended runs are refused: they cannot grant themselves standing privileges.

**Editing the YAML changes every activation.** An activation reads its declaration from the workflow
each time it fires, so a new cron, filter or input mapping applies on the next event. Renaming or
removing a declaration, or changing its source kind or integration, leaves its activations BROKEN:
they fire nothing, report why in ` + bt + `list_triggers` + bt + `, and recover when the declaration is restored (or are
enabled again by the user). Runs a trigger starts are unattended — nobody answers questions or approvals.

**Who the run writes as.** New GitHub integration activations default to ` + bt + `acting_identity: app` + bt + ` (GitHub comments,
reviews and pull requests appear as the Reliant bot, ` + bt + `reliant-labs[bot]` + bt + `). Other sources, including schedules,
webhooks and workflow events, still default to ` + bt + `owner` + bt + ` (writes appear as the user); a scheduled GitHub writer needs an
explicit app choice. Existing activations retain their stored identity, and explicit owner choices stay owner.
Only GitHub supports app identity; do not promise a bot identity for another integration. It is the
ACTIVATION's choice, made in the app, not the workflow's and not yours: nothing in the YAML, an ` + bt + `action` + bt + ` node's ` + bt + `with:` + bt + ` or a prompt can
switch it, and a person typing in the run's chat acts as themselves. It covers every integration action the run takes, an
` + bt + `action` + bt + ` node and an agent's integration tools alike, and only the writes: reads use the owner's access. Everything the bot
authors ends with a footer naming the automation and its owner, so do not write your own signature. The bot only writes to a
repository the owner can push to, scoped to that one repository; if the owner cannot push, the Reliant GitHub App is not
installed there, the App lacks a permission, the deployment has no App key, or minting fails, the write FAILS with the reason (it never falls back to the owner's name or to a saved connection; a write that names a saved connection on a bot run fails too, and a git_publish push is refused the same way),
so a workflow that must keep going should handle the action's error output. When you hand a workflow to the user, say that
new GitHub triggers default to the bot; scheduled GitHub writers need the user to choose it explicitly. A deployment
without a control plane refuses the bot identity on save; the user must configure the App or explicitly choose owner. Comments written by Reliant's own bot never fire
` + bt + `issue_comment.created` + bt + ` or ` + bt + `pull_request_review_comment.created` + bt + `, so a run that comments cannot trigger itself; other bots' comments do.

### When a run fails: ` + bt + `on_failure:` + bt + `

A workflow-level ` + bt + `on_failure:` + bt + ` is a finally-style handler: an inline graph (` + bt + `entry` + bt + `, ` + bt + `nodes` + bt + `, ` + bt + `edges` + bt + `, or ` + bt + `sequence:` + bt + `) that runs ONCE when a run
FAILS, so the people waiting on it are told instead of finding a silent dead end.

` + "```yaml" + `
on_failure:
  sequence:
    - id: note
      type: save_message
      args:
        role: assistant
        display_style: warning
        content: "Failed at {{ failure.node_id }} ({{ failure.error_code }}): {{ failure.error }}. prep: {{ nodes.?prep.n.orValue('-') }}."
` + "```" + `

- **It runs when** a node's error was not handled (a config that will not evaluate, a sub-workflow that failed, an agent that never produced its response and ended the step), or the graph reached a node that declares ` + bt + `outcome: failure` + bt + `. It does not run when the run completes, pauses (a step that exhausts its retries pauses the run, it does not fail it), or is cancelled.
- **` + bt + `failure` + bt + `** exists only inside the handler: ` + bt + `failure.node_id` + bt + `, ` + bt + `failure.node_path` + bt + ` (dotted, through loops and sub-workflows), ` + bt + `failure.error` + bt + ` (one line), ` + bt + `failure.error_code` + bt + ` (` + bt + `node_error` + bt + `, ` + bt + `outcome` + bt + ` or ` + bt + `panic` + bt + `), ` + bt + `failure.attempt` + bt + ` (the loop iteration, 1 if none). ` + bt + `inputs` + bt + ` are the run's. ` + bt + `nodes` + bt + ` are the outputs the run had produced when it failed, as a read-only copy.
- **Guard every ` + bt + `nodes.<main node>` + bt + ` read** with ` + bt + `has()` + bt + ` or ` + bt + `.?` + bt + `: whichever node failed, the ones after it never ran, and validation rejects an unguarded read. A handler node may not reuse a main-graph node id, and ` + bt + `on_failure` + bt + ` itself is a reserved id.
- **It cannot mask the failure.** The run still ends FAILED. An error inside the handler (or a handler that outlives its 5-minute budget) is logged and recorded, never handled and never recursive; it has no ` + bt + `outputs` + bt + `, ` + bt + `inputs` + bt + ` or nested ` + bt + `on_failure` + bt + `. It can contain actions, ` + bt + `save_message` + bt + `, sub-workflows and agents, but no ` + bt + `approval` + bt + ` or ` + bt + `ask_question` + bt + `: nobody is answering.
- **Make a failure visible.** A run that ends on a path labelled "blocked", "punted" or "no result" but finishes normally is a success to the platform: it is auto-archived and never reaches the handler. Route those paths to a terminal node with ` + bt + `outcome: failure` + bt + `.
- **Tell a person once.** The Autopilot pack's ` + bt + `builtin://autopilot-failure-note` + bt + ` posts one comment per pull request head commit (or issue): it searches the thread for its own marker first, posts nothing if the thread cannot be read, and writes as whoever the run acts as.
- **Test it.** A scenario names the handler's nodes ` + bt + `on_failure.<node>` + bt + ` in ` + bt + `events` + bt + `, ` + bt + `reached` + bt + `, ` + bt + `completed` + bt + ` and ` + bt + `messages` + bt + `, and asserts ` + bt + `expect.on_failure: {ran, node_id, node_path, error_code, attempt, error_contains, handler_failed}` + bt + `.`
}

func generateGotchas() string {
	return `## Important rules

### Parameters
- Set model as a param via a **tag** (flagship, moderate, cheap, etc.) — not a hardcoded model ID
- **NEVER** assume which models exist. Your training data may be stale, and users have different API keys.
- A workflow always runs in the context of a user thread. You never need to create input to consume the user's request.

### Agents and LLMs
- ` + "`CallLLM`" + ` is a single LLM call — it doesn't execute tools or loop
- Common patterns:
  - **Augmented agents**: Do additional work inside the agentic loop (e.g., auditing)
  - **Parallelization**: Multiple edges from the same source (not switch/case logic)
  - **Combining agents**: Multiple specialized agents with distinct tools/instructions
  - **Structured output**: Use response tools to produce output for conditional routing
- Combine patterns to create powerful workflows

### Loops
- ` + "`while`" + ` is **do-while**: the body always runs at least once
- ` + "`iter.iteration`" + ` is 0-indexed inside the loop body
- ` + "`outputs.*`" + ` in the ` + "`while`" + ` condition references the **current iteration's** outputs

### Response tools
- Force structured LLM output for routing/classification
- ` + "`builtin://agent`" + ` returns when no tool calls remain (use ` + "`ask: true`" + ` to prompt for user feedback first)
- ` + "`builtin://structured-agent`" + ` returns when the response tool is called (use ` + "`ask: true`" + ` to prompt for user feedback first)
- Access structured output via ` + "`nodes.<execute_tools_id>.response_data.<tool_name>`" + `

### Conditions on nodes
- Skipped nodes forward execution to the next edge target
- You **cannot** access outputs of skipped nodes
- Join nodes handle skipped inputs correctly`
}

func generateAvailableTools() string {
	return `## Available tools

Granted by ` + "`tag:workflow`" + ` — no ` + "`load_tool`" + ` needed in a workflow-building chat. All take the
required ` + "`id`" + ` (UUID/slug/name) described above except ` + "`create_workflow`" + `, which mints one, and
the discovery/reference tools (` + "`list_workflows`" + `, ` + "`list_presets`" + `, ` + "`get_preset`" + `, ` + "`get_schema`" + `, ` + "`get_cel_reference`" + `, ` + "`cel_eval`" + `, ` + "`get_workflow_suggestions`" + `).

| Tool | Purpose |
|------|---------|
| ` + "`create_workflow`" + ` | Create a new workflow draft (returns id, name, slug) |
| ` + "`get_workflow`" + ` | Read current workflow content |
| ` + "`edit_workflow`" + ` | Make targeted edits to a workflow |
| ` + "`write_workflow`" + ` | Full workflow rewrite |
| ` + "`get_schema`" + ` | Get full field documentation for any node/input/shared type |
| ` + "`get_cel_reference`" + ` | Authoritative CEL reference (namespaces, functions, types) |
| ` + "`cel_eval`" + ` | Evaluate a CEL expression or template against a sample trigger (generated from an integration event's declared payload), inputs and node outputs: value and type, or a precise error. Test a filter in one call |
| ` + "`search_integrations`" + ` | Find an integration action ref (GitHub, Slack, HTTP, ...) for a ` + "`type: action`" + ` node's ` + "`uses:`" + ` |
| ` + "`get_integration_schema`" + ` | Read one integration action's params (` + "`with:`" + `) and output (` + "`nodes.<id>.data`" + `) schemas |
| ` + "`list_triggers`" + ` | List the triggers the user has enabled (optionally one workflow's) with health; read-only. BROKEN means the declaration it activates is gone or changed |
| ` + "`fire_trigger`" + ` | Manually fire an existing activation once, including a paused one; never creates, enables or changes it. Orchestrator-tier; returns an asynchronous fire_workflow_id, not a successful run |
| ` + "`list_trigger_events`" + ` | Read firing history without source payloads. Match fire_workflow_id exactly, then inspect the launched chat_id with get_run; paginate with next_page_token |
| ` + "`list_workflows`" + ` | Browse existing workflows for examples and patterns |
| ` + "`list_presets`" + ` | Discover available agent presets |
| ` + "`get_preset`" + ` | View a preset's full configuration |
| ` + "`write_scenario`" + ` | Create a test scenario |
| ` + "`run_scenario`" + ` | Execute a scenario and check results |
| ` + "`list_scenarios`" + ` | List existing scenarios for a workflow |
| ` + "`view_scenario`" + ` | Read a scenario's content |
| ` + "`edit_scenario`" + ` | Modify an existing scenario |
| ` + "`delete_scenario`" + ` | Remove a scenario |
| ` + "`get_workflow_suggestions`" + ` | Get AI-powered suggestions for workflow improvements |

Outside a workflow-building chat, these tools aren't preloaded, but ` + "`load_tool(name=\"tag:workflow\")`" + `
is always available (it's in ` + "`tag:coding:default`" + `) and loads the full set on demand. Every tool that targets an
existing workflow requires its ` + "`id`" + ` (returned by ` + "`create_workflow`" + `, or listed by ` + "`list_workflows`" + `).`
}

func generateCELQuickReference() string {
	var sb strings.Builder
	sb.WriteString("## CEL Reference\n\n")
	sb.WriteString("> Auto-generated from `internal/workflow/reference`. Use `get_cel_reference` for the complete authoritative reference.\n\n")

	sb.WriteString("### Syntax rules\n\n")
	sb.WriteString("| Context | Syntax | Example |\n")
	sb.WriteString("|---------|--------|--------|\n")
	sb.WriteString("| `condition`, `while` | Pure CEL (no `{{}}`) | `condition: \"nodes.llm.stop_reason == 'tool_use'\"` |\n")
	sb.WriteString("| All other fields | Template interpolation `{{}}` | `model: \"{{inputs.model}}\"` |\n\n")

	namespaces := append([]reference.CELNamespace(nil), reference.CELNamespaces...)
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i].Name < namespaces[j].Name })

	sb.WriteString("### Namespaces\n\n")
	sb.WriteString("| Namespace | Description | Fields |\n")
	sb.WriteString("|-----------|-------------|--------|\n")
	for _, ns := range namespaces {
		fields := "workflow-specific"
		if len(ns.Fields) > 0 {
			fieldNames := make([]string, 0, len(ns.Fields))
			for _, field := range ns.Fields {
				fieldNames = append(fieldNames, "`"+field.Name+"`")
			}
			fields = strings.Join(fieldNames, ", ")
		}
		fmt.Fprintf(&sb, "| `%s.*` | %s | %s |\n", tableCell(ns.Name), tableCell(ns.Description), tableCell(fields))
	}
	sb.WriteString("\n")

	for _, ns := range namespaces {
		if len(ns.Fields) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "#### `%s` fields\n\n", ns.Name)
		sb.WriteString("| Field | Type | Description |\n")
		sb.WriteString("|-------|------|-------------|\n")
		fields := append([]reference.CELField(nil), ns.Fields...)
		sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
		for _, field := range fields {
			fmt.Fprintf(&sb, "| `%s.%s` | `%s` | %s |\n", tableCell(ns.Name), tableCell(field.Name), tableCell(field.Type), tableCell(field.Description))
		}
		sb.WriteString("\n")
	}

	functions := append([]reference.CELFunction(nil), reference.CELFunctions...)
	sort.Slice(functions, func(i, j int) bool { return functions[i].Name < functions[j].Name })

	sb.WriteString("### Key functions\n\n")
	sb.WriteString("| Function | Description | Example |\n")
	sb.WriteString("|----------|-------------|--------|\n")
	for _, fn := range functions {
		signature := fn.Signature
		if signature == "" {
			signature = fn.Name
		}
		fmt.Fprintf(&sb, "| `%s` | %s | `%s` |\n", tableCell(signature), tableCell(fn.Description), tableCell(fn.Example))
	}
	sb.WriteString("\n")

	sb.WriteString(`### Common patterns

` + "```yaml" + `
# Check if LLM wants to use tools
condition: "nodes.llm.stop_reason == 'tool_use'"

# Agentic loop — keep going while the model has more to do. stop_reason is a
# closed set: tool_use | incomplete | truncated | interrupted | refused | error | done
while: "outputs.stop_reason in ['tool_use', 'incomplete', 'truncated', 'interrupted'] && iter.iteration < 50"

# Dynamic model from input
model: "{{inputs.model}}"

# Safe field access with has()
condition: "has(nodes.classify) && nodes.classify.response_data.route.category == 'urgent'"

# Safe navigation with ?.
condition: "nodes.result.?error_message == null"

# Parse JSON from command output in an interpolated field
result: "{{parseJson(nodes.run.stdout)}}"

# Coalesce to provide defaults
name: "{{coalesce(inputs.name, 'anonymous')}}"
` + "```" + `

### Null safety

Always guard optional field access:

` + "```yaml" + `
# Good — use has()
condition: "has(nodes.check) && nodes.check.exit_code == 0"

# Good — safe navigation
condition: "nodes.result.?data != null"

# Bad — errors if node was skipped or field doesn't exist
condition: "nodes.check.exit_code == 0"
` + "```")

	return sb.String()
}

func generateBuiltinWorkflowsList() (string, error) {
	var sb strings.Builder
	sb.WriteString("## Builtin Workflows\n\n")
	sb.WriteString("> Auto-generated from `internal/workflow/builtin/*.yaml`. Reference via `builtin://<name>` in workflow nodes.\n\n")
	sb.WriteString("| Name | Description |\n")
	sb.WriteString("|------|-------------|\n")

	entries, err := builtin.BuiltinWorkflowsFS.ReadDir(".")
	if err != nil {
		return "", err
	}

	var workflows []BuiltinWorkflow
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}

		data, err := builtin.BuiltinWorkflowsFS.ReadFile(entry.Name())
		if err != nil {
			continue
		}

		var wf BuiltinWorkflow
		if err := yaml.Unmarshal(data, &wf); err != nil {
			continue
		}

		if wf.Name == "" {
			continue
		}
		wf.Description = firstParagraph(wf.Description)
		workflows = append(workflows, wf)
	}

	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].Name < workflows[j].Name
	})

	for _, wf := range workflows {
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(wf.Name), tableCell(wf.Description))
	}

	return sb.String(), nil
}

func generateNodeTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Node Types\n\n")
	sb.WriteString("> Auto-generated from `reference.ListNodeTypes()` / `reference.GetNodeType()`. Use `get_schema(name=\"<type>\")` for full field documentation.\n\n")

	sb.WriteString("### Common Fields (all nodes)\n\n")
	sb.WriteString("| Field | Type | Description |\n")
	sb.WriteString("|-------|------|-------------|\n")
	sb.WriteString("| `type` | string | Node type (required) |\n")
	sb.WriteString("| `id` | string | Unique node ID (required) |\n")
	sb.WriteString("| `condition` | CEL | Skip if false; on join nodes: `all` or `any` |\n")
	sb.WriteString("| `thread` | ThreadConfig | Thread mode: `inherit`, `new`, `fork` |\n")
	sb.WriteString("| `timeout` | string | Override timeout (e.g., `5m`) |\n")
	sb.WriteString("| `save_message` | SaveMessageConfig | Auto-save message after completion |\n\n")

	sb.WriteString("### Available Types\n\n")
	sb.WriteString("| Type | Description |\n")
	sb.WriteString("|------|-------------|\n")

	nodeTypes := reference.ListNodeTypes()
	sort.Strings(nodeTypes)
	for _, typeName := range nodeTypes {
		info, ok := reference.GetNodeType(typeName)
		if !ok {
			continue
		}
		desc := summaryForName(info.Name, info.Summary, info.Description)
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(typeName), tableCell(desc))
	}

	return sb.String()
}

func generateInputTypesSummary() string {
	var sb strings.Builder
	sb.WriteString("## Input Types\n\n")
	sb.WriteString("> Auto-generated from `reference.ListInputTypes()` / `reference.GetInputType()`. Use `get_schema(name=\"<type>\")` for full documentation.\n\n")
	sb.WriteString("| Type | Description |\n")
	sb.WriteString("|------|-------------|\n")

	inputTypes := reference.ListInputTypes()
	sort.Strings(inputTypes)
	for _, typeName := range inputTypes {
		info, ok := reference.GetInputType(typeName)
		if !ok {
			continue
		}
		desc := summaryForName(info.Name, info.Summary, info.Description)
		fmt.Fprintf(&sb, "| `%s` | %s |\n", tableCell(typeName), tableCell(desc))
	}

	return sb.String()
}

func extractTopLevelSchema(schemaPath string) (string, error) {
	content, err := os.ReadFile(schemaPath)
	if err != nil {
		return "", fmt.Errorf("reading workflow schema: %w (run 'task generate:docs:schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	var sb strings.Builder
	sb.WriteString("## Workflow Structure\n\n")
	sb.WriteString("> Auto-generated from `generated/docs-source/reference/workflow-schema.md`.\n\n")

	for index, name := range []string{"Workflow", "Edge", "EdgeCase"} {
		section := extractSection(text, "## "+name, "\n---")
		if section == "" {
			continue
		}
		if index > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimSpace(section))
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

func extractScenarioEssentials(scenarioPath string) (string, error) {
	content, err := os.ReadFile(scenarioPath)
	if err != nil {
		return "", fmt.Errorf("reading scenario schema: %w (run 'task generate:docs:scenario-schema' first)", err)
	}

	text := cleanMarkdown(string(content))

	var sb strings.Builder
	sb.WriteString("## Scenario Testing\n\n")
	sb.WriteString("Test workflows by simulating LLM and tool responses without making real API calls.\n\n")
	sb.WriteString("> Auto-generated schema details from `generated/docs-source/reference/scenario-schema.md`.\n\n")

	for _, schema := range []struct {
		Name  string
		Title string
	}{
		{Name: "Scenario", Title: "Scenario fields"},
		{Name: "SimulatedEvent", Title: "Event fields"},
		{Name: "Expectation", Title: "Expectation fields"},
	} {
		section := extractSection(text, "## "+schema.Name, "\n---")
		if section == "" {
			continue
		}
		summary := extractSectionSummary(section)
		fieldsTable := extractFieldsTable(section)
		if fieldsTable == "" {
			continue
		}
		fmt.Fprintf(&sb, "### %s\n\n", schema.Title)
		if summary != "" {
			sb.WriteString(summary)
			sb.WriteString("\n\n")
		}
		sb.WriteString(fieldsTable)
		sb.WriteString("\n\n")
	}

	if targeting := extractSection(text, "## Targeting Nodes", "\n---"); targeting != "" {
		sb.WriteString(demoteHeading(strings.TrimSpace(targeting)))
		sb.WriteString("\n\n")
	}

	if events := extractSection(text, "## Event Types", "\n---"); events != "" {
		sb.WriteString(demoteHeading(strings.TrimSpace(events)))
		sb.WriteString("\n\n")
	}

	sb.WriteString(`### Use the raw ` + "`output:`" + ` form, not ` + "`type:`" + `

Every event needs either ` + "`output:`" + ` (a raw activity output map) or ` + "`type:`" + ` (a convenience wrapper
like ` + "`llm_response`" + `/` + "`tool_result`" + ` that gets converted into one). **Write ` + "`output:`" + ` directly.** It is
the form nearly every builtin scenario (` + "`internal/workflow/builtin/<workflow>/scenarios/`" + `, which CI runs) uses, and it is
the only form that exposes real activity field names — ` + "`response_data`" + `, ` + "`thread_token_count`" + `,
` + "`has_feedback`" + `, ` + "`tool_results`" + ` — which you need for routing and compaction scenarios and which the
` + "`type:`" + ` wrapper hides.

### ` + "`completed:`" + ` proves a node ran — ` + "`reached:`" + ` does not

` + "`reached:`" + ` passes for anything **scheduled**, including a node skipped by a false condition — per
the field doc above, it covers nodes "completed, skipped, or errored." So ` + "`reached: [handler]`" + `
is satisfied even when ` + "`handler`" + ` never actually executed. **If a scenario is meant to prove a
branch executed, assert ` + "`completed:`" + `, not ` + "`reached:`" + `.** This is the single most common way a
scenario passes without testing what its author intended.

A node can legitimately land in both ` + "`completed:`" + ` and ` + "`skipped:`" + ` across one run — e.g. a
loop-body node that runs on iteration 0 and is skipped on iteration 1. Real example, from
` + "`internal/workflow/builtin/get-it-right/scenarios/stuck_feedback_rereviews_without_reimplementing.yaml`" + `:

` + "```yaml" + `
expect:
  outcome: completed
  reached:
    - attempt.implement   # scheduled at least once — true either way, proves nothing about iteration 1
  skipped:
    - attempt.implement   # the assertion that actually proves iteration 1 skipped it
` + "```" + `

Node state is a set of facts about the whole run, not a single verdict per node.

### Example scenario

Verbatim from ` + "`internal/workflow/builtin/agent/scenarios/compaction_at_default_threshold.yaml`" + `, run against ` + "`builtin://agent`" + `.
It exercises a tool call whose output crosses the compaction token threshold, so the ` + "`compact`" + `
node runs before the loop's next iteration:

` + "```yaml" + `
name: compaction_at_default_threshold
description: Token count exceeds the default compaction threshold after tool execution, compact node runs
inputs:
  mode: auto
events:
  # LLM requests a tool that returns lots of data
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Let me read a large file."
      response_text: "Let me read a large file."
      tool_calls:
        - id: call_0
          name: view
          input: '{"file_path":"/path/to/large/file.go"}'
  # Tool returns with token count above the 185000 default threshold
  - node: agent_loop.execute_tools
    output:
      message:
        role: tool
        text: ""
      tool_results:
        - tool_call_id: call_0
          content: "Very large file content..."
      thread_token_count: 200000
  # Compact node runs due to high token count
  - node: agent_loop.compact
    output:
      message:
        role: assistant
        text: "Context compacted successfully."
  # Next iteration: LLM provides final response
  - node: agent_loop.call_llm
    output:
      message:
        role: assistant
        text: "Here's the analysis of the large file."
      response_text: "Here's the analysis of the large file."
      tool_calls: []
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
    - agent_loop.compact
  not_reached:
    - agent_loop.approval
` + "```" + `

Notes on this example, all load-bearing:

- ` + "`inputs:`" + ` overrides workflow inputs for just this scenario (here, selecting ` + "`mode: auto`" + ` to skip
  the approval node).
- ` + "`not_reached:`" + ` is the primary way to prove a conditional path was actually SKIPPED, not just that
  the scenario didn't happen to visit it — assert it on every branch you didn't take.
- The loop node itself (` + "`agent_loop`" + `) belongs in ` + "`reached`" + ` alongside its children — a loop that never
  entered its body wouldn't reach ` + "`agent_loop.call_llm`" + ` either.
- Field names on ` + "`output:`" + ` are the real activity output fields for that node type: ` + "`call_llm`" + ` gives
  ` + "`message`" + `, ` + "`response_text`" + `, ` + "`tool_calls`" + `, ` + "`token_count`" + `; ` + "`execute_tools`" + ` gives ` + "`tool_results`" + `,
  ` + "`response_data`" + ` (structured output keyed by response-tool name), ` + "`thread_token_count`" + `;
  ` + "`ask_question`" + ` gives ` + "`has_feedback`" + `. Use ` + "`get_schema(name=\"<node_type>\")`" + ` for the full field list of
  any node type before guessing at output shapes.

### Asserting workflow outputs with ` + "`outputs:`" + `

` + "`outputs:`" + ` checks the workflow's declared outputs (as opposed to ` + "`node_outputs:`" + `, which checks a
specific node's raw activity output). Keys support dotted paths into structured values, and matching
is partial — only the paths you list are checked. Verbatim from
` + "`internal/workflow/builtin/structured-agent/scenarios/happy_path.yaml`" + `:

` + "```yaml" + `
expect:
  outcome: completed
  reached:
    - agent_loop
    - agent_loop.call_llm
    - agent_loop.execute_tools
  outputs:
    response.choice: "complete"
    response.value: "Task completed successfully. The analysis shows positive results."
    completed: true
` + "```" + `

### Where scenario files live

A project workflow's scenarios live in ` + "`.reliant/workflows/<slug>/scenarios/`" + `, **one scenario per
file**, where ` + "`<slug>`" + ` is the workflow's ` + "`name:`" + ` as a slug — not its file name. ` + "`blog.yaml`" + ` declaring
` + "`name: blog-content-pipeline`" + ` keeps its scenarios in ` + "`blog-content-pipeline/scenarios/`" + `:

` + "```" + `
.reliant/workflows/
  blog.yaml                          # name: blog-content-pipeline
  blog-content-pipeline/scenarios/
    drafts_from_brief.yaml           # one scenario; its name: defaults to the file stem
` + "```" + `

The app indexes exactly this layout, and ` + "`reliant workflow scenario run`" + ` /
` + "`reliant workflow scenario list`" + ` read it the same way, so a scenario the CLI runs is one the app lists.
A file holding several scenarios (` + "`---`" + `-separated, or a ` + "`scenarios:`" + ` list) is an error: the app
identifies a project scenario by its file. The retired layouts — ` + "`scenarios/<file>/`" + ` and
` + "`<file>_scenarios.yaml`" + ` — are an error naming where each file belongs.

The same rule addresses sub-workflows: ` + "`ref: project://blog-content-pipeline`" + ` names the workflow by its
` + "`name:`" + `, never its file name, in a run, in a scenario and in ` + "`reliant workflow validate`" + ` alike.
` + "`validate`" + ` follows every ` + "`project://`" + ` ref transitively and reports a broken one with the whole chain
(` + "`a.yaml → project://b → project://c: no project workflow is named \"c\"`" + `) — and a ref cycle as an error.

The builtin workflows keep their scenarios in this same layout
(` + "`internal/workflow/builtin/<workflow>/scenarios/`" + `), embedded in the binary;
` + "`reliant workflow scenario run --include-builtins`" + ` runs them alongside the project's.

### Reading ` + "`run_scenario`" + ` output

` + "`run_scenario`" + ` reports, in order:

- **Status and outcome** — ` + "`✓ PASSED`" + `/` + "`✗ FAILED`" + `/` + "`⚠ ERROR`" + `, plus actual vs. expected outcome when they
  differ.
- **` + "`completed:`" + ` and, if any, ` + "`skipped:`" + ` and ` + "`errored:`" + `** — every node broken out by its actual
  execution state, not merged into one "reached" list. A node showing under ` + "`skipped:`" + ` when you
  expected it to run, or absent from ` + "`completed:`" + ` entirely, tells you which fix to make: "never
  scheduled" means a routing/edge problem, "skipped" means the node's own condition was false.
- **Error details** — for a workflow-level error, the failing node, step, message, and (if the failure
  was a condition evaluation) the CEL expression itself.
- **Warnings**, under "Warnings — this run may not have tested what you think" — printed even on a
  PASS. See the next section; do not skip a warning just because the run went green.
- **Failed expectations** — on a FAIL, each mismatch (e.g. a node in ` + "`not_reached`" + ` was actually
  scheduled) annotated with that node's actual state, e.g. ` + "`[handle_question was skipped]`" + `, so you
  don't have to cross-reference the completed/skipped lists yourself.
- **Workflow outputs and node outputs** — the actual values, truncated, so you can see what to assert
  against without a second run.

### Warnings: a PASS can still test nothing

Two warnings print even when the scenario passes, because a passing assertion can coexist with a run
that never exercised what its name claims to test:

- **Black-boxed sub-workflow** — ` + "`type: workflow`" + ` with a ` + "`ref:`" + ` (not ` + "`inline:`" + `) executes as one
  opaque unit unless the scenario targets its internals with qualified node ids. If nothing in
  ` + "`events:`" + ` or ` + "`expect:`" + ` uses ` + "`<node_id>.<inner_id>`" + `, the sub-workflow's output came straight from
  your scenario's mock and none of its own logic ran. Fix: mock its internals directly, e.g.
  ` + "`node: research.search`" + `, or accept that this scenario only tests the caller's wiring and say so
  in the description.
- **Unmocked node router defaulted to ` + "`candidates[0]`" + `** — a ` + "`type: router`" + ` node in node-routing mode
  (` + "`router.nodes`" + ` set) that never receives a mocked ` + "`selected_node`" + ` silently falls back to its
  first candidate. A scenario can pass while asserting nothing about which branch was chosen. **If
  you are testing routing, you must mock the router explicitly** — otherwise the scenario proves
  nothing about the routing decision, no matter what its name says. Fix, verbatim from
  ` + "`internal/workflow/builtin/pitch-deck/scenarios/start_from_research.yaml`" + `:

  ` + "```yaml" + `
  events:
    - node: classify
      output:
        selected_node: research_competitors
        reasoning: "Website analysis already exists — run the research phase."
  ` + "```" + `

  A corpus survey found 96 of 159 scenarios (60%) black-box at least one sub-workflow, and 27 (17%)
  have a silently-defaulted router — including scenarios whose names (e.g.
  ` + "`implement_only`" + ` or ` + "`plan_only`" + `) assert a routing outcome ` + "`classify`" + ` never actually made.
  Don't let this happen to your scenario: if the point is "task X gets classified as Y," mock ` + "`classify`" + `.

### Responding to a black-box warning: three options, pick deliberately

You have three ways to respond, and the warning itself cannot tell you which is right — that judgment
call is why the flag exists:

1. **Make it transparent.** Add an event (or an ` + "`expect:`" + ` reference) targeting a node STRICTLY
   INSIDE the ref with a qualified id, e.g. ` + "`node: research.search`" + `. This is the right move whenever
   the sub-workflow's own logic — its routing, its loop exit condition, an expression evaluated inside
   it — is part of what this scenario is supposed to prove.
2. **Declare it intentional with ` + "`black_box: true`" + `.** Add the flag to the event targeting the node
   itself. This is the right move when the sub-workflow is genuinely not what this scenario tests —
   e.g. mocking ` + "`builtin://agent`" + ` to test the pipeline stages around it — and you want the aggregate
   output without paying for a full simulated agent loop.
3. **Leave the warning.** If the scenario cannot reasonably be converted (e.g. it predates this
   convention and reworking it is out of scope), leaving the warning stand is better than converting it
   incorrectly. Do not silence it with ` + "`black_box: true`" + ` just to make the warning go away — see below.

` + "`black_box: true`" + ` is a claim about INTENT, not a way to quiet the output. Setting it does not
change what ran; it tells the tool you already knew the body wouldn't run and that's what you meant.
The node genuinely does not execute, so anything inside it — every inner condition, loop, and
expression — is untested by this scenario, same as before you added the flag. Reach for it only when
you have actually decided the body is out of scope, never as a reflex to a warning you have not read.

Concrete precedent for why the distinction matters: ` + "`parallel-compete.yaml`" + `'s parallel loop body
referenced ` + "`iter.item.num`" + `, which never resolved at runtime — the loop's own iteration binding was
broken. No scenario noticed, because every one of them black-boxed the loop as a unit (mocking its
aggregate ` + "`_results`" + `/` + "`_completed`" + ` output) instead of letting the body execute, so the failing
expression was never even compiled. The bug shipped to production and stayed invisible in a fully
green corpus. Loop bodies now execute by default for exactly this reason — this is the one case where
opting back in with ` + "`black_box: true`" + ` needs a real reason, not a reflex.

Real example, verbatim from ` + "`internal/workflow/builtin/pitch-deck/scenarios/start_from_research.yaml`" + `,
declaring a parallel loop black-box deliberately because the scenario tests the caller's aggregation of
per-slide results, not the per-slide pipeline itself:

` + "```yaml" + `
- node: slide_pipeline
  # Deliberate black box: this scenario mocks the parallel loop's AGGREGATE
  # result (_results/_completed) rather than exercising the per-slide body.
  # Loop bodies execute by default; this opts out explicitly.
  black_box: true
  output:
    _completed: 1
    _failed: 0
    _parallel: true
    _results:
      "01-hook.md":
        response_text: "Wrote hook slide."
` + "```" + `

The same flag applies to a ref-mode ` + "`type: workflow`" + ` node: set ` + "`black_box: true`" + ` on the event
targeting the node itself (not an internal qualified id) to declare its opacity intentional and
silence the warning for that node.

### Best practices

- **Aim for 3+ scenarios per workflow** — cover the happy path, error cases, and edge cases
- **` + "`outcome:`" + ` accepts three values** — ` + "`completed`" + `, ` + "`error`" + ` (workflow terminated with an error;
  pair with ` + "`error_contains`" + `/` + "`error_node`" + `), and ` + "`failed`" + ` (e.g. validation failure, distinct from a
  runtime error)
- **Use ` + "`completed:`" + ` to prove a branch ran, not ` + "`reached:`" + `** — see above; ` + "`reached:`" + ` also passes
  for skipped nodes
- **Test routing logic by mocking the router** — ` + "`reached:`" + `/` + "`completed:`" + ` on the destination node
  proves nothing if the router itself defaulted; read the warnings section
- **Test loop termination** — verify loops exit correctly (both via ` + "`while`" + ` condition and max iterations)
- **Test skipped nodes with ` + "`skipped:`" + `**, not ` + "`not_reached:`" + ` — a node can be scheduled and skipped
  (` + "`reached`" + `/` + "`skipped`" + `) or never scheduled at all (` + "`not_reached`" + `); they are different failure modes
- **Use ` + "`start_at`" + `** — to test specific sections of complex workflows without simulating the entire flow
- **Use ` + "`state`" + `** — to pre-populate node outputs when testing downstream logic
- **Name scenarios descriptively** — e.g., ` + "`error_handling_api_failure`" + ` not ` + "`test_3`" + `
- **Try to break it** — simulate unexpected LLM responses, tool failures, and edge cases. Finding bugs in scenarios is much cheaper than finding them during a real 1-hour workflow run.`)

	return strings.TrimSpace(sb.String()), nil
}

func generateReferences() string {
	return `## References

- Use ` + "`get_cel_reference`" + ` for the complete, auto-generated CEL reference
- Use ` + "`get_schema(name=\"<type>\")`" + ` for full field docs on any node type, input type, or shared type
- Use ` + "`list_workflows`" + ` to find real-world examples and established patterns`
}

func resolveReferenceFile(docsDir, filename string) (string, error) {
	candidates := []string{
		filepath.Join(docsDir, "reference", filename),
		filepath.Join(docsDir, filename),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find %s under %s or %s (run 'task generate:docs:schema generate:docs:scenario-schema' first)", filename, candidates[0], candidates[1])
}

func extractSection(text, startMarker, endMarker string) string {
	startIdx := strings.Index(text, startMarker)
	if startIdx == -1 {
		return ""
	}

	rest := text[startIdx+len(startMarker):]
	endIdx := strings.Index(rest, endMarker)
	if endIdx == -1 {
		return startMarker + rest
	}

	return startMarker + rest[:endIdx]
}

// stripFieldsTable removes ### Fields sections to reduce verbosity when a caller
// wants section prose without duplicating schema tables.
func stripFieldsTable(text string) string {
	fieldsIdx := strings.Index(text, "### Fields")
	if fieldsIdx == -1 {
		return text
	}

	before := text[:fieldsIdx]
	after := text[fieldsIdx:]
	restIdx := strings.Index(after[len("### Fields"):], "###")
	if restIdx == -1 {
		return strings.TrimSpace(before)
	}

	return strings.TrimSpace(before + after[len("### Fields")+restIdx:])
}

func extractFieldsTable(section string) string {
	fieldsIdx := strings.Index(section, "### Fields")
	if fieldsIdx == -1 {
		return ""
	}

	lines := strings.Split(section[fieldsIdx:], "\n")
	var table []string
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "| Field |") {
			inTable = true
		}
		if !inTable {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			break
		}
		table = append(table, trimmed)
	}

	return strings.Join(table, "\n")
}

func extractSectionSummary(section string) string {
	section = stripFieldsTable(section)
	lines := strings.Split(section, "\n")
	var summary []string
	for _, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(summary) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(trimmed, "Example") || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "###") {
			break
		}
		summary = append(summary, trimmed)
	}
	return strings.Join(summary, "\n")
}

func demoteHeading(section string) string {
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") {
			lines[i] = "#" + line
		} else if strings.HasPrefix(line, "### ") {
			lines[i] = "#" + line
		}
	}
	return strings.Join(lines, "\n")
}

// cleanMarkdown removes frontmatter and HTML comments.
func cleanMarkdown(text string) string {
	commentRe := regexp.MustCompile(`(?s)<!--.*?-->`)
	text = commentRe.ReplaceAllString(text, "")

	text = strings.TrimSpace(text)

	if strings.HasPrefix(text, "---") {
		rest := text[3:]
		idx := strings.Index(rest, "---")
		if idx != -1 {
			text = strings.TrimSpace(rest[idx+3:])
		}
	}

	blankRe := regexp.MustCompile(`\n{3,}`)
	text = blankRe.ReplaceAllString(text, "\n\n")

	return strings.TrimSpace(text)
}

func summaryForName(name, summary, description string) string {
	result := strings.TrimSpace(summary)
	if result == "" || result == "-" {
		result = strings.TrimSpace(description)
	}
	result = firstParagraph(result)

	prefix := name + " "
	if strings.HasPrefix(result, prefix) {
		result = strings.TrimPrefix(result, prefix)
		if result != "" {
			result = strings.ToUpper(result[:1]) + result[1:]
		}
	}
	if result == "" {
		return "-"
	}
	return result
}

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "-"
	}

	paragraphs := regexp.MustCompile(`\n\s*\n`).Split(text, 2)
	text = paragraphs[0]
	return strings.Join(strings.Fields(text), " ")
}

func tableCell(text string) string {
	text = strings.TrimSpace(text)
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "|", `\|`)
	if text == "" {
		return "-"
	}
	return text
}

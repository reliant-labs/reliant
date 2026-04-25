# Reliant Examples

This directory contains example workflows, presets, and scenarios that demonstrate Reliant's capabilities.

## Directory Structure

```
examples/
├── workflows/     # Core workflow definitions (symlinks to builtin sources)
├── presets/       # Agent configuration presets (symlinks to builtin sources)
├── scenarios/     # Test scenarios for workflows (symlinks to builtin sources)
├── hooks/         # Example hook scripts
├── boolean-type-validation-demo.yaml
├── output-type-validation.yaml

```

## Workflows

| Workflow | Description |
|----------|-------------|
| `agent.yaml` | Standard interactive agent with loop-based tool execution |
| `auditing-agent.yaml` | Agent with per-turn audit oversight from a cheap reviewer model |
| `discovery-relay.yaml` | Iterative waves with progressive knowledge transfer |
| `env-setup.yaml` | Environment isolation pipeline: setup, validate, complete |
| `get-it-right.yaml` | For complex brownfield codebases — try, fail, and learn before implementing |
| `migrate.yaml` | Guided migration from Claude Code, Cursor, Codex, or Windsurf into Reliant |
| `one-ring.yaml` | Unified development pipeline: planning → tests → get-it-right loop → complete |
| `parallel-compete.yaml` | 3 agents implement in parallel worktrees, reviewer picks winner or synthesizes |
| `ralph-wiggum.yaml` | Brute-force iteration for complex tasks |
| `structured-agent.yaml` | Agent that requires structured output via a response tool |

## Presets

| Preset | Description |
|--------|-------------|
| `general.yaml` | Balanced general-purpose agent |
| `planner.yaml` | Strategic planner for research and implementation |
| `researcher.yaml` | Research specialist with read-only access |
| `architect.yaml` | Design-level review and refactoring recommendations |
| `code_reviewer.yaml` | Comprehensive code review |
| `security_reviewer.yaml` | Security-focused code review |
| `performance_reviewer.yaml` | Performance analysis |
| `code_hygiene_reviewer.yaml` | Code quality review |
| `tester.yaml` | Testing specialist |
| `debug.yaml` | Debugging orchestrator |
| `reproducer.yaml` | Bug reproduction specialist |
| `refactor.yaml` | Code refactoring |
| `simplifier.yaml` | Code simplification |
| `documentation.yaml` | Technical documentation |
| `git.yaml` | Git operations |
| `migrate.yaml` | Migration assistant |
| `ux.yaml` | User experience improvements |
| `ux_reviewer.yaml` | UX-focused code review |
| `workflow_builder.yaml` | Workflow creation and modification |

## Scenarios

Test scenarios for each workflow live in subdirectories under `scenarios/`. Each directory contains multiple `.yaml` files that define inputs, simulated events, and expected outcomes.

| Directory | Description |
|-----------|-------------|
| `agent/` | Agent workflow test cases (happy path, manual mode, compaction, multi-tool, etc.) |
| `auditing-agent/` | Auditing agent test cases (approval, rejection, guidance flows) |
| `context-reducing-agent/` | Context-reducing agent test cases (compaction, large result filtering) |
| `get-it-right/` | Get-it-right test cases (retries, max retries exhausted, restart) |
| `one-ring/` | One-ring pipeline test cases (full pipeline, individual steps, retries) |
| `parallel-compete/` | Parallel compete test cases (winner selection, synthesis, failure handling) |
| `structured-agent/` | Structured agent test cases (schema validation, reminders, custom tools) |

## Usage

### Using a Workflow

```yaml
nodes:
  - id: my_agent
    type: workflow
    ref: builtin://agent
    args:
      mode: auto
      max_turns: 50
```

### Applying Presets

```yaml
nodes:
  - id: research_step
    type: workflow
    ref: builtin://agent
    presets: researcher
    args:
      mode: auto
```
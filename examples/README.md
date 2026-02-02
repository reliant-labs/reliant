# Reliant Examples

This directory contains example workflows, presets, and scenarios that demonstrate Reliant's capabilities.

## Directory Structure

```
examples/
├── workflows/     # Core workflow definitions
├── presets/       # Agent configuration presets
└── scenarios/     # Test scenarios for workflows
```

## Workflows

Workflow definitions that demonstrate different agent patterns:

| Workflow | Description |
|----------|-------------|
| `agent.yaml` | Interactive agent workflow with loop-based tool execution. The foundation for most agent interactions. |
| `checklist.yaml` | Checklist-based workflow for structured task completion. |
| `auditing-agent.yaml` | Agent pattern for auditing and review tasks. |
| `parallel-compete.yaml` | Runs multiple agents in parallel and selects the best result. |
| `structured-agent.yaml` | Agent that produces structured output matching a defined schema. |
| `retry-with-escalation.yaml` | Retry pattern that escalates to more capable models on failure. |
| `context-reducing-agent.yaml` | Agent with automatic context compaction for long conversations. |

## Presets

Presets configure agent behavior for specialized tasks. Apply them via the `presets:` field in workflows.

### General Purpose

| Preset | Description |
|--------|-------------|
| `general.yaml` | Balanced general-purpose agent with full tool access |
| `planner.yaml` | Strategic planner for orchestrating research and implementation plans |
| `researcher.yaml` | Research specialist with read-only codebase access |

### Code Review

| Preset | Description |
|--------|-------------|
| `code_reviewer.yaml` | Comprehensive code review orchestrator |
| `architect.yaml` | Design-level review and high-leverage refactoring recommendations |
| `security_reviewer.yaml` | Security-focused code review |
| `performance_reviewer.yaml` | Performance analysis and optimization review |
| `code_hygiene_reviewer.yaml` | Code quality and maintainability review |

### Development

| Preset | Description |
|--------|-------------|
| `tester.yaml` | Testing specialist for comprehensive test creation |
| `debug.yaml` | Debugging orchestrator for isolating bugs |
| `reproducer.yaml` | Runtime reproduction specialist for capturing bug evidence |
| `refactor.yaml` | Code refactoring with behavior preservation |
| `documentation.yaml` | Technical documentation creation |

### Specialized

| Preset | Description |
|--------|-------------|
| `git.yaml` | Git operations, commits, and version control |
| `ux.yaml` | User experience and UI/UX improvements |
| `conflict-resolver.yaml` | Git merge conflict resolution |
| `workflow_builder.yaml` | Workflow creation and modification |

## Scenarios

Test scenarios demonstrate expected workflow behavior. Each scenario defines:
- **inputs**: Configuration values for the workflow
- **events**: Simulated LLM responses and tool executions
- **expect**: Expected outcomes, nodes reached, and assertions

### Agent Scenarios (`scenarios/agent/`)

| Scenario | Description |
|----------|-------------|
| `happy_path.yaml` | LLM responds without tool calls, loop exits immediately |
| `tool_use.yaml` | LLM calls tools, they execute, conversation continues |
| `manual_mode.yaml` | Tool execution requires user approval |
| `plan_mode.yaml` | Read-only mode with planning tools only |
| `max_turns.yaml` | Loop terminates when max iterations reached |

### Structured Agent Scenarios (`scenarios/structured-agent/`)

| Scenario | Description |
|----------|-------------|
| `happy_path.yaml` | Agent produces valid structured output |
| `custom_schema.yaml` | Custom output schema validation |
| `reminder_flow.yaml` | Reminder mechanism for incomplete responses |
| `tool_use_then_response.yaml` | Tool usage followed by structured response |
| `schema_validation_failure.yaml` | Handling of invalid output |

### Context-Reducing Agent Scenarios (`scenarios/context-reducing-agent/`)

| Scenario | Description |
|----------|-------------|
| `happy_path.yaml` | Normal operation without compaction |
| `compaction_triggered.yaml` | Context compaction activates at threshold |
| `large_results_filtered.yaml` | Large tool results are filtered |

## Usage

### Using a Workflow

Reference builtin workflows in your workflow definitions:

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

Apply presets to configure agent behavior:

```yaml
nodes:
  - id: research_step
    type: workflow
    ref: builtin://agent
    presets: researcher  # Applies researcher preset
    args:
      mode: auto
```

### Running Scenarios

Scenarios are used for testing workflow behavior. They simulate LLM responses to verify workflow logic without making actual API calls.

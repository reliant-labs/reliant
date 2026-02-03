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

| Workflow | Description |
|----------|-------------|
| `agent.yaml` | Interactive agent workflow with loop-based tool execution |
| `checklist.yaml` | Checklist-based workflow with planning, implementation, and review phases |
| `auditing-agent.yaml` | Agent with an auditor that reviews responses before sending |
| `parallel-compete.yaml` | Runs multiple agents in parallel and selects the best result |
| `structured-agent.yaml` | Agent that produces structured output matching a schema |
| `retry-with-escalation.yaml` | Retry pattern that escalates to more capable models on failure |
| `context-reducing-agent.yaml` | Agent with automatic context compaction |

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
| `documentation.yaml` | Technical documentation |
| `git.yaml` | Git operations |
| `ux.yaml` | User experience improvements |
| `conflict-resolver.yaml` | Git merge conflict resolution |
| `workflow_builder.yaml` | Workflow creation and modification |

## Scenarios

Test scenarios for each workflow. Each file contains multiple scenarios (separated by `---`) that define inputs, simulated events, and expected outcomes.

| File | Description |
|------|-------------|
| `agent_scenarios.yaml` | Agent workflow test cases |
| `auditing-agent_scenarios.yaml` | Auditing agent test cases |
| `checklist_scenarios.yaml` | Checklist workflow test cases |
| `context-reducing-agent_scenarios.yaml` | Context-reducing agent test cases |
| `parallel-compete_scenarios.yaml` | Parallel compete test cases |
| `retry-with-escalation_scenarios.yaml` | Retry escalation test cases |
| `structured-agent_scenarios.yaml` | Structured agent test cases |
| `presets_validation.yaml` | Preset validation test cases |

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

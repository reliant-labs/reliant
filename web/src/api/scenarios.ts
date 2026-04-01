// Copyright (c) 2025 Reliant Labs
// Scenario API - Test workflows with mocked events

import { getScenarioClient } from './grpc-client'
import { 
  type Scenario,
  type ScenarioDefinition,
  type ScenarioResult,
  type SimulatedEvent,
  type ScenarioExpectation,
} from '../gen/reliant/v1/workflow_pb'

// Re-export types for convenience
export type { 
  Scenario, 
  ScenarioDefinition, 
  ScenarioResult, 
  SimulatedEvent, 
  ScenarioExpectation,
}

/**
 * List all scenarios for a workflow
 */
export interface ListScenariosResponse {
  scenarios: Scenario[]
  scenariosDir: string  // Directory path for file-based scenarios
}

export async function listScenarios(
  projectId: string, 
  workflowSlug: string
): Promise<ListScenariosResponse> {
  const client = getScenarioClient()
  const response = await client.listScenarios({
    projectId,
    workflowSlug,
  })
  return {
    scenarios: response.scenarios,
    scenariosDir: response.scenariosDir,
  }
}

/**
 * Create a scenario and optionally run it
 */
export interface CreateScenarioOptions {
  projectId: string
  workflowSlug?: string      // Workflow to test (optional if workflowYaml provided)
  workflowYaml?: string      // Raw YAML to test (optional)
  scenario: ScenarioDefinition
  run?: boolean              // Run immediately after creating (default: true)
  save?: boolean             // Save the scenario for future runs (default: false)
}

export interface CreateScenarioResponse {
  success: boolean
  message: string
  scenario?: Scenario        // Created scenario (if save=true)
  result?: ScenarioResult    // Run result (if run=true)
}

export async function createScenario(options: CreateScenarioOptions): Promise<CreateScenarioResponse> {
  const client = getScenarioClient()
  const response = await client.createScenario({
    projectId: options.projectId,
    workflowSlug: options.workflowSlug ?? '',
    workflowYaml: options.workflowYaml ?? '',
    scenario: options.scenario,
    run: options.run ?? true,
    save: options.save ?? false,
  })
  return {
    success: response.success,
    message: response.message,
    scenario: response.scenario,
    result: response.result,
  }
}

/**
 * Run a scenario against a workflow
 */
export interface RunScenarioOptions {
  projectId: string
  scenarioId?: string         // ID of saved scenario to run
  // OR run ad-hoc:
  workflowSlug?: string       // Workflow to test
  scenario?: ScenarioDefinition  // Ad-hoc scenario to run
}

export async function runScenario(options: RunScenarioOptions): Promise<ScenarioResult> {
  const client = getScenarioClient()
  const response = await client.runScenario({
    projectId: options.projectId,
    scenarioId: options.scenarioId ?? '',
    workflowSlug: options.workflowSlug ?? '',
    scenario: options.scenario,
  })
  return response.result!
}

/**
 * Delete a scenario
 */
export async function deleteScenario(projectId: string, scenarioId: string): Promise<void> {
  const client = getScenarioClient()
  await client.deleteScenario({
    projectId,
    scenarioId,
  })
}

// ============================================================================
// Helper functions for creating scenario definitions
//
// The simplified event model: each event has:
// - node: (optional) target a specific node by ID
// - output: mock output matching the activity's output structure
// ============================================================================

/**
 * Create a simulated event with mock output for a node.
 * The output should match the activity's output structure directly.
 */
export function mockOutput(options: {
  node?: string
  output: Record<string, unknown>
}): SimulatedEvent {
  return {
    $typeName: 'reliant.v1.SimulatedEvent',
    node: options.node ?? '',
    outputJson: JSON.stringify(options.output),
  }
}

/**
 * Create a call_llm node output event.
 * @see CallLLMOutput in the simulator for the full output structure
 */
export function callLLMOutput(options: {
  node?: string
  responseText: string
  toolCalls?: Array<{ id: string; name: string; input: Record<string, unknown> }>
  inputTokens?: number
  outputTokens?: number
}): SimulatedEvent {
  return mockOutput({
    node: options.node,
    output: {
      response_text: options.responseText,
      tool_calls: options.toolCalls ?? [],
      input_tokens: options.inputTokens ?? 0,
      output_tokens: options.outputTokens ?? 0,
    },
  })
}

/**
 * Create an execute_tools node output event.
 * @see ExecuteToolsOutput in the simulator for the full output structure
 */
export function executeToolsOutput(options: {
  node?: string
  toolResults: Array<{ toolCallId: string; toolName: string; output: string; isError?: boolean }>
}): SimulatedEvent {
  return mockOutput({
    node: options.node,
    output: {
      tool_results: options.toolResults.map(r => ({
        tool_call_id: r.toolCallId,
        tool_name: r.toolName,
        output: r.output,
        is_error: r.isError ?? false,
      })),
    },
  })
}

/**
 * Create a run node (bash/script) output event.
 * @see RunOutput in the simulator for the full output structure
 */
export function runOutput(options: {
  node?: string
  exitCode: number
  stdout?: string
  stderr?: string
}): SimulatedEvent {
  return mockOutput({
    node: options.node,
    output: {
      exit_code: options.exitCode,
      stdout: options.stdout ?? '',
      stderr: options.stderr ?? '',
    },
  })
}

/**
 * Create an approval node output event.
 */
export function approvalOutput(options: {
  node?: string
  status: 'approved' | 'denied' | 'timeout'
  actionTaken?: string
}): SimulatedEvent {
  return mockOutput({
    node: options.node,
    output: {
      status: options.status,
      action_taken: options.actionTaken ?? '',
    },
  })
}

/**
 * Create a scenario definition
 */
export function scenario(options: {
  name: string
  description?: string
  events: SimulatedEvent[]
  expect?: {
    outcome?: 'completed' | 'error'
    reached?: string[]
    notReached?: string[]
    errorContains?: string
    errorNode?: string
  }
  inputs?: Record<string, unknown>
}): ScenarioDefinition {
  return {
    $typeName: 'reliant.v1.ScenarioDefinition',
    name: options.name,
    description: options.description ?? '',
    events: options.events,
    expect: options.expect ? {
      $typeName: 'reliant.v1.ScenarioExpectation',
      outcome: options.expect.outcome ?? '',
      reached: options.expect.reached ?? [],
      notReached: options.expect.notReached ?? [],
      errorContains: options.expect.errorContains ?? '',
      errorNode: options.expect.errorNode ?? '',
    } : undefined,
    inputsJson: options.inputs ? JSON.stringify(options.inputs) : '',
  }
}

/**
 * Upload a scenario YAML file to the project directory
 */
export interface UploadScenarioOptions {
  projectId: string
  workflowSlug: string
  filename: string       // Filename without extension (e.g., "happy-path")
  yamlContent: string    // YAML content of the scenario
}

export interface UploadScenarioResponse {
  success: boolean
  message: string
  path: string           // Full path to the created file
  scenario?: Scenario    // The created scenario
}

export async function uploadScenario(options: UploadScenarioOptions): Promise<UploadScenarioResponse> {
  const client = getScenarioClient()
  const response = await client.uploadScenario({
    projectId: options.projectId,
    workflowSlug: options.workflowSlug,
    filename: options.filename,
    yamlContent: options.yamlContent,
  })
  return {
    success: response.success,
    message: response.message,
    path: response.path,
    scenario: response.scenario,
  }
}

/**
 * Export a scenario to YAML format
 */
export interface ExportScenarioOptions {
  projectId: string
  scenarioId: string
}

export interface ExportScenarioResponse {
  yamlContent: string    // YAML content of the scenario
  filename: string       // Suggested filename
}

export async function exportScenario(options: ExportScenarioOptions): Promise<ExportScenarioResponse> {
  const client = getScenarioClient()
  const response = await client.exportScenario({
    projectId: options.projectId,
    scenarioId: options.scenarioId,
  })
  return {
    yamlContent: response.yamlContent,
    filename: response.filename,
  }
}

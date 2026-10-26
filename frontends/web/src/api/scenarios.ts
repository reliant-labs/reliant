// Copyright (c) 2025 Reliant Labs
// Scenario API - Test workflows with mocked events

import { connectClient } from '../lib/connect'
import {
  ScenarioService,
  type Scenario,
  type ScenarioDefinition,
  type ScenarioResult,
} from '../gen/reliant/v1/scenario_pb'

// Re-export types for convenience
export type { Scenario, ScenarioResult }

const client = () => connectClient(ScenarioService)

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
  const response = await client().listScenarios({
    projectId,
    workflowSlug,
  })
  return {
    scenarios: response.scenarios,
    scenariosDir: response.scenariosDir,
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
  const response = await client().runScenario({
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
  await client().deleteScenario({
    projectId,
    scenarioId,
  })
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
  const response = await client().uploadScenario({
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
  const response = await client().exportScenario({
    projectId: options.projectId,
    scenarioId: options.scenarioId,
  })
  return {
    yamlContent: response.yamlContent,
    filename: response.filename,
  }
}

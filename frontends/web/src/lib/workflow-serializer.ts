import * as yaml from 'js-yaml'
import type { Workflow, Step, Param, Edge, EdgeCase, WorkflowUI, SwitchMetadata, SwitchCase } from '../types/workflow'
import {
  getInputDescription,
  getInputUI,
  getInputDefault,
  getInputEnumValues,
  getInputMulti,
  getInputMin,
  getInputMax,
  getInputPattern,
  getInputMinLength,
  getInputMaxLength,
  getInputMinItems,
  getInputMaxItems,
  getInputTags,
  getInputPresetConfig,
  getInputNestedInputs,
  createInput,
} from './inputHelpers'
import {
  getStepCommand,
  getStepRef,
  getStepInline,
  getStepWhile,
  getStepParallel,
  getStepItems,
  getStepKey,
  getStepOnFailure,
  getStepInputs,
  getStepPresets,
  getStepProject,
  getStepThread,
  withWorkflowArgs,
  withLoopArgs,
} from '../types/workflow'
import { directCel, celString, normalizeCelString } from './celAdapter'

/**
 * Workflow serializer for YAML import/export
 * 
 * Note on field naming:
 * - Proto/internal uses `params` for workflow parameters
 * - YAML format uses `inputs` for workflow parameters (user-facing)
 * - This serializer bridges the two formats
 */

// Recursively clean a step for serialization (removes empty objects, handles inline workflows)
function cleanStepForYaml(step: Step): Record<string, unknown> {
  const result: Record<string, unknown> = {
    id: step.id,
    type: step.type,
  }

  // Add type-specific fields from args oneof via accessors
  const command = getStepCommand(step)
  if (command) result.command = command
  const ref = getStepRef(step)
  if (ref) result.ref = ref
  const whileExpr = getStepWhile(step)
  if (whileExpr) result.while = whileExpr
  const parallelValue = getStepParallel(step)
  if (parallelValue !== undefined && parallelValue !== false) result.parallel = parallelValue
  const itemsExpr = getStepItems(step)
  if (itemsExpr) result.items = itemsExpr
  const keyExpr = getStepKey(step)
  if (keyExpr) result.key = keyExpr
  const onFailure = getStepOnFailure(step)
  if (onFailure) result.on_failure = onFailure
  if (step.args?.case === 'workflow' && step.args.value.propagateOutcome) result.propagate_outcome = true
  const condStr = normalizeCelString(step.condition)
  if (condStr) result.condition = condStr
  const timeoutStr = normalizeCelString(step.timeout)
  if (timeoutStr) result.timeout = timeoutStr

  // Add inputs: for workflow/loop steps use getStepInputs; for action steps use step.inputs UI bag
  const isStructuralWithInputs = step.type === 'workflow' || step.type === 'loop'
  if (isStructuralWithInputs) {
    const structInputs = getStepInputs(step)
    if (Object.keys(structInputs).length > 0) {
      const inputs: Record<string, unknown> = {}
      for (const [key, value] of Object.entries(structInputs)) {
        inputs[key] = protoValueToPlain(value)
      }
      result.inputs = inputs
    }
  } else if (step.args?.value) {
    // Action steps: inputs are in the typed args value record
    const argsValue = step.args.value as Record<string, unknown>
    if (Object.keys(argsValue).length > 0) {
      const inputs: Record<string, unknown> = {}
      for (const [key, value] of Object.entries(argsValue)) {
        inputs[key] = protoValueToPlain(value)
      }
      result.inputs = inputs
    }
  }

  // Handle thread config on workflow/loop nodes
  const stepThread = getStepThread(step)
  if (stepThread) {
    const thread: Record<string, unknown> = {}
    if (stepThread.mode) thread.mode = stepThread.mode
    if (stepThread.memo !== undefined) thread.memo = stepThread.memo
    if (stepThread.inject) {
      const injectRole = normalizeCelString(stepThread.inject.role)
      const injectContent = normalizeCelString(stepThread.inject.content)
      const injectAttachments = normalizeCelString(stepThread.inject.attachments)
      const injectDisplayStyle = normalizeCelString(stepThread.inject.displayStyle)
      thread.inject = {
        ...(injectRole && { role: injectRole }),
        ...(injectContent && { content: injectContent }),
        ...(injectAttachments && { attachments: injectAttachments }),
        ...(injectDisplayStyle && { display_style: injectDisplayStyle }),
      }
    }
    if (Object.keys(thread).length > 0) result.thread = thread
  }

  // Handle saveMessage config
  if (step.saveMessage) {
    const saveMessage: Record<string, unknown> = {}
    if (step.saveMessage.role !== undefined) {
      saveMessage.role = normalizeCelString(step.saveMessage.role)
    }
    if (step.saveMessage.content !== undefined) {
      saveMessage.content = normalizeCelString(step.saveMessage.content)
    }
    if (step.saveMessage.toolCalls !== undefined) {
      saveMessage.tool_calls = normalizeCelString(step.saveMessage.toolCalls)
    }
    if (step.saveMessage.toolResults !== undefined) {
      saveMessage.tool_results = normalizeCelString(step.saveMessage.toolResults)
    }
    if (step.saveMessage.condition !== undefined) {
      saveMessage.condition = normalizeCelString(step.saveMessage.condition)
    }
    if (step.saveMessage.attachments !== undefined) {
      saveMessage.attachments = normalizeCelString(step.saveMessage.attachments)
    }
    if (Object.keys(saveMessage).length > 0) result.save_message = saveMessage
  }

  // The node base keys every kind may carry, in the order the server writes
  // them (internal/workflow/yaml/nodes.go, marshalNode).
  const daemon = daemonSelectorToYaml(step.daemon)
  if (daemon !== undefined) result.daemon = daemon
  if (step.outcome) result.outcome = step.outcome
  if (step.tolerateFailure) result.tolerate_failure = true
  const observe = observeToYaml(step.observe)
  if (observe) result.observe = observe
  const preflight = preflightToYaml(step.preflight)
  if (preflight !== undefined) result.preflight = preflight

  // Handle project config via accessor
  const project = getStepProject(step)
  if (project?.path) {
    const projPath = normalizeCelString(project.path)
    if (projPath) result.project = { path: projPath }
  }

  // Handle presets map via accessor
  const presets = getStepPresets(step)
  if (Object.keys(presets).length > 0) {
    result.presets = presets
  }

  // Handle inline workflow recursively via accessor
  const inline = getStepInline(step)
  if (inline) {
    result.inline = cleanWorkflowForYaml(inline)
  }

  return result
}

// A node's `daemon:` as the server writes it (internal/workflow/yaml/cel.go,
// marshalCelDaemonSelector): an expression or a lone type or name as a
// string, anything else as a mapping. A pool never takes the string form: a
// bare string is a machine name.
function daemonSelectorToYaml(daemon: Step['daemon']): unknown {
  const value = daemon?.value
  if (!value || value.case === undefined) return undefined
  if (value.case === 'expr') return value.value
  const ds = value.value
  if (!ds) return undefined
  const labels = ds.labels && Object.keys(ds.labels).length > 0 ? ds.labels : undefined
  if (!ds.id && !ds.name && !labels && !ds.pool && ds.type) return ds.type
  if (!ds.id && !ds.type && !labels && !ds.pool && ds.name) return ds.name
  const out: Record<string, unknown> = {}
  if (ds.id) out.id = ds.id
  if (ds.name) out.name = ds.name
  if (ds.type) out.type = ds.type
  if (labels) out.labels = labels
  if (ds.pool) out.pool = ds.pool
  return Object.keys(out).length > 0 ? out : undefined
}

type ObserveBlock = Step['observe']

// An `observe:` block (a node's, or the workflow's defaults) as YAML, its
// optional keys left out when empty. undefined when it declares nothing.
type PreflightBlock = Step['preflight']

// A node's `preflight:` as the server writes it (internal/workflow/yaml/
// preflight.go): `true` when it overrides nothing, else its mapping.
function preflightToYaml(preflight: PreflightBlock): true | Record<string, unknown> | undefined {
  if (!preflight) return undefined
  const out: Record<string, unknown> = {}
  if (preflight.tools && preflight.tools.length > 0) out.tools = [...preflight.tools]
  if (preflight.minFreeDisk) out.min_free_disk = preflight.minFreeDisk
  if (preflight.maxLoad) out.max_load = preflight.maxLoad
  if (preflight.allowRoot) out.allow_root = true
  if (preflight.maxMachines) out.max_machines = preflight.maxMachines
  return Object.keys(out).length > 0 ? out : true
}

function parseRawPreflight(raw: unknown): PreflightBlock {
  if (raw === true) return {}
  if (!isRecord(raw)) return undefined
  const preflight: Exclude<PreflightBlock, undefined> = {}
  if (Array.isArray(raw.tools)) preflight.tools = raw.tools.filter((t): t is string => typeof t === 'string')
  const minFreeDisk = parseOptionalString(raw.min_free_disk)
  if (minFreeDisk) preflight.minFreeDisk = minFreeDisk
  if (typeof raw.max_load === 'number') preflight.maxLoad = raw.max_load
  if (raw.allow_root === true) preflight.allowRoot = true
  if (typeof raw.max_machines === 'number') preflight.maxMachines = raw.max_machines
  return preflight
}

function observeToYaml(observe: ObserveBlock): Record<string, unknown> | undefined {
  if (!observe) return undefined
  const out: Record<string, unknown> = {}
  if (observe.attributes && Object.keys(observe.attributes).length > 0) {
    out.attributes = { ...observe.attributes }
  }
  if (observe.events && observe.events.length > 0) {
    out.events = observe.events.map((event) => {
      const e: Record<string, unknown> = { name: event.name ?? '' }
      if (event.when) e.when = event.when
      if (event.each) e.each = event.each
      if (event.attributes && Object.keys(event.attributes).length > 0) e.attributes = { ...event.attributes }
      return e
    })
  }
  if (observe.metrics && observe.metrics.length > 0) {
    out.metrics = observe.metrics.map((metric) => {
      const m: Record<string, unknown> = { name: metric.name ?? '', kind: metric.kind ?? '' }
      if (metric.value) m.value = metric.value
      if (metric.unit) m.unit = metric.unit
      if (metric.when) m.when = metric.when
      if (metric.attributes && Object.keys(metric.attributes).length > 0) m.attributes = { ...metric.attributes }
      return m
    })
  }
  return Object.keys(out).length > 0 ? out : undefined
}

// Convert proto Value to plain JS for YAML serialization
function protoValueToPlain(value: unknown): unknown {
  if (!value || typeof value !== 'object') return value
  
  const v = value as { kind?: { case?: string; value?: unknown } }
  if (!v.kind || !v.kind.case) return value

  switch (v.kind.case) {
    case 'nullValue':
      return null
    case 'numberValue':
    case 'stringValue':
    case 'boolValue':
      return v.kind.value
    case 'listValue': {
      const list = v.kind.value as { values?: unknown[] }
      return (list.values || []).map(protoValueToPlain)
    }
    case 'structValue': {
      const struct = v.kind.value as { fields?: Record<string, unknown> }
      const result: Record<string, unknown> = {}
      for (const [k, val] of Object.entries(struct.fields || {})) {
        result[k] = protoValueToPlain(val)
      }
      return result
    }
    default:
      return value
  }
}

// Clean workflow for YAML serialization
function cleanWorkflowForYaml(workflow: Workflow): Record<string, unknown> {
  const result: Record<string, unknown> = {
    name: workflow.name,
  }

  // Add optional fields
  if (workflow.apiVersion) result.apiVersion = workflow.apiVersion
  if (workflow.description) result.description = workflow.description

  // Add presets config
  if (workflow.presets?.tag) {
    result.presets = {
      tag: workflow.presets.tag,
      ...(workflow.presets.default && { default: workflow.presets.default }),
    }
  }

  // Add workflow inputs as "inputs" in YAML
  const workflowInputs = workflow.inputs
  if (workflowInputs && Object.keys(workflowInputs).length > 0) {
    result.inputs = cleanParamsForYaml(workflowInputs)
  }

  // Add nodes
  result.nodes = (workflow.nodes || []).map(cleanStepForYaml)

  // Add entry point(s) - always as array
  if (workflow.entry && workflow.entry.length > 0) {
    result.entry = workflow.entry
  }

  // Add edges
  if (workflow.edges && workflow.edges.length > 0) {
    result.edges = workflow.edges.map(edge => {
      const cleanEdge: Record<string, unknown> = { from: edge.from }
      if (edge.cases && edge.cases.length > 0) {
        cleanEdge.cases = edge.cases.map(c => {
          const targets = Array.isArray(c.to) ? c.to : (c.to ? [c.to] : []);
          return {
            to: targets.length === 1 ? targets[0] : targets,
            condition: c.condition,
            ...(c.label && { label: c.label }),
          };
        })
      }
      if (edge.default) {
        const targets = Array.isArray(edge.default) ? edge.default : [edge.default];
        cleanEdge.default = targets.length === 1 ? targets[0] : targets;
      }
      return cleanEdge
    })
  }

  // Add outputs
  if (workflow.outputs && Object.keys(workflow.outputs).length > 0) {
    result.outputs = workflow.outputs
  }

  // The workflow's observe defaults, merged into every node by the runtime
  const observe = observeToYaml(workflow.observe)
  if (observe) result.observe = observe

  // Add UI metadata (positions, switches, locked)
  const hasPositions = workflow.ui?.positions && Object.keys(workflow.ui.positions).length > 0
  const hasSwitches = workflow.ui?.switches && Object.keys(workflow.ui.switches).length > 0
  const isLocked = workflow.ui?.locked === true

  if (hasPositions || hasSwitches || isLocked) {
    const ui: Record<string, unknown> = {}

    if (hasPositions) {
      const positions: Record<string, { x: number; y: number }> = {}
      for (const [id, pos] of Object.entries(workflow.ui!.positions!)) {
        positions[id] = { x: pos.x ?? 0, y: pos.y ?? 0 }
      }
      ui.positions = positions
    }

    if (hasSwitches) {
      const switches: Record<string, unknown> = {}
      for (const [switchId, switchMeta] of Object.entries(workflow.ui!.switches!)) {
        switches[switchId] = {
          source_node: switchMeta.sourceNode,
          position: {
            x: switchMeta.position?.x ?? 0,
            y: switchMeta.position?.y ?? 0,
          },
          cases: (switchMeta.cases || []).map((c) => ({
            id: c.id,
            condition: normalizeCelString(c.condition) || '',
            ...(c.label && { label: c.label }),
          })),
        }
      }
      ui.switches = switches
    }

    if (isLocked) {
      ui.locked = true
    }

    result.ui = ui
  }

  return result
}

// Clean params for YAML serialization
function cleanParamsForYaml(params: { [key: string]: Param }): Record<string, unknown> {
  const result: Record<string, unknown> = {}
  for (const [key, param] of Object.entries(params)) {
    const cleanParam: Record<string, unknown> = {
      type: param.type,
    }

    // Read through helpers to handle both proto Input and legacy flat shapes
    const defaultVal = getInputDefault(param)
    const description = getInputDescription(param)
    const ui = getInputUI(param)
    const enumValues = getInputEnumValues(param)
    const multi = getInputMulti(param)
    const min = getInputMin(param)
    const max = getInputMax(param)
    const pattern = getInputPattern(param)
    const minLength = getInputMinLength(param)
    const maxLength = getInputMaxLength(param)
    const minItems = getInputMinItems(param)
    const maxItems = getInputMaxItems(param)
    const tags = getInputTags(param)
    const presetConfig = getInputPresetConfig(param)
    const nestedInputs = getInputNestedInputs(param)

    if (defaultVal !== undefined) cleanParam.default = protoValueToPlain(defaultVal)
    if (description) cleanParam.description = description
    if (ui) cleanParam.ui = ui
    if (enumValues && enumValues.length > 0) cleanParam.enum_values = enumValues
    if (multi) cleanParam.multi = multi
    if (min !== undefined) cleanParam.min = min
    if (max !== undefined) cleanParam.max = max
    if (pattern) cleanParam.pattern = pattern
    if (minLength !== undefined) cleanParam.min_length = minLength
    if (maxLength !== undefined) cleanParam.max_length = maxLength
    if (minItems !== undefined) cleanParam.min_items = minItems
    if (maxItems !== undefined) cleanParam.max_items = maxItems
    if (tags && tags.length > 0) cleanParam.tags = tags

    // Handle preset config for groups
    if (presetConfig?.tag) {
      cleanParam.preset_config = {
        tag: presetConfig.tag,
        ...(presetConfig.default && { default: presetConfig.default }),
      }
    }

    // Handle nested inputs for groups
    if (nestedInputs && Object.keys(nestedInputs).length > 0) {
      cleanParam.inputs = cleanParamsForYaml(nestedInputs as Record<string, Param>)
    }

    result[key] = cleanParam
  }
  return result
}

export function serializeWorkflowToYAML(workflow: Workflow): string {
  const cleanWorkflow = cleanWorkflowForYaml(workflow)

  return yaml.dump(cleanWorkflow, {
    indent: 2,
    lineWidth: 120,
    noRefs: true,
    sortKeys: false,
    flowLevel: -1,
  })
}

// Raw workflow shape from YAML (uses inputs, not params)
interface RawWorkflow {
  name?: string
  description?: string
  apiVersion?: string
  presets?: { tag?: string; default?: string }
  inputs?: Record<string, unknown>
  params?: Record<string, unknown> // Legacy field name
  nodes?: Array<Record<string, unknown>>
  edges?: Array<Record<string, unknown>>
  outputs?: Record<string, string>
  ui?: { positions?: Record<string, { x: number; y: number }>; switches?: unknown; locked?: boolean }
  entry?: string | string[]
  observe?: unknown
}

export function deserializeWorkflowFromYAML(yamlString: string): Workflow {
  let raw: RawWorkflow

  try {
    raw = yaml.load(yamlString) as RawWorkflow
  } catch (err) {
    throw new Error(`Invalid YAML syntax: ${err instanceof Error ? err.message : 'Unknown error'}`)
  }

  if (!raw || typeof raw !== 'object') {
    throw new Error('YAML file does not contain a valid workflow definition')
  }

  if (!raw.name) {
    throw new Error('Workflow is missing required field: name')
  }

  if (!raw.nodes) {
    throw new Error('Workflow is missing required field: nodes')
  }

  if (!Array.isArray(raw.nodes)) {
    throw new Error('Workflow nodes must be an array')
  }

  raw.nodes.forEach((node, index) => {
    if (!node.id) {
      throw new Error(`Node at index ${index} is missing required field: id`)
    }
  })

  const parsedParams = parseRawParams(raw.inputs ?? {})

  const workflow: Workflow = {
    name: raw.name,
    description: raw.description ?? '',
    apiVersion: raw.apiVersion ?? '',
    nodes: raw.nodes.map(parseRawStep),
    edges: (raw.edges ?? []).map(parseRawEdge),
    inputs: parsedParams,
    outputs: raw.outputs ?? {},
    entry: raw.entry ? (Array.isArray(raw.entry) ? raw.entry : [raw.entry]) : [],
    presets: raw.presets?.tag
      ? {
          tag: raw.presets.tag,
          default: raw.presets.default ?? '',
        }
      : undefined,
    ui: raw.ui ? parseRawUI(raw.ui) : undefined,
  }
  const observe = parseRawObserve(raw.observe)
  if (observe) workflow.observe = observe

  return workflow
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object'
}

function parseOptionalString(value: unknown): string | undefined {
  return typeof value === 'string' ? value : undefined
}

function parseOptionalBoolean(value: unknown): boolean | undefined {
  return typeof value === 'boolean' ? value : undefined
}

function parseOptionalNumber(value: unknown): number | undefined {
  return typeof value === 'number' ? value : undefined
}

function parseOptionalStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) {
    return undefined
  }

  const result = value.filter((item): item is string => typeof item === 'string')
  return result.length > 0 ? result : undefined
}

function parseStringOrStringArray(value: unknown): string[] | undefined {
  if (typeof value === 'string') {
    return [value]
  }

  const parsedArray = parseOptionalStringArray(value)
  if (parsedArray) {
    return parsedArray
  }

  return undefined
}

function isStringEntry(entry: [string, unknown]): entry is [string, string] {
  return typeof entry[1] === 'string'
}

function parseRawStep(raw: Record<string, unknown>): Step {
  const type = parseOptionalString(raw.type) ?? ''
  let step: Step = {
    id: (parseOptionalString(raw.id) ?? ''),
    type,
  }

  const condition = parseOptionalString(raw.condition)
  if (condition !== undefined) step.condition = directCel(condition)

  const timeout = parseOptionalString(raw.timeout)
  if (timeout !== undefined) step.timeout = celString(timeout)

  // Build proto args oneof based on step type
  if (type === 'run') {
    const command = parseOptionalString(raw.command)
    step.args = { case: 'run' as const, value: { command: command ? celString(command) : undefined, env: {} } } as Step['args']
  } else if (type === 'workflow') {
    const ref = parseOptionalString(raw.ref)
    const workflowArgs: Record<string, unknown> = { args: {}, presets: {} }
    if (ref) workflowArgs.ref = celString(ref)
    if (isRecord(raw.inputs)) workflowArgs.args = raw.inputs
    if (raw.propagate_outcome === true) workflowArgs.propagateOutcome = true
    if (isRecord(raw.presets)) {
      const presetsEntries = Object.entries(raw.presets).filter(isStringEntry)
      if (presetsEntries.length > 0) workflowArgs.presets = Object.fromEntries(presetsEntries)
    }
    if (isRecord(raw.project)) {
      const path = parseOptionalString(raw.project.path)
      if (path) workflowArgs.project = { path: celString(path) }
    }
    if (isRecord(raw.inline)) {
      const inlineRaw: Record<string, unknown> = { ...raw.inline }
      if (!inlineRaw.name) inlineRaw.name = `${step.id}-inline`
      workflowArgs.inline = deserializeWorkflowFromYAML(yaml.dump(inlineRaw))
    }
    step.args = { case: 'workflow' as const, value: workflowArgs } as Step['args']
  } else if (type === 'loop') {
    const ref = parseOptionalString(raw.ref)
    const loopArgs: Record<string, unknown> = { args: {}, presets: {} }
    if (ref) loopArgs.ref = celString(ref)
    if (isRecord(raw.inputs)) loopArgs.args = raw.inputs
    if (isRecord(raw.presets)) {
      const presetsEntries = Object.entries(raw.presets).filter(isStringEntry)
      if (presetsEntries.length > 0) loopArgs.presets = Object.fromEntries(presetsEntries)
    }
    if (isRecord(raw.project)) {
      const path = parseOptionalString(raw.project.path)
      if (path) loopArgs.project = { path: celString(path) }
    }
    if (isRecord(raw.inline)) {
      const inlineRaw: Record<string, unknown> = { ...raw.inline }
      if (!inlineRaw.name) inlineRaw.name = `${step.id}-inline`
      loopArgs.inline = deserializeWorkflowFromYAML(yaml.dump(inlineRaw))
    }
    const whileExpr = parseOptionalString(raw.while)
    if (whileExpr) loopArgs.while = directCel(whileExpr)
    const parallel = raw.parallel
    if (typeof parallel === 'boolean') {
      loopArgs.parallel = parallel
    } else if (typeof parallel === 'string') {
      loopArgs.parallel = celString(parallel)
    }
    const items = parseOptionalString(raw.items)
    if (items) loopArgs.items = celString(items)
    const key = parseOptionalString(raw.key)
    if (key) loopArgs.key = key
    const onFailure = parseOptionalString(raw.on_failure)
    if (onFailure) loopArgs.onFailure = onFailure
    step.args = { case: 'loop' as const, value: loopArgs } as Step['args']
  } else {
    // Action steps: inputs go in the typed args value
    if (isRecord(raw.inputs)) {
      const actionCase = step.args?.case
      if (actionCase) {
        step.args = { case: actionCase as any, value: { ...(step.args?.value as Record<string, unknown> ?? {}), ...raw.inputs } } as Step['args']
      }
    }
  }

  // Thread config — supported on workflow nodes and parallel loops
  if (isRecord(raw.thread) && (step.type === 'workflow' || step.type === 'loop')) {
    const thread: Record<string, unknown> = {}

    const mode = parseOptionalString(raw.thread.mode)
    if (mode !== undefined) thread.mode = mode

    const memo = parseOptionalBoolean(raw.thread.memo)
    if (memo !== undefined) thread.memo = memo

    if (isRecord(raw.thread.inject)) {
      const inject: Record<string, unknown> = {}
      const role = parseOptionalString(raw.thread.inject.role)
      if (role !== undefined) inject.role = celString(role)
      const content = parseOptionalString(raw.thread.inject.content)
      if (content !== undefined) inject.content = celString(content)
      const attachments = parseOptionalString(raw.thread.inject.attachments)
      if (attachments !== undefined) inject.attachments = celString(attachments)
      const displayStyle = parseOptionalString(raw.thread.inject.display_style)
      if (displayStyle !== undefined) inject.displayStyle = celString(displayStyle)

      if (Object.keys(inject).length > 0) {
        thread.inject = inject
      }
    }

    if (Object.keys(thread).length > 0) {
      step = step.type === 'loop'
        ? withLoopArgs(step, { thread: thread as any })
        : withWorkflowArgs(step, { thread: thread as any })
    }
  }

  if (isRecord(raw.save_message)) {
    const saveMessage: Exclude<Step['saveMessage'], undefined> = {}

    const role = parseOptionalString(raw.save_message.role)
    if (role !== undefined) saveMessage.role = celString(role)
    const content = parseOptionalString(raw.save_message.content)
    if (content !== undefined) saveMessage.content = celString(content)
    const toolCalls = parseOptionalString(raw.save_message.tool_calls)
    if (toolCalls !== undefined) saveMessage.toolCalls = celString(toolCalls)
    const toolResults = parseOptionalString(raw.save_message.tool_results)
    if (toolResults !== undefined) saveMessage.toolResults = celString(toolResults)
    const smCondition = parseOptionalString(raw.save_message.condition)
    if (smCondition !== undefined) saveMessage.condition = directCel(smCondition)
    const attachments = parseOptionalString(raw.save_message.attachments)
    if (attachments !== undefined) saveMessage.attachments = celString(attachments)

    if (Object.keys(saveMessage).length > 0) {
      step.saveMessage = saveMessage
    }
  }

  const daemon = parseRawDaemonSelector(raw.daemon)
  if (daemon) step.daemon = daemon
  const outcome = parseOptionalString(raw.outcome)
  if (outcome) step.outcome = outcome
  if (raw.tolerate_failure === true) step.tolerateFailure = true
  const observe = parseRawObserve(raw.observe)
  if (observe) step.observe = observe
  const preflight = parseRawPreflight(raw.preflight)
  if (preflight) step.preflight = preflight

  return step
}

// A node's `daemon:` as the server reads it (internal/workflow/yaml/cel.go,
// unmarshalCelDaemonSelector): an interpolated string is an expression, a
// known type a type, any other string a name (or id), and a mapping a full
// selector.
function parseRawDaemonSelector(raw: unknown): Step['daemon'] {
  if (typeof raw === 'string') {
    if (raw === '') return undefined
    if (raw.includes('{{')) return { value: { case: 'expr', value: raw } }
    const literal = raw === 'local' || raw === 'cloud' || raw === 'any' ? { type: raw } : { name: raw }
    return { value: { case: 'literal', value: literal } }
  }
  if (!isRecord(raw)) return undefined
  const literal: Record<string, unknown> = {}
  for (const key of ['id', 'name', 'type', 'pool'] as const) {
    const v = parseOptionalString(raw[key])
    if (v) literal[key] = v
  }
  if (isRecord(raw.labels)) {
    const labels = Object.fromEntries(Object.entries(raw.labels).filter(isStringEntry))
    if (Object.keys(labels).length > 0) literal.labels = labels
  }
  return { value: { case: 'literal', value: literal } }
}

function parseStringMap(raw: unknown): Record<string, string> | undefined {
  if (!isRecord(raw)) return undefined
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(raw)) {
    // A scalar is kept as written: a template, or a literal number or bool.
    if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
      out[key] = String(value)
    }
  }
  return Object.keys(out).length > 0 ? out : undefined
}

// An `observe:` block, on a node or the workflow. Its shape is the server's
// to judge on save; this keeps what it says.
function parseRawObserve(raw: unknown): ObserveBlock {
  if (!isRecord(raw)) return undefined
  const observe: Exclude<ObserveBlock, undefined> = {}
  const attributes = parseStringMap(raw.attributes)
  if (attributes) observe.attributes = attributes
  if (Array.isArray(raw.events)) {
    observe.events = raw.events.filter(isRecord).map((event) => ({
      name: parseOptionalString(event.name) ?? '',
      when: parseOptionalString(event.when) ?? '',
      each: parseOptionalString(event.each) ?? '',
      attributes: parseStringMap(event.attributes) ?? {},
    }))
  }
  if (Array.isArray(raw.metrics)) {
    observe.metrics = raw.metrics.filter(isRecord).map((metric) => ({
      name: parseOptionalString(metric.name) ?? '',
      kind: parseOptionalString(metric.kind) ?? '',
      value: metric.value === undefined ? '' : String(metric.value),
      unit: parseOptionalString(metric.unit) ?? '',
      when: parseOptionalString(metric.when) ?? '',
      attributes: parseStringMap(metric.attributes) ?? {},
    }))
  }
  return Object.keys(observe).length > 0 ? observe : undefined
}

function parseRawEdge(raw: Record<string, unknown>): Edge {
  const edge: Edge = {
    from: parseOptionalString(raw.from) ?? '',
  }

  if (Array.isArray(raw.cases)) {
    const parsedCases: EdgeCase[] = raw.cases
      .filter(isRecord)
      .map((rawCase) => {
        const edgeCase: EdgeCase = {}
        const to = parseStringOrStringArray(rawCase.to)
        if (to !== undefined) edgeCase.to = to
        const condition = parseOptionalString(rawCase.condition)
        if (condition !== undefined) edgeCase.condition = condition
        const label = parseOptionalString(rawCase.label)
        if (label !== undefined) edgeCase.label = label
        return edgeCase
      })

    if (parsedCases.length > 0) {
      edge.cases = parsedCases
    }
  }

  const defaultTarget = parseStringOrStringArray(raw.default)
  if (defaultTarget !== undefined) {
    edge.default = defaultTarget
  }

  return edge
}

function parseRawParams(raw: Record<string, unknown>): Record<string, Param> {
  const result: Record<string, Param> = {}

  for (const [key, value] of Object.entries(raw)) {
    if (!isRecord(value)) {
      continue
    }

    const type = parseOptionalString(value.type) ?? 'string'
    const init: Record<string, unknown> = {}

    if ('default' in value) init.default = value.default

    const description = parseOptionalString(value.description)
    if (description !== undefined) init.description = description

    const ui = parseOptionalString(value.ui)
    if (ui !== undefined) init.ui = ui

    const enumValues = parseOptionalStringArray(value.enum_values)
    if (enumValues !== undefined) init.enumValues = enumValues

    const multi = parseOptionalBoolean(value.multi)
    if (multi !== undefined) init.multi = multi

    const min = parseOptionalNumber(value.min)
    if (min !== undefined) init.min = min

    const max = parseOptionalNumber(value.max)
    if (max !== undefined) init.max = max

    const pattern = parseOptionalString(value.pattern)
    if (pattern !== undefined) init.pattern = pattern

    const minLength = parseOptionalNumber(value.min_length)
    if (minLength !== undefined) init.minLength = minLength

    const maxLength = parseOptionalNumber(value.max_length)
    if (maxLength !== undefined) init.maxLength = maxLength

    const minItems = parseOptionalNumber(value.min_items)
    if (minItems !== undefined) init.minItems = minItems

    const maxItems = parseOptionalNumber(value.max_items)
    if (maxItems !== undefined) init.maxItems = maxItems

    const tags = parseOptionalStringArray(value.tags)
    if (tags !== undefined) init.tags = tags

    if (isRecord(value.preset_config)) {
      const tag = parseOptionalString(value.preset_config.tag)
      if (tag !== undefined) {
        init.presets = {
          tag,
          default: parseOptionalString(value.preset_config.default) ?? '',
        }
      }
    }

    if (isRecord(value.inputs)) {
      init.inputs = parseRawParams(value.inputs)
    }

    result[key] = createInput(type, init) as Param
  }

  return result
}

function parseRawUI(raw: { positions?: Record<string, { x: number; y: number }>; switches?: unknown; locked?: boolean }): WorkflowUI {
  const normalizeSwitchCondition = (value: unknown) => {
    const parsed = parseOptionalString(value)
    if (parsed === undefined || parsed.length === 0) {
      return undefined
    }
    return directCel(parsed)
  }
  const positions: Record<string, { x: number; y: number }> = {}

  if (raw.positions) {
    for (const [id, pos] of Object.entries(raw.positions)) {
      positions[id] = { x: pos.x, y: pos.y }
    }
  }

  const switches: Record<string, SwitchMetadata> = {}
  if (isRecord(raw.switches)) {
    for (const [switchId, rawSwitchMeta] of Object.entries(raw.switches)) {
      if (!isRecord(rawSwitchMeta)) {
        continue
      }

      const sourceNode = parseOptionalString(rawSwitchMeta.source_node)
      if (!sourceNode) {
        continue
      }

      let position = { x: 0, y: 0 }
      if (isRecord(rawSwitchMeta.position)) {
        position = {
          x: parseOptionalNumber(rawSwitchMeta.position.x) ?? 0,
          y: parseOptionalNumber(rawSwitchMeta.position.y) ?? 0,
        }
      }

      const parsedCases: SwitchCase[] = Array.isArray(rawSwitchMeta.cases)
        ? rawSwitchMeta.cases
            .filter(isRecord)
            .map((rawCase) => ({
              id: parseOptionalString(rawCase.id) ?? '',
              condition: normalizeSwitchCondition(rawCase.condition),
              label: parseOptionalString(rawCase.label),
            }))
            .filter((c) => c.id.length > 0)
        : []

      switches[switchId] = {
        sourceNode,
        position,
        cases: parsedCases,
      }
    }
  }

  return {
    positions,
    switches,
    locked: raw.locked === true,
  }
}
/**
 * Typed accessors for action step args.
 *
 * Action steps (call_llm, execute_tools, etc.) store parameters in the proto
 * args oneof.  The catalog uses snake_case field names (matching proto), while
 * proto-es generates camelCase property names.  These helpers bridge the gap
 * so that the UI can read/write proto args directly — no flat "inputs" bag.
 */

import type { Step } from '../types/workflow'
import { celString, celExpr } from './celAdapter'

// ---------------------------------------------------------------------------
// Case conversion
// ---------------------------------------------------------------------------

/** Convert snake_case to camelCase (e.g. "system_prompt" → "systemPrompt") */
export function snakeToCamel(s: string): string {
  return s.replace(/_([a-z])/g, (_, c) => c.toUpperCase())
}

// ---------------------------------------------------------------------------
// Step type → args.case mapping
// ---------------------------------------------------------------------------

export const TYPE_TO_CASE: Record<string, string> = {
  call_llm: 'callLlm',
  execute_tools: 'executeTools',
  compact: 'compact',
  approval: 'approval',
  save_message: 'saveMessageNode',
  create_worktree: 'createWorktree',
}

/** Check whether a step type has typed proto args (i.e., is an action step) */
export function hasTypedArgs(step: Step): boolean {
  return step.type !== undefined && TYPE_TO_CASE[step.type] !== undefined
}

// ---------------------------------------------------------------------------
// Read helpers
// ---------------------------------------------------------------------------

/**
 * Get the typed args object from an action step.
 * Returns the args.value record if the args.case matches the expected type,
 * or falls back to args.value if the step type implies action args.
 */
function getArgsValue(step: Step): Record<string, unknown> | undefined {
  if (!step.args?.value) return undefined
  const expectedCase = step.type ? TYPE_TO_CASE[step.type] : undefined
  if (expectedCase && step.args.case === expectedCase) {
    return step.args.value as Record<string, unknown>
  }
  // Fallback: trust the value even if case doesn't match (proto-es quirks)
  if (expectedCase) {
    return step.args.value as Record<string, unknown>
  }
  return undefined
}

/**
 * Read a single field from action step args by its catalog name (snake_case).
 * Returns the raw proto value (CEL wrapper). ProtoFieldRenderer handles
 * unwrapping via normalizeProtoFieldValue().
 */
export function getActionArgValue(step: Step, catalogFieldName: string): unknown {
  const argsValue = getArgsValue(step)
  if (!argsValue) return undefined
  const camelKey = snakeToCamel(catalogFieldName)
  return argsValue[camelKey]
}

/**
 * Get all args fields as a shallow record keyed by camelCase property names.
 * Useful for debug display (NodeDetailsPanel).
 */
export function getActionArgsRecord(step: Step): Record<string, unknown> {
  return getArgsValue(step) ?? {}
}

// ---------------------------------------------------------------------------
// Write helpers
// ---------------------------------------------------------------------------

/**
 * Wrap a plain UI value in the appropriate CEL wrapper for storage in proto args.
 *
 * The catalog field's `type` and `isCel` tell us what wrapper to use:
 * - string  → CelString
 * - boolean → CelBool
 * - integer → CelInt
 * - number  → CelDouble
 * - model   → CelModelSelector (wraps { id: value })
 * - array/string_list (tool_filter) → CelStringList
 *
 * When isCel is false, the value is stored directly (no wrapper).
 */
function wrapValue(value: unknown, fieldType: string, isCel: boolean): unknown {
  if (value === undefined || value === null) return undefined

  // Non-CEL fields: store as-is
  if (!isCel) return value

  const strValue = typeof value === 'string' ? value : String(value)
  const isExpr = strValue.includes('{{')

  switch (fieldType) {
    case 'string':
      if (!strValue) return undefined
      return celString(strValue)

    case 'boolean': {
      if (isExpr) return celExpr(strValue)
      const boolVal = typeof value === 'boolean' ? value : strValue === 'true'
      return { value: { case: 'literal' as const, value: boolVal } }
    }

    case 'integer': {
      if (isExpr) return celExpr(strValue)
      if (!strValue) return undefined
      const intVal = parseInt(strValue, 10)
      if (Number.isNaN(intVal)) return celString(strValue)
      return { value: { case: 'literal' as const, value: BigInt(intVal) } }
    }

    case 'number': {
      if (isExpr) return celExpr(strValue)
      if (!strValue) return undefined
      const numVal = parseFloat(strValue)
      if (Number.isNaN(numVal)) return celString(strValue)
      return { value: { case: 'literal' as const, value: numVal } }
    }

    case 'model': {
      if (!strValue) return undefined
      if (isExpr) return celExpr(strValue)
      return { value: { case: 'literal' as const, value: { id: strValue, tags: [], providers: [] } } }
    }

    case 'array':
    case 'string_list': {
      if (!strValue) return undefined
      if (isExpr) return celExpr(strValue)
      // Comma-separated list → CelStringList literal
      const items = strValue.split(',').map(s => s.trim()).filter(Boolean)
      return { value: { case: 'literal' as const, value: { values: items } } }
    }

    default:
      // Fallback: treat as CelString
      if (!strValue) return undefined
      return celString(strValue)
  }
}

/**
 * Return a new Step with a single args field updated.
 * `catalogFieldName` is the snake_case name from the catalog.
 * `value` is the plain UI value (string, boolean, number) from ProtoFieldRenderer.
 * `fieldType` is the catalog field type (string, boolean, integer, number, model, etc.).
 * `isCel` indicates whether the field supports CEL expressions.
 */
export function withActionArg(
  step: Step,
  catalogFieldName: string,
  value: unknown,
  fieldType: string,
  isCel: boolean,
): Step {
  const camelKey = snakeToCamel(catalogFieldName)
  const wrapped = wrapValue(value, fieldType, isCel)

  const argsCase = step.type ? TYPE_TO_CASE[step.type] : step.args?.case
  if (!argsCase) return step

  const currentArgs = (step.args?.value as Record<string, unknown>) ?? {}
  const newArgsValue = { ...currentArgs, [camelKey]: wrapped }

  return {
    ...step,
    args: { case: argsCase as any, value: newArgsValue },
  } as Step
}

/**
 * Bulk-set multiple args fields. Used for applying defaults.
 */
export function withActionArgs(
  step: Step,
  fields: Array<{ name: string; value: unknown; type: string; isCel: boolean }>,
): Step {
  let result = step
  for (const field of fields) {
    result = withActionArg(result, field.name, field.value, field.type, field.isCel)
  }
  return result
}

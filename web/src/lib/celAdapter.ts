// =============================================================================
// CEL VALUE CONSTRUCTORS
// Proto types CelString and DirectCelBool reject plain strings.
// Use these constructors to create values for CEL-capable fields.
//
// The returned objects carry the proto Message<> brand via a single cast
// inside each factory, so call sites don't need `as any` to assign them to
// proto fields.
// =============================================================================

import type {
  CelString,
  DirectCelBool,
  ProjectConfig,
} from '../gen/reliant/v1/workflow_v2_pb'

/** Construct a CelString literal: { value: { case: 'literal', value } } */
export function celLiteral(s: string): CelString {
  return { value: { case: 'literal' as const, value: s } } as CelString
}

/** Construct a CelString expression: { value: { case: 'expr', value } } */
export function celExpr(s: string): CelString {
  return { value: { case: 'expr' as const, value: s } } as CelString
}

/**
 * Construct a CelString from a raw string.
 * Strings containing {{ }} are treated as expressions, otherwise literals.
 */
export function celString(s: string): CelString {
  return s.includes('{{') ? celExpr(s) : celLiteral(s)
}

/** Construct a DirectCelBool expression: { expr } */
export function directCel(expr: string): DirectCelBool {
  return { expr } as DirectCelBool
}

/**
 * Construct a `ProjectConfig` from a literal path string. Wraps the path in
 * `celString` and brand-casts the outer object once so call sites can pass
 * the result straight to proto fields without `as any`.
 */
export function projectConfigLiteral(path: string): ProjectConfig {
  return { path: celString(path) } as ProjectConfig
}

// =============================================================================
// CEL VALUE NORMALIZERS (read-side)
// =============================================================================

type CelOneofValueCase = 'literal' | 'expr'

type CelOneofValue<T> = {
  case?: CelOneofValueCase
  value?: T | string
}

type CelWrapper<T> = {
  value?: CelOneofValue<T> | T | string
  literal?: T
  expr?: string
}

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

/**
 * Normalizes plain strings and CEL wrapper values to a scalar string.
 *
 * Supports:
 * - plain string
 * - oneof shape: { value: { case: 'literal' | 'expr', value: string } }
 * - fallback shape: { literal: string } or { expr: string }
 */
export function normalizeCelString(value: unknown, fallback = ''): string {
  if (typeof value === 'string') {
    return value
  }

  if (typeof value === 'number') {
    return String(value)
  }

  if (!isObjectRecord(value)) {
    return fallback
  }

  const wrapper = value as CelWrapper<string>

  if (isObjectRecord(wrapper.value)) {
    const oneof = wrapper.value as CelOneofValue<string>
    if ((oneof.case === 'literal' || oneof.case === 'expr') && typeof oneof.value === 'string') {
      return oneof.value
    }
    if ((oneof.case === 'literal' || oneof.case === 'expr') && typeof oneof.value === 'number') {
      return String(oneof.value)
    }
  }

  if (typeof wrapper.literal === 'string') {
    return wrapper.literal
  }

  if (typeof wrapper.literal === 'number') {
    return String(wrapper.literal)
  }

  if (typeof wrapper.expr === 'string') {
    return wrapper.expr
  }

  if (typeof wrapper.value === 'string') {
    return wrapper.value
  }

  return fallback
}

/**
 * Normalizes plain numbers and CEL wrapper numeric values to a string.
 *
 * Returns the literal numeric value as a string, the CEL expression string,
 * or `''` when the value is unrecognised. Used by `<input type="number">`
 * controls that need a string for their `value` prop.
 *
 * Supports:
 * - plain number / bigint / string
 * - oneof shape: { value: { case: 'literal', value: number | bigint } }
 * - oneof shape: { value: { case: 'expr', value: string } }
 * - fallback shape: { literal: number | bigint } or { expr: string }
 */
export function normalizeCelNumberString(value: unknown): string {
  if (typeof value === 'number' || typeof value === 'bigint') {
    return String(value)
  }

  if (typeof value === 'string') {
    return value
  }

  if (!isObjectRecord(value)) {
    return ''
  }

  const wrapper = value as CelWrapper<number | bigint>

  if (isObjectRecord(wrapper.value)) {
    const oneof = wrapper.value as CelOneofValue<number | bigint>
    if (oneof.case === 'literal' && (typeof oneof.value === 'number' || typeof oneof.value === 'bigint')) {
      return String(oneof.value)
    }
    if (oneof.case === 'expr' && typeof oneof.value === 'string') {
      return oneof.value
    }
  }

  if (typeof wrapper.literal === 'number' || typeof wrapper.literal === 'bigint') {
    return String(wrapper.literal)
  }

  if (typeof wrapper.expr === 'string') {
    return wrapper.expr
  }

  return ''
}

/**
 * Unwraps a CEL-wrapped literal value of arbitrary type `T`, returning either:
 * - the literal `T` (e.g. a `ModelValue` object for model selectors);
 * - the CEL expression string when the value is `{ case: 'expr' }`;
 * - the raw `value` unchanged when it's already a `T` and not wrapped;
 * - `undefined` when the value is not a recognised CEL wrapper.
 *
 * Useful for non-string scalar literals (objects, ModelSelectors) where the
 * caller still wants to peek into the wrapper without committing to a
 * `string` representation up front.
 */
export function unwrapCelLiteralOrExpr<T>(
  value: unknown,
): { kind: 'literal'; value: T } | { kind: 'expr'; value: string } | undefined {
  if (!isObjectRecord(value)) {
    return undefined
  }

  const wrapper = value as CelWrapper<T>

  if (isObjectRecord(wrapper.value)) {
    const oneof = wrapper.value as CelOneofValue<T>
    if (oneof.case === 'literal' && oneof.value !== undefined) {
      return { kind: 'literal', value: oneof.value as T }
    }
    if (oneof.case === 'expr' && typeof oneof.value === 'string') {
      return { kind: 'expr', value: oneof.value }
    }
  }

  if (wrapper.literal !== undefined) {
    return { kind: 'literal', value: wrapper.literal as T }
  }

  if (typeof wrapper.expr === 'string') {
    return { kind: 'expr', value: wrapper.expr }
  }

  return undefined
}

/**
 * Normalizes plain booleans and CEL wrapper values to a scalar boolean,
 * or returns the CEL expression string when the value is an expression.
 *
 * Supports:
 * - plain boolean
 * - plain string: CEL expression (contains {{}}) returned as-is, "true"/"false" parsed
 * - oneof shape: { value: { case: 'literal', value: boolean } }
 * - oneof shape: { value: { case: 'expr', value: string } } → returns expression string
 * - fallback shape: { literal: boolean }
 * - fallback shape: { expr: string } → returns expression string
 */
export function normalizeCelBoolean(value: unknown, fallback: boolean = false): boolean | string {
  if (typeof value === 'boolean') {
    return value
  }

  if (typeof value === 'string') {
    if (value.includes('{{')) {
      return value
    }
    if (value === 'true') return true
    if (value === 'false') return false
    return fallback
  }

  if (!isObjectRecord(value)) {
    return fallback
  }

  const wrapper = value as CelWrapper<boolean>

  if (isObjectRecord(wrapper.value)) {
    const oneof = wrapper.value as CelOneofValue<boolean>
    if (oneof.case === 'literal' && typeof oneof.value === 'boolean') {
      return oneof.value
    }
    if (oneof.case === 'expr' && typeof oneof.value === 'string') {
      return oneof.value
    }
  }

  if (typeof wrapper.literal === 'boolean') {
    return wrapper.literal
  }

  if (typeof wrapper.expr === 'string') {
    return wrapper.expr
  }

  if (typeof wrapper.value === 'boolean') {
    return wrapper.value
  }

  return fallback
}

// =============================================================================
// CEL VALUE CONSTRUCTORS
// Proto types CelString and DirectCelBool reject plain strings.
// Use these constructors to create values for CEL-capable fields.
// =============================================================================

/** Construct a CelString literal: { value: { case: 'literal', value } } */
export function celLiteral(s: string) {
  return { value: { case: 'literal' as const, value: s } }
}

/** Construct a CelString expression: { value: { case: 'expr', value } } */
export function celExpr(s: string) {
  return { value: { case: 'expr' as const, value: s } }
}

/**
 * Construct a CelString from a raw string.
 * Strings containing {{ }} are treated as expressions, otherwise literals.
 */
export function celString(s: string) {
  return s.includes('{{') ? celExpr(s) : celLiteral(s)
}

/** Construct a DirectCelBool expression: { expr } */
export function directCel(expr: string) {
  return { expr }
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

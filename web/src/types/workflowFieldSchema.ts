import { normalizeCelBoolean, normalizeCelString } from '../lib/celAdapter'

export type ProtoFieldWidget = 'text' | 'textarea' | 'select' | 'checkbox' | 'model' | 'tools' | 'number'
export type ProtoFieldValueKind = 'string' | 'boolean'

export interface ProtoFieldOption {
  value: string
  label: string
}

export type ProtoFieldContext = Record<string, unknown>

export interface ProtoFieldSchema {
  key: string
  label: string
  description?: string
  helpText?: string
  placeholder?: string
  widget: ProtoFieldWidget
  /** For 'number' widget: true = integer (step=1, parseInt), false = float (step=any, parseFloat) */
  isInteger?: boolean
  valueKind?: ProtoFieldValueKind
  celCapable?: boolean
  celExpressionOnly?: boolean
  showCelModeToggle?: boolean
  options?: ProtoFieldOption[]
  allowEmptyOption?: boolean
  emptyOptionLabel?: string
  /**
   * Field is shown only when all listed keys are present in context.
   */
  requiresContext?: string[]
  /**
   * Field is shown only when context keys match expected values.
   */
  visibleWhen?: Record<string, unknown>
  /**
   * Escape hatch for role/loop dependent visibility.
   */
  isVisible?: (context: ProtoFieldContext) => boolean
  /**
   * Omit value from serialized config when normalized value is empty string.
   */
  omitIfEmpty?: boolean
  /**
   * Omit value from serialized config when it matches defaultValue.
   */
  omitIfDefault?: boolean
  defaultValue?: unknown
  annotations?: {
    defaultValue?: string
    range?: string
  }
}

export function isProtoFieldVisible(schema: ProtoFieldSchema, context: ProtoFieldContext = {}): boolean {
  const requiredKeys = schema.requiresContext
  if (requiredKeys && requiredKeys.length > 0) {
    const hasRequiredContext = requiredKeys.every((key) => {
      if (!Object.prototype.hasOwnProperty.call(context, key)) {
        return false
      }

      const value = context[key]
      return value !== null && value !== undefined
    })

    if (!hasRequiredContext) {
      return false
    }
  }

  if (schema.visibleWhen) {
    const matches = Object.entries(schema.visibleWhen).every(([key, expectedValue]) => {
      return context[key] === expectedValue
    })

    if (!matches) {
      return false
    }
  }

  if (schema.isVisible) {
    return schema.isVisible(context)
  }

  return true
}

export function normalizeProtoFieldValue(schema: ProtoFieldSchema, value: unknown): unknown {
  if (schema.valueKind === 'boolean' || schema.widget === 'checkbox') {
    const boolFallback = typeof schema.defaultValue === 'boolean' ? schema.defaultValue : false
    return normalizeCelBoolean(value, boolFallback)
  }

  if (schema.valueKind === 'string' || schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'select' || schema.widget === 'tools') {
    const stringFallback = typeof schema.defaultValue === 'string' ? schema.defaultValue : ''
    return normalizeCelString(value, stringFallback)
  }

  return value
}

export function shouldOmitProtoFieldValue(schema: ProtoFieldSchema, value: unknown): boolean {
  if (schema.omitIfEmpty && typeof value === 'string' && value.trim() === '') {
    return true
  }

  if (schema.omitIfDefault && value === schema.defaultValue) {
    return true
  }

  return false
}

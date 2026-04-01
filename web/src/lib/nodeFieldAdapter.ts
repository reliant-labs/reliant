import type { NodeInputField } from '../gen/reliant/v1/catalog_pb'
import type { ProtoFieldSchema } from '../types/workflowFieldSchema'
import { formatValueForDisplay } from './paramUtils'
import type { InputDef } from './inputHelpers'
import {
  getInputDescription,
  getInputDefault,
  getInputEnumValues,
  getInputMin,
  getInputMax,
  getInputUI,
} from './inputHelpers'

function formatLabel(name: string): string {
  return name.charAt(0).toUpperCase() + name.slice(1).replace(/_/g, ' ')
}

/**
 * Convert a NodeInputField (catalog RPC type) to a ProtoFieldSchema
 * (what ProtoFieldRenderer consumes). This eliminates the need for
 * hand-rolled rendering logic in ActionStepConfig.
 */
export function nodeInputFieldToSchema(field: NodeInputField): ProtoFieldSchema {
  const label = field.label || formatLabel(field.name)

  // Build enriched help text: combine description with default/range info
  const helpParts: string[] = []
  if (field.description) helpParts.push(field.description)
  if (field.defaultValue) helpParts.push(`Default: ${formatValueForDisplay(field.defaultValue)}`)
  if (field.minValue !== undefined && field.maxValue !== undefined) {
    helpParts.push(`Range: ${field.minValue} – ${field.maxValue}`)
  } else if (field.minValue !== undefined) {
    helpParts.push(`Min: ${field.minValue}`)
  } else if (field.maxValue !== undefined) {
    helpParts.push(`Max: ${field.maxValue}`)
  }

  const helpText = helpParts.length > 0 ? helpParts.join('. ') : undefined

  const base = {
    key: field.name,
    label,
    description: field.description || undefined,
    helpText,
    placeholder: field.placeholder,
    omitIfEmpty: field.cleanupSemantics === 'trim',
  }

  // Enum fields → select widget with CEL toggle
  if (field.enumValues && field.enumValues.length > 0) {
    return {
      ...base,
      widget: 'select',
      valueKind: 'string',
      celCapable: true,
      showCelModeToggle: true,
      options: field.enumValues.map((v) => ({ value: v, label: v })),
      allowEmptyOption: !field.required,
      emptyOptionLabel: 'None',
    }
  }

  // Boolean → checkbox
  if (field.type === 'boolean') {
    return {
      ...base,
      widget: 'checkbox',
      valueKind: 'boolean',
    }
  }

  // Numbers are always entered as text/CEL
  if (field.type === 'integer' || field.type === 'number') {
    return {
      ...base,
      widget: 'text',
      valueKind: 'string',
      celCapable: true,
    }
  }

  // Textarea hint
  if (field.uiHint === 'textarea') {
    return {
      ...base,
      widget: 'textarea',
      valueKind: 'string',
      celCapable: field.isCel,
    }
  }

  // Model → model picker with CEL toggle
  if (field.type === 'model') {
    return {
      ...base,
      widget: 'model',
      valueKind: 'string',
      celCapable: true,
      showCelModeToggle: true,
    }
  }

  // Tool filter → rich tools selector
  if ((field.type === 'array' || field.type === 'string_list') && field.name === 'tool_filter') {
    return {
      ...base,
      widget: 'tools',
      valueKind: 'string',
      celCapable: field.isCel,
      showCelModeToggle: true,
    }
  }

  // Array or string_list → textarea
  if (field.type === 'array' || field.type === 'string_list') {
    return {
      ...base,
      widget: 'textarea',
      valueKind: 'string',
      celCapable: field.isCel,
    }
  }

  // Default: text input, cel-capable if field.isCel
  return {
    ...base,
    widget: 'text',
    valueKind: 'string',
    celCapable: field.isCel,
  }
}

/**
 * Group fields by their category, preserving the order in which categories first appear.
 * Fields without a category go into an empty-string group.
 */
export function groupFieldsByCategory(
  fields: NodeInputField[],
): { category: string; fields: NodeInputField[] }[] {
  const groups: { category: string; fields: NodeInputField[] }[] = []
  const seen = new Map<string, number>()

  for (const field of fields) {
    const cat = field.category || ''
    const idx = seen.get(cat)
    if (idx !== undefined) {
      groups[idx].fields.push(field)
    } else {
      seen.set(cat, groups.length)
      groups.push({ category: cat, fields: [field] })
    }
  }

  return groups
}

/**
 * Convert a workflow InputDef (proto Input oneof) to a ProtoFieldSchema
 * so that WorkflowInputGroup can render via ProtoFieldRenderer — the same
 * renderer used by action steps (call_llm, etc.).
 */
export function inputDefToSchema(name: string, input: InputDef): ProtoFieldSchema {
  // Strip group prefix for display label
  const baseName = name.includes('.') ? name.split('.').pop()! : name
  const label = formatLabel(baseName)

  const description = getInputDescription(input)
  const defaultVal = getInputDefault(input)
  const min = getInputMin(input)
  const max = getInputMax(input)
  const ui = getInputUI(input)

  // Build help text from description + default + range
  const helpParts: string[] = []
  if (description) helpParts.push(description)
  if (defaultVal !== undefined && defaultVal !== null && defaultVal !== '') {
    helpParts.push(`Default: ${formatValueForDisplay(defaultVal)}`)
  }
  if (min !== undefined && max !== undefined) {
    helpParts.push(`Range: ${min} – ${max}`)
  } else if (min !== undefined) {
    helpParts.push(`Min: ${min}`)
  } else if (max !== undefined) {
    helpParts.push(`Max: ${max}`)
  }
  const helpText = helpParts.length > 0 ? helpParts.join('. ') : undefined

  const base: Partial<ProtoFieldSchema> = {
    key: name,
    label,
    description: description || undefined,
    helpText,
  }

  const type = input?.type as string | undefined

  // Tools → rich tools selector
  if (type === 'tools') {
    return { ...base, widget: 'tools', valueKind: 'string', celCapable: true, showCelModeToggle: true } as ProtoFieldSchema
  }

  // Model → model picker with CEL toggle
  if (type === 'model') {
    return { ...base, widget: 'model', valueKind: 'string', celCapable: true, showCelModeToggle: true } as ProtoFieldSchema
  }

  // Enum → select widget
  const enumValues = getInputEnumValues(input)
  if (type === 'enum' && enumValues && enumValues.length > 0) {
    return {
      ...base,
      widget: 'select',
      valueKind: 'string',
      celCapable: true,
      showCelModeToggle: true,
      options: enumValues.map((v) => ({ value: v, label: v || 'Off' })),
      allowEmptyOption: true,
      emptyOptionLabel: 'None',
    } as ProtoFieldSchema
  }

  // Boolean → checkbox
  if (type === 'boolean') {
    return { ...base, widget: 'checkbox', valueKind: 'boolean' } as ProtoFieldSchema
  }

  // Numbers → number input (proper numeric type, CEL-capable for expressions)
  if (type === 'number' || type === 'integer') {
    return { ...base, widget: 'number', isInteger: type === 'integer', celCapable: true } as ProtoFieldSchema
  }

  // Textarea hint or multiline-likely fields
  if (ui === 'textarea' || baseName.includes('prompt') || baseName.includes('description')) {
    return { ...base, widget: 'textarea', valueKind: 'string', celCapable: true } as ProtoFieldSchema
  }

  // Array → textarea
  if (type === 'array') {
    return { ...base, widget: 'textarea', valueKind: 'string', celCapable: true } as ProtoFieldSchema
  }

  // Default: text input, CEL-capable
  return { ...base, widget: 'text', valueKind: 'string', celCapable: true } as ProtoFieldSchema
}

/**
 * Partition fields into basic and advanced groups.
 * Fields with visibilityContexts including 'advanced' go to advanced;
 * everything else (including fields with no contexts, or 'basic') goes to basic.
 * ResponseToolDefinition fields are excluded entirely (handled separately).
 */
export function partitionFieldsByVisibility(fields: NodeInputField[]): {
  basic: NodeInputField[]
  advanced: NodeInputField[]
} {
  const basic: NodeInputField[] = []
  const advanced: NodeInputField[] = []

  for (const field of fields) {
    const contexts = field.visibilityContexts
    if (contexts.length === 0 || contexts.includes('basic')) {
      basic.push(field)
    } else {
      advanced.push(field)
    }
  }

  return { basic, advanced }
}

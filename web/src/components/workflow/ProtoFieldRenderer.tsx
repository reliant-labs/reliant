import { ALargeSmall, Braces, List } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { cn } from '../../lib/utils'
import { HelpPopover } from '../ui/HelpPopover'
import { Toggle } from '../ui/Toggle'
import { CELInput } from './CELInput'
import { ModelDropdown, extractModelId } from './ModelDropdown'
import { ToolsSelector } from './ToolsSelector'
import type { ModelValue } from './ModelDropdown'
import type { ProtoFieldContext, ProtoFieldSchema } from '../../types/workflowFieldSchema'
import { isProtoFieldVisible, normalizeProtoFieldValue } from '../../types/workflowFieldSchema'

interface ProtoFieldRendererProps {
  schema: ProtoFieldSchema
  value: unknown
  onChange: (value: unknown) => void
  context?: ProtoFieldContext
  disabled?: boolean
  className?: string
  celContext?: 'default' | 'workflow' | 'loop_while' | 'edge_condition' | 'save_message' | 'thread'
  currentNodeType?: string
  /** Hide CEL toggle and CEL badge — for contexts where CEL doesn't apply (e.g. workflow params) */
  hideCELToggle?: boolean
}

const DEFAULT_CEL_REGEX = /\{\{[\s\S]*\}\}/

function canRenderAsSelectLiteral(value: string, options: NonNullable<ProtoFieldSchema['options']>): boolean {
  if (value.length === 0) {
    return true
  }

  return options.some((option) => option.value === value)
}

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function getModelStringValue(value: unknown): string {
  if (typeof value === 'string') {
    return value
  }

  if (isObjectRecord(value)) {
    const wrapper = value as { value?: { case?: string; value?: unknown }; literal?: unknown; expr?: unknown }
    if (wrapper.value?.case === 'expr' && typeof wrapper.value.value === 'string') {
      return wrapper.value.value
    }
    if (wrapper.value?.case === 'literal') {
      return extractModelId(wrapper.value.value as ModelValue)
    }
    if (typeof wrapper.expr === 'string') {
      return wrapper.expr
    }
    if (wrapper.literal) {
      return extractModelId(wrapper.literal as ModelValue)
    }
  }

  return extractModelId(value as ModelValue)
}

function getNumberStringValue(value: unknown): string {
  if (typeof value === 'number' || typeof value === 'bigint') {
    return String(value)
  }

  if (typeof value === 'string') {
    return value
  }

  if (isObjectRecord(value)) {
    const wrapper = value as { value?: { case?: string; value?: unknown }; literal?: unknown; expr?: unknown }
    if (wrapper.value?.case === 'literal' && (typeof wrapper.value.value === 'number' || typeof wrapper.value.value === 'bigint')) {
      return String(wrapper.value.value)
    }
    if (wrapper.value?.case === 'expr' && typeof wrapper.value.value === 'string') {
      return wrapper.value.value
    }
    if (typeof wrapper.literal === 'number' || typeof wrapper.literal === 'bigint') {
      return String(wrapper.literal)
    }
    if (typeof wrapper.expr === 'string') {
      return wrapper.expr
    }
  }

  return ''
}

export function ProtoFieldRenderer({
  schema,
  value,
  onChange,
  context,
  disabled = false,
  className,
  celContext,
  currentNodeType,
  hideCELToggle = false,
}: ProtoFieldRendererProps) {
  const helperText = schema.helpText || schema.description
  const normalizedValue = normalizeProtoFieldValue(schema, value)
  const resolvedCelContext = celContext === 'workflow' ? 'default' : celContext
  const isInlineCheckbox = schema.widget === 'checkbox' && typeof normalizedValue !== 'string'
  const normalizedStringValue = typeof normalizedValue === 'string' ? normalizedValue : ''
  const normalizedBooleanValue = typeof normalizedValue === 'boolean' ? normalizedValue : Boolean(normalizedValue)
  const modelStringValue = getModelStringValue(value)
  const numberStringValue = getNumberStringValue(value)
  const inputId = schema.key.replace(/\./g, '-')

  const supportsModeToggle = !hideCELToggle && schema.celCapable && !schema.celExpressionOnly && (
    (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number') ||
    ((schema.widget === 'select' || schema.widget === 'model' || schema.widget === 'tools') && schema.showCelModeToggle)
  )
  const options = useMemo(() => schema.options ?? [], [schema.options])
  const shouldForceCelMode = useMemo(() => {
    if (!supportsModeToggle) {
      return false
    }

    if (DEFAULT_CEL_REGEX.test(normalizedStringValue) || DEFAULT_CEL_REGEX.test(modelStringValue) || DEFAULT_CEL_REGEX.test(numberStringValue)) {
      return true
    }

    if (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number' || schema.widget === 'model') {
      return false
    }

    return !canRenderAsSelectLiteral(normalizedStringValue, options)
  }, [normalizedStringValue, modelStringValue, numberStringValue, options, supportsModeToggle, schema.widget])

  const [useCelMode, setUseCelMode] = useState(shouldForceCelMode)

  useEffect(() => {
    if (shouldForceCelMode) {
      setUseCelMode(true)
    }
  }, [shouldForceCelMode])

  if (!isProtoFieldVisible(schema, context)) {
    return null
  }

  const isCelInput = schema.celExpressionOnly || (supportsModeToggle && useCelMode && (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number'))

  const labelAction = (
    <div className="ml-auto flex items-center gap-1">
      {!hideCELToggle && schema.celCapable && !supportsModeToggle && (
        <span className="cpv2-cel-toggle active">CEL</span>
      )}
      {supportsModeToggle && !disabled && (
        <div className="cpv2-mode-group gap-[2px]">
          <button
            type="button"
            onClick={() => {
              setUseCelMode(false)
              if (schema.widget === 'select' && !canRenderAsSelectLiteral(normalizedStringValue, options)) {
                const fallbackOption = options[0]
                onChange(fallbackOption ? fallbackOption.value : '')
              }
            }}
            className={cn('cpv2-mode-pill !flex-none !p-[3px_6px]', !useCelMode && 'active')}
            title={schema.widget === 'select' || schema.widget === 'model' || schema.widget === 'tools' ? 'Use dropdown' : 'Use literal value'}
          >
            {schema.widget === 'select' || schema.widget === 'model' || schema.widget === 'tools' ? (
              <List className="w-3 h-3" />
            ) : (
              <ALargeSmall className="w-3 h-3" />
            )}
          </button>
          <button
            type="button"
            onClick={() => setUseCelMode(true)}
            className={cn('cpv2-mode-pill !flex-none !p-[3px_6px]', useCelMode && 'active')}
            title="Use CEL expression"
          >
            <Braces className="w-3 h-3" />
          </button>
        </div>
      )}
    </div>
  )

  const renderCelInput = (celValue: string, { multiline = false, placeholder = schema.placeholder }: { multiline?: boolean; placeholder?: string } = {}) => (
    <CELInput
      value={celValue}
      onChange={(nextValue) => onChange(nextValue)}
      placeholder={placeholder}
      disabled={disabled}
      multiline={multiline}
      rows={multiline ? 3 : undefined}
      hideCELHint
      showCELIndicator={false}
      pureExpression={schema.celExpressionOnly}
      celContext={resolvedCelContext}
      currentNodeType={currentNodeType}
    />
  )

  return (
    <div className={cn('space-y-1.5', className)}>
      {!isInlineCheckbox && (
        <div className="cpv2-field-label">
          <span className="flex items-center gap-1.5">
            <label htmlFor={inputId}>{schema.label}</label>
            {helperText && <HelpPopover content={helperText} title={schema.label} />}
          </span>
          {labelAction}
        </div>
      )}

      {schema.widget === 'text' &&
        (isCelInput ? (
          renderCelInput(normalizedStringValue)
        ) : (
          <input
            id={inputId}
            value={normalizedStringValue}
            onChange={(event) => onChange(event.target.value)}
            placeholder={schema.placeholder}
            disabled={disabled}
            className="cpv2-field-input"
          />
        ))}

      {schema.widget === 'textarea' &&
        (isCelInput ? (
          renderCelInput(normalizedStringValue, { multiline: true })
        ) : (
          <textarea
            id={inputId}
            value={normalizedStringValue}
            onChange={(event) => onChange(event.target.value)}
            placeholder={schema.placeholder}
            disabled={disabled}
            rows={3}
            className="cpv2-field-textarea"
          />
        ))}

      {schema.widget === 'select' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(normalizedStringValue, { placeholder: schema.placeholder || '{{...}}' })}
        </div>
      ) : (
        <select
          id={inputId}
          value={normalizedStringValue}
          onChange={(event) => onChange(event.target.value)}
          disabled={disabled}
          className="cpv2-field-select"
        >
          {schema.allowEmptyOption && (
            <option value="">{schema.emptyOptionLabel || schema.placeholder || 'Select an option'}</option>
          )}
          {options.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      ))}

      {schema.widget === 'model' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(modelStringValue, { placeholder: schema.placeholder || '{{...}}' })}
        </div>
      ) : (
        <ModelDropdown
          value={modelStringValue ? { id: modelStringValue } : value as Parameters<typeof ModelDropdown>[0]['value']}
          onChange={(nextModel) => onChange(extractModelId(nextModel))}
          disabled={disabled}
          placeholder={schema.placeholder}
        />
      ))}

      {schema.widget === 'number' && (supportsModeToggle && useCelMode ? (
        renderCelInput(numberStringValue)
      ) : (
        <input
          id={inputId}
          type="number"
          value={numberStringValue}
          onChange={(event) => {
            const nextValue = event.target.value
            if (nextValue === '') {
              onChange(undefined)
              return
            }

            const parsedValue = schema.isInteger ? parseInt(nextValue, 10) : parseFloat(nextValue)
            if (!Number.isNaN(parsedValue)) {
              onChange(parsedValue)
            }
          }}
          step={schema.isInteger ? 1 : 'any'}
          min={schema.minValue}
          max={schema.maxValue}
          disabled={disabled}
          className="cpv2-field-input"
        />
      ))}

      {schema.widget === 'tools' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(normalizedStringValue, { placeholder: schema.placeholder || '{{...}}' })}
        </div>
      ) : (
        <ToolsSelector
          value={normalizedStringValue ? normalizedStringValue.split(',').map(s => s.trim()).filter(Boolean) : []}
          onChange={(tools) => onChange(tools.join(', '))}
          disabled={disabled}
          hideLabel
        />
      ))}

      {schema.widget === 'checkbox' && (
        typeof normalizedValue === 'string' ? (
          <div className="border-l-2 border-primary/30 pl-2">
            {renderCelInput(normalizedValue, { placeholder: schema.placeholder || '{{...}}' })}
          </div>
        ) : (
          <div className="space-y-1">
            <div className="cpv2-field-inline py-1.5">
              <div className="flex items-center gap-2 min-w-0">
                <span className="cpv2-fi-label">
                  {schema.label}
                </span>
                {helperText && (
                  <HelpPopover content={helperText} title={schema.label} />
                )}
              </div>
              <Toggle
                id={inputId}
                checked={normalizedBooleanValue}
                onChange={(checked) => onChange(checked)}
                disabled={disabled}
                srLabel={schema.label}
              />
            </div>
          </div>
        )
      )}

      {/* Annotations moved to helpText/tooltip — no longer rendered inline */}
    </div>
  )
}

import { ALargeSmall, Braces, List } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { cn } from '../../lib/utils'
import { HelpPopover } from '../ui/HelpPopover'
import { Toggle } from '../ui/Toggle'
import { CELInput } from './CELInput'
import { ModelDropdown, extractModelId } from './ModelDropdown'
import { ToolsSelector } from './ToolsSelector'
import type { ProtoFieldContext, ProtoFieldSchema } from '../../types/workflowFieldSchema'
import { isProtoFieldVisible, normalizeProtoFieldValue } from '../../types/workflowFieldSchema'

interface ProtoFieldRendererProps {
  schema: ProtoFieldSchema
  value: unknown
  onChange: (value: unknown) => void
  context?: ProtoFieldContext
  disabled?: boolean
  className?: string
  celContext?: 'default' | 'loop_while' | 'edge_condition' | 'save_message' | 'thread'
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
  const isInlineCheckbox = schema.widget === 'checkbox' && typeof normalizedValue !== 'string'
  const normalizedStringValue = typeof normalizedValue === 'string' ? normalizedValue : ''
  const normalizedBooleanValue = typeof normalizedValue === 'boolean' ? normalizedValue : Boolean(normalizedValue)
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

    if (DEFAULT_CEL_REGEX.test(normalizedStringValue)) {
      return true
    }

    // Text/textarea/number: only force CEL mode when value contains CEL expressions (handled above)
    if (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number') {
      return false
    }

    // Model widget: never force CEL mode for normal string values
    if (schema.widget === 'model') {
      return false
    }

    return !canRenderAsSelectLiteral(normalizedStringValue, options)
  }, [normalizedStringValue, options, supportsModeToggle, schema.widget])

  const [useCelMode, setUseCelMode] = useState(shouldForceCelMode)

  useEffect(() => {
    if (shouldForceCelMode) {
      setUseCelMode(true)
    }
  }, [shouldForceCelMode])

  if (!isProtoFieldVisible(schema, context)) {
    return null
  }

  const isCelInput = schema.celExpressionOnly || (supportsModeToggle && useCelMode && (schema.widget === 'text' || schema.widget === 'textarea'))

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
          <CELInput
            value={normalizedStringValue}
            onChange={(nextValue) => onChange(nextValue)}
            placeholder={schema.placeholder}
            disabled={disabled}
            hideCELHint
            showCELIndicator={false}
            pureExpression={schema.celExpressionOnly}
            celContext={celContext}
            currentNodeType={currentNodeType}
          />
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
          <CELInput
            value={normalizedStringValue}
            onChange={(nextValue) => onChange(nextValue)}
            placeholder={schema.placeholder}
            disabled={disabled}
            multiline
            rows={3}
            hideCELHint
            showCELIndicator={false}
            pureExpression={schema.celExpressionOnly}
            celContext={celContext}
            currentNodeType={currentNodeType}
          />
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
          <CELInput
            value={normalizedStringValue}
            onChange={(nextValue) => onChange(nextValue)}
            placeholder={schema.placeholder || '{{...}}'}
            disabled={disabled}
            hideCELHint
            showCELIndicator={false}
            pureExpression={schema.celExpressionOnly}
            celContext={celContext}
            currentNodeType={currentNodeType}
          />
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
          <CELInput
            value={typeof value === 'string' ? value : extractModelId(value as Parameters<typeof extractModelId>[0])}
            onChange={(nextValue) => onChange(nextValue)}
            placeholder={schema.placeholder || '{{...}}'}
            disabled={disabled}
            hideCELHint
            showCELIndicator={false}
            pureExpression={schema.celExpressionOnly}
            celContext={celContext}
            currentNodeType={currentNodeType}
          />
        </div>
      ) : (
        <ModelDropdown
          value={value as Parameters<typeof ModelDropdown>[0]['value']}
          onChange={onChange}
          disabled={disabled}
          placeholder={schema.placeholder}
        />
      ))}

      {schema.widget === 'number' && (supportsModeToggle && useCelMode ? (
        <CELInput
          value={typeof value === 'string' ? value : typeof value === 'number' ? String(value) : ''}
          onChange={(nextValue) => onChange(nextValue)}
          placeholder={schema.placeholder}
          disabled={disabled}
          hideCELHint
          showCELIndicator={false}
          pureExpression={schema.celExpressionOnly}
          celContext={celContext}
          currentNodeType={currentNodeType}
        />
      ) : (
        <input
          id={inputId}
          type="number"
          value={typeof value === 'number' ? value : ''}
          onChange={(e) => {
            const val = e.target.value
            if (val === '') {
              onChange(undefined)
            } else {
              const num = schema.isInteger ? parseInt(val, 10) : parseFloat(val)
              if (!isNaN(num)) onChange(num)
            }
          }}
          step={schema.isInteger ? 1 : 'any'}
          disabled={disabled}
          className="cpv2-field-input"
        />
      ))}

      {schema.widget === 'tools' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          <CELInput
            value={normalizedStringValue}
            onChange={(nextValue) => onChange(nextValue)}
            placeholder={schema.placeholder || '{{...}}'}
            disabled={disabled}
            hideCELHint
            showCELIndicator={false}
            pureExpression={schema.celExpressionOnly}
            celContext={celContext}
            currentNodeType={currentNodeType}
          />
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
            <CELInput
              value={normalizedValue}
              onChange={(nextValue) => onChange(nextValue)}
              placeholder={schema.placeholder || '{{...}}'}
              disabled={disabled}
              hideCELHint
              showCELIndicator={false}
              pureExpression={schema.celExpressionOnly}
              celContext={celContext}
            />
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
import { AlertTriangle, ALargeSmall, Braces, List } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { cn } from '../../lib/utils'
import { HelpPopover } from '../ui/HelpPopover'
import { Toggle } from '../ui/Toggle'
import { CELInput } from './CELInput'
import { InsertDataMenu } from './InsertDataMenu'
import { OptionPicker } from './OptionPicker'
import { celInsertText } from './MonacoCELEditor'
import type { CELInsertTarget } from './CELCompletionContext'
import { ModelDropdown, extractModelId } from './ModelDropdown'
import { ToolsSelector } from './ToolsSelector'
import { useFieldFindings } from './WorkflowFindingsContext'
import type { ModelValue } from './ModelDropdown'
import type { ProtoFieldContext, ProtoFieldSchema } from '../../types/workflowFieldSchema'
import { isProtoFieldVisible, normalizeProtoFieldValue } from '../../types/workflowFieldSchema'
import { normalizeCelNumberString, unwrapCelLiteralOrExpr } from '../../lib/celAdapter'
import { formatValueForDisplay } from '../../lib/paramUtils'

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

/**
 * What an expression-mode input shows when empty: the field's own example
 * when it is an expression (`{{nodes.call_llm.tool_calls}}`), else the shape
 * every template takes, so `{}` mode never opens on a blank box.
 */
export function expressionPlaceholder(schema: ProtoFieldSchema): string {
  if (schema.celExpressionOnly) {
    return schema.example || schema.placeholder || 'nodes.<step>.<output>'
  }
  return [schema.example, schema.placeholder].find((text) => text && DEFAULT_CEL_REGEX.test(text)) ?? '{{nodes.<step>.<output>}}'
}

/** A declared default as words for "Default: …", or undefined when there is none. */
function describeDefault(value: unknown): string | undefined {
  if (value === undefined || value === null || value === '') return undefined
  if (Array.isArray(value)) return value.length > 0 ? value.map((entry) => formatValueForDisplay(entry)).join(', ') : undefined
  if (typeof value === 'object') {
    const model = value as { id?: unknown; tags?: unknown }
    if (typeof model.id === 'string' && model.id) return model.id
    if (Array.isArray(model.tags) && model.tags.length > 0) return model.tags.join(', ')
  }
  return formatValueForDisplay(value)
}

/**
 * Whether a fixed value fails the field's format, for the hint under the
 * input. A template is checked at run time, and an invalid pattern is no
 * reason to warn the author.
 */
export function failsPattern(pattern: string | undefined, value: string): boolean {
  if (!pattern || value === '' || DEFAULT_CEL_REGEX.test(value)) return false
  try {
    return !new RegExp(pattern, 'u').test(value)
  } catch {
    return false
  }
}

/** Split the comma-separated form of a list field into its stored entries. */
function splitStringList(value: string): string[] {
  return value
    .split(',')
    .map((entry) => entry.trim())
    .filter(Boolean)
}

function canRenderAsSelectLiteral(value: string, options: NonNullable<ProtoFieldSchema['options']>): boolean {
  if (value.length === 0) {
    return true
  }

  return options.some((option) => option.value === value)
}

/**
 * Read-side: convert a possibly-CEL-wrapped model value into a string suitable
 * for `<ModelDropdown>` (literal → model id; expr → expression string).
 * Wraps the generic `unwrapCelLiteralOrExpr` from `celAdapter` with a
 * ModelDropdown-specific extractor; the unwrap logic itself lives in one
 * place.
 */
function getModelStringValue(value: unknown): string {
  if (typeof value === 'string') {
    return value
  }
  const unwrapped = unwrapCelLiteralOrExpr<ModelValue>(value)
  if (unwrapped?.kind === 'expr') return unwrapped.value
  if (unwrapped?.kind === 'literal') return extractModelId(unwrapped.value)
  return extractModelId(value as ModelValue)
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
  // The description is printed under the input, where it is read; the ?
  // popover carries only what it adds (default, range), never a copy.
  const inlineHint = schema.description || schema.helpText
  const popoverText = schema.helpText && schema.helpText !== inlineHint ? schema.helpText : undefined
  // An unset field with a default runs with the default, so its empty input
  // says so rather than looking like "nothing".
  const defaultLabel = describeDefault(schema.defaultValue)
  const literalPlaceholder = schema.placeholder ?? (defaultLabel ? `Default: ${defaultLabel}` : schema.example)
  const celPlaceholder = expressionPlaceholder(schema)
  const hasPicker = schema.widget === 'select' || schema.widget === 'model' || schema.widget === 'tools' || schema.widget === 'picker'
  const normalizedValue = normalizeProtoFieldValue(schema, value)
  const resolvedCelContext = celContext === 'workflow' ? 'default' : celContext
  const isInlineCheckbox = schema.widget === 'checkbox' && typeof normalizedValue !== 'string'
  const normalizedStringValue = typeof normalizedValue === 'string' ? normalizedValue : ''
  const normalizedBooleanValue = typeof normalizedValue === 'boolean' ? normalizedValue : Boolean(normalizedValue)
  const modelStringValue = getModelStringValue(value)
  const numberStringValue = normalizeCelNumberString(value)
  const inputId = schema.key.replace(/\./g, '-')
  const hintId = inlineHint ? `${inputId}-hint` : undefined
  const { findings: fieldFindings, focusSeq } = useFieldFindings(schema.key)
  const fieldErrors = fieldFindings.filter((finding) => !finding.warning)
  const fieldRef = useRef<HTMLDivElement>(null)
  // Picking this field's problem in the problems list brings it into view.
  useEffect(() => {
    if (focusSeq === null || !fieldRef.current) return
    fieldRef.current.scrollIntoView?.({ block: 'center' })
    // The value control itself, not the Fixed/Expression toggle beside it.
    const root = fieldRef.current
    const control =
      root.querySelector<HTMLElement>(`#${CSS.escape(inputId)}`) ??
      root.querySelector<HTMLElement>('input, textarea, select, [contenteditable="true"]') ??
      root.querySelector<HTMLElement>('button:not([aria-pressed])')
    control?.focus({ preventScroll: true })
  }, [focusSeq, inputId])

  const supportsModeToggle = !hideCELToggle && schema.celCapable && !schema.celExpressionOnly && (
    (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number') ||
    ((schema.widget === 'select' || schema.widget === 'model' || schema.widget === 'tools' || schema.widget === 'picker') && schema.showCelModeToggle)
  )
  const options = useMemo(() => schema.options ?? [], [schema.options])
  const shouldForceCelMode = useMemo(() => {
    if (!supportsModeToggle) {
      return false
    }

    if (DEFAULT_CEL_REGEX.test(normalizedStringValue) || DEFAULT_CEL_REGEX.test(modelStringValue) || DEFAULT_CEL_REGEX.test(numberStringValue)) {
      return true
    }

    if (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number' || schema.widget === 'model' || schema.widget === 'picker') {
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

  // List-valued fields are stored as `{ values: [...] }` but edited as a
  // comma-separated string, and that conversion is lossy mid-edit: the
  // separator the user is still typing (", ") has no representation in the
  // stored array, so echoing the stored value back would erase it. Hold the
  // raw text while this field is being edited and show that instead.
  const isStringListField = schema.valueKind === 'stringList'
  const [listDraft, setListDraft] = useState<string | null>(null)
  const displayStringValue =
    isStringListField && listDraft !== null && splitStringList(listDraft).join(', ') === normalizedStringValue
      ? listDraft
      : normalizedStringValue

  const emitChange = (nextValue: string) => {
    if (isStringListField) {
      setListDraft(nextValue)
    }
    onChange(nextValue)
  }

  // Insert data: the expression input's own insertion target, and the plain
  // input a fixed text value is typed into.
  const insertRef = useRef<CELInsertTarget | null>(null)
  const plainInputRef = useRef<HTMLInputElement | HTMLTextAreaElement | null>(null)

  if (!isProtoFieldVisible(schema, context)) {
    return null
  }

  const isCelInput = schema.celExpressionOnly || (supportsModeToggle && useCelMode && (schema.widget === 'text' || schema.widget === 'textarea' || schema.widget === 'number'))
  const isBooleanExpression = schema.widget === 'checkbox' && typeof normalizedValue === 'string'
  const inExpressionMode = isCelInput || (supportsModeToggle && useCelMode) || isBooleanExpression
  const isFixedText = !inExpressionMode && (schema.widget === 'text' || schema.widget === 'textarea')
  // Insert data offers what an expression can read. A fixed text value can
  // take a {{ }} template too, so it gets the picker as well and switches to
  // Expression on insert; other fixed widgets switch first.
  const canInsertData = !hideCELToggle && !disabled && !!schema.celCapable && (inExpressionMode || (isFixedText && supportsModeToggle))
  const insertData = (path: string) => {
    if (inExpressionMode) {
      insertRef.current?.insert(path)
      return
    }
    const current = schema.widget === 'textarea' ? displayStringValue : normalizedStringValue
    const element = plainInputRef.current
    const start = element?.selectionStart ?? current.length
    const end = element?.selectionEnd ?? start
    setUseCelMode(true)
    emitChange(current.slice(0, start) + celInsertText(current, start, path, false) + current.slice(end))
  }
  const formatMismatch = !inExpressionMode && failsPattern(schema.pattern, normalizedStringValue)
  // A tools value is a comma-separated string from this widget, but a run
  // form hands an unset input its default as the list it is declared as
  // (["tag:coding:default"]), which must show as selected, not as nothing.
  const toolTokens = Array.isArray(value)
    ? value.map(String).filter(Boolean)
    : splitStringList(normalizedStringValue)

  const labelAction = (
    <div className="ml-auto flex items-center gap-1">
      {canInsertData && (
        <InsertDataMenu fieldLabel={schema.label} celContext={resolvedCelContext} onInsert={insertData} />
      )}
      {!hideCELToggle && schema.celCapable && !supportsModeToggle && (
        <span className="cpv2-cel-toggle active">CEL</span>
      )}
      {supportsModeToggle && !disabled && (
        <div role="group" aria-label={`${schema.label}: value mode`} className="cpv2-mode-group gap-[2px]">
          <button
            type="button"
            aria-pressed={!useCelMode}
            onClick={() => {
              setUseCelMode(false)
              if (schema.widget === 'select' && !canRenderAsSelectLiteral(normalizedStringValue, options)) {
                const fallbackOption = options[0]
                onChange(fallbackOption ? fallbackOption.value : '')
              }
            }}
            className={cn('cpv2-mode-pill cpv2-mode-pill-compact', !useCelMode && 'active')}
            title={hasPicker ? 'Pick a value' : 'Type a fixed value'}
          >
            {hasPicker ? <List className="w-3 h-3" aria-hidden /> : <ALargeSmall className="w-3 h-3" aria-hidden />}
            Fixed
          </button>
          <button
            type="button"
            aria-pressed={useCelMode}
            onClick={() => setUseCelMode(true)}
            className={cn('cpv2-mode-pill cpv2-mode-pill-compact', useCelMode && 'active')}
            title="Compute the value with a {{ }} expression"
          >
            <Braces className="w-3 h-3" aria-hidden />
            Expression
          </button>
        </div>
      )}
    </div>
  )

  const renderCelInput = (celValue: string, { multiline = false, placeholder = celPlaceholder }: { multiline?: boolean; placeholder?: string } = {}) => (
    <CELInput
      id={inputId}
      value={celValue}
      onChange={(nextValue) => emitChange(nextValue)}
      placeholder={placeholder}
      disabled={disabled}
      multiline={multiline}
      rows={multiline ? 3 : undefined}
      hideCELHint
      showCELIndicator={false}
      pureExpression={schema.celExpressionOnly}
      celContext={resolvedCelContext}
      currentNodeType={currentNodeType}
      insertRef={insertRef}
    />
  )

  return (
    <div
      ref={fieldRef}
      className={cn('space-y-1.5', fieldErrors.length > 0 && 'cpv2-field--invalid', className)}
      data-field-key={schema.key}
    >
      {!isInlineCheckbox && (
        <div className="cpv2-field-label">
          <span className="flex items-center gap-1.5">
            <label htmlFor={inputId}>{schema.label}</label>
            {schema.typeHint && <span className="cpv2-field-type">{schema.typeHint}</span>}
            {popoverText && <HelpPopover content={popoverText} title={schema.label} />}
          </span>
          {labelAction}
        </div>
      )}

      {schema.widget === 'text' &&
        (isCelInput ? (
          renderCelInput(normalizedStringValue)
        ) : (
          <input
            ref={(element) => { plainInputRef.current = element }}
            id={inputId}
            value={normalizedStringValue}
            onChange={(event) => onChange(event.target.value)}
            placeholder={literalPlaceholder}
            aria-describedby={hintId}
            aria-invalid={formatMismatch || undefined}
            disabled={disabled}
            className="cpv2-field-input"
          />
        ))}

      {schema.widget === 'textarea' &&
        (isCelInput ? (
          renderCelInput(displayStringValue, { multiline: true })
        ) : (
          <textarea
            ref={(element) => { plainInputRef.current = element }}
            id={inputId}
            value={displayStringValue}
            onChange={(event) => emitChange(event.target.value)}
            placeholder={literalPlaceholder}
            aria-describedby={hintId}
            disabled={disabled}
            rows={3}
            className="cpv2-field-textarea"
          />
        ))}

      {schema.widget === 'select' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(normalizedStringValue)}
        </div>
      ) : (
        <select
          id={inputId}
          value={normalizedStringValue}
          onChange={(event) => onChange(event.target.value)}
          aria-describedby={hintId}
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

      {schema.widget === 'picker' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(normalizedStringValue)}
        </div>
      ) : (
        <OptionPicker
          id={inputId}
          value={normalizedStringValue}
          onChange={(next) => onChange(next)}
          options={options}
          placeholder={defaultLabel ? `Default: ${defaultLabel}` : `Select ${schema.label.replace(/\s*\*$/, '').toLowerCase()}…`}
          manualPlaceholder={schema.example ? `e.g. ${schema.example}` : undefined}
          searchPlaceholder={`Search ${schema.label.replace(/\s*\*$/, '').toLowerCase()}`}
          disabled={disabled}
          describedBy={hintId}
        />
      ))}

      {schema.widget === 'model' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(modelStringValue)}
        </div>
      ) : (
        <ModelDropdown
          value={modelStringValue ? { id: modelStringValue } : value as Parameters<typeof ModelDropdown>[0]['value']}
          onChange={(nextModel) => onChange(extractModelId(nextModel))}
          disabled={disabled}
          placeholder={schema.placeholder ?? (defaultLabel ? `Default: ${defaultLabel}` : schema.example ? `e.g. ${schema.example}` : undefined)}
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
          placeholder={literalPlaceholder}
          aria-describedby={hintId}
          disabled={disabled}
          className="cpv2-field-input"
        />
      ))}

      {schema.widget === 'tools' && (supportsModeToggle && useCelMode ? (
        <div className="border-l-2 border-primary/30 pl-2">
          {renderCelInput(normalizedStringValue)}
        </div>
      ) : (
        <>
          <ToolsSelector
            value={toolTokens}
            onChange={(tools) => onChange(tools.join(', '))}
            disabled={disabled}
            hideLabel
          />
          {/* The selector has no empty-state text of its own. */}
          {toolTokens.length === 0 && defaultLabel && (
            <p className="cpv2-field-hint !mt-0">Default: {defaultLabel}</p>
          )}
        </>
      ))}

      {schema.widget === 'checkbox' && (
        typeof normalizedValue === 'string' ? (
          <div className="border-l-2 border-primary/30 pl-2">
            {renderCelInput(normalizedValue)}
          </div>
        ) : (
          <div className="space-y-1">
            <div className="cpv2-field-inline py-1.5">
              <div className="flex items-center gap-2 min-w-0">
                <span className="cpv2-fi-label">
                  {schema.label}
                </span>
                {popoverText && (
                  <HelpPopover content={popoverText} title={schema.label} />
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

      {formatMismatch && (
        <p className="cpv2-field-hint !mt-0 flex items-start gap-1.5 text-warning-ink">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden />
          <span>
            This doesn't look like a {schema.label.replace(/\s*\*$/, '')}
            {schema.example ? <>. Expected something like <code className="font-mono">{schema.example}</code>.</> : '.'}
          </span>
        </p>
      )}

      {inlineHint && (
        <p id={hintId} className="cpv2-field-hint !mt-0">
          {inlineHint}
        </p>
      )}

      {/* Validation of the canvas, on the field it is about (WorkflowFindingsContext). */}
      {fieldFindings.map((finding, index) => (
        <p
          key={index}
          role={finding.warning ? undefined : 'alert'}
          className={cn('cpv2-field-error !mt-0', finding.warning && 'cpv2-field-error--warning')}
        >
          {finding.text}
          {finding.suggestion && <span className="block opacity-80">{finding.suggestion}</span>}
        </p>
      ))}
    </div>
  )
}

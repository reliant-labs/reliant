import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import type { NodeThreadConfig, ThreadMode } from '../../types/workflow'
import { HelpPopover } from '../ui/HelpPopover'
import { ProtoFieldRenderer } from './ProtoFieldRenderer'
import type { ProtoFieldSchema } from '../../types/workflowFieldSchema'
import {
  normalizeProtoFieldValue,
  shouldOmitProtoFieldValue,
} from '../../types/workflowFieldSchema'


interface NodeThreadConfigEditorProps {
  config?: NodeThreadConfig
  onChange: (config: NodeThreadConfig | undefined) => void
  /** Whether this node is inside a loop (shows memo option) */
  isInLoop?: boolean
  /** Whether the editor is in read-only mode */
  isReadOnly?: boolean
  /** 'collapsible' (default) wraps in bordered collapsible box; 'flat' renders fields directly */
  variant?: 'collapsible' | 'flat'
  /** When variant='flat', which sub-section to render: 'thread' for thread fields, 'inject' for inject fields */
  section?: 'thread' | 'inject'
}

const THREAD_MODE_OPTIONS: Array<{ value: ThreadMode; label: string }> = [
  { value: 'inherit', label: "Inherit — Use parent's thread (default)" },
  { value: 'new', label: 'New — Create a new empty thread' },
  { value: 'fork', label: 'Fork — Copy parent thread, then diverge' },
]

const THREAD_FIELD_SCHEMAS: ProtoFieldSchema[] = [
  {
    key: 'mode',
    label: 'Thread Mode',
    widget: 'select',
    valueKind: 'string',
    celCapable: true,
    showCelModeToggle: true,
    options: THREAD_MODE_OPTIONS,
    defaultValue: 'inherit',
    omitIfDefault: true,
    placeholder: '{{input.isolated ? "new" : "inherit"}}',
    helpText: 'Controls what thread context is passed to the child workflow',
  },
  {
    key: 'memo',
    label: 'Memoize thread across iterations',
    widget: 'checkbox',
    valueKind: 'boolean',
    defaultValue: true,
    omitIfDefault: true,
    helpText:
      'Same thread is reused across loop iterations (conversation continues). Disable for fresh thread per iteration.',
    isVisible: (context) => context.mode !== 'inherit',
  },
]

type ThreadFieldKey = 'mode' | 'memo'

const INJECT_FIELD_SCHEMAS: ProtoFieldSchema[] = [
  {
    key: 'inject.role',
    label: 'Role',
    widget: 'select',
    valueKind: 'string',
    options: [
      { value: 'user', label: 'User' },
      { value: 'assistant', label: 'Assistant' },
      { value: 'system', label: 'System' },
    ],
    defaultValue: 'user',
    omitIfDefault: true,
    helpText: 'Role of the injected message (user, assistant, or system)',
  },
  {
    key: 'inject.content',
    label: 'Content',
    widget: 'textarea',
    valueKind: 'string',
    celCapable: true,
    placeholder: 'Enter message content or CEL expression...',
    omitIfEmpty: true,
    helpText: 'Message content. Use {{expression}} for dynamic values like {{output.text}}',
  },
  {
    key: 'inject.displayStyle',
    label: 'Display Style',
    widget: 'select',
    valueKind: 'string',
    options: [
      { value: 'hidden', label: 'Hidden — sent to LLM only' },
      { value: 'info', label: 'Info' },
      { value: 'warning', label: 'Warning' },
      { value: 'success', label: 'Success' },
    ],
    defaultValue: 'hidden',
    omitIfDefault: true,
    helpText: 'Controls how the injected message appears in the transcript. Hidden keeps prompt context out of the UI.',
  },
]

function getNestedConfigValue(config: NodeThreadConfig | undefined, path: string): unknown {
  if (!config) {
    return undefined
  }

  const [rootKey, nestedKey] = path.split('.')
  if (!nestedKey) {
    return config[rootKey as keyof NodeThreadConfig]
  }

  const rootValue = config[rootKey as keyof NodeThreadConfig]
  if (!rootValue || typeof rootValue !== 'object') {
    return undefined
  }

  return (rootValue as Record<string, unknown>)[nestedKey]
}

function compactThreadConfig(config: NodeThreadConfig | undefined): NodeThreadConfig | undefined {
  const nextConfig: Record<string, unknown> = {}

  for (const schema of THREAD_FIELD_SCHEMAS) {
    const fieldKey = schema.key as ThreadFieldKey
    const normalizedValue = normalizeProtoFieldValue(schema, config?.[fieldKey])

    if (shouldOmitProtoFieldValue(schema, normalizedValue)) {
      continue
    }

    if (typeof normalizedValue === 'string' || typeof normalizedValue === 'boolean') {
      nextConfig[fieldKey] = normalizedValue
    }
  }

  const injectConfig: Record<string, unknown> = {}

  for (const schema of INJECT_FIELD_SCHEMAS) {
    const [, nestedKey] = schema.key.split('.')
    const normalizedValue = normalizeProtoFieldValue(schema, getNestedConfigValue(config, schema.key))

    if (shouldOmitProtoFieldValue(schema, normalizedValue)) {
      continue
    }

    if (nestedKey && (typeof normalizedValue === 'string' || typeof normalizedValue === 'boolean')) {
      injectConfig[nestedKey] = normalizedValue
    }
  }

  if (Object.keys(injectConfig).length > 0) {
    nextConfig.inject = injectConfig
  }

  return Object.keys(nextConfig).length > 0 ? (nextConfig as NodeThreadConfig) : undefined
}

export function NodeThreadConfigEditor({
  config,
  onChange,
  isInLoop = false,
  isReadOnly = false,
  variant = 'collapsible',
  section,
}: NodeThreadConfigEditorProps) {
  const compactConfig = useMemo(() => compactThreadConfig(config), [config])

  const mode = compactConfig?.mode || 'inherit'
  const hasCustomConfig = !!compactConfig && (mode !== 'inherit' || !!compactConfig.inject?.content)

  const [isExpanded, setIsExpanded] = useState(hasCustomConfig)
  const [isInjectExpanded, setIsInjectExpanded] = useState(!!compactConfig?.inject?.content)

  useEffect(() => {
    if (hasCustomConfig) {
      setIsExpanded(true)
    }
  }, [hasCustomConfig])

  useEffect(() => {
    if (compactConfig?.inject?.content) {
      setIsInjectExpanded(true)
    }
  }, [compactConfig?.inject?.content])

  const threadFieldContext = {
    isInLoop,
    mode,
  }

  const updateField = (schemaKey: string, value: unknown) => {
    const nextConfig: NodeThreadConfig = {
      ...(config ?? {}),
    }

    if (schemaKey.startsWith('inject.')) {
      const [, nestedKey] = schemaKey.split('.')
      const nextInject: Record<string, unknown> = {
        ...(nextConfig.inject ?? {}),
      }
      nextInject[nestedKey] = value

      nextConfig.inject = nextInject as NonNullable<NodeThreadConfig['inject']>
    } else {
      const threadFieldKey = schemaKey as ThreadFieldKey
      nextConfig[threadFieldKey] = value as never
    }

    onChange(compactThreadConfig(nextConfig))
  }

  const clearInjectConfig = () => {
    const nextConfig: NodeThreadConfig = {
      ...(config ?? {}),
    }

    delete nextConfig.inject
    onChange(compactThreadConfig(nextConfig))
  }

  const hasInjectConfig = !!compactConfig?.inject?.content

  const threadContent = (
    <div className="space-y-4">
      {THREAD_FIELD_SCHEMAS.map((schema) => {
        const fieldKey = schema.key as ThreadFieldKey
        const fieldValue = compactConfig?.[fieldKey]

        return (
          <ProtoFieldRenderer
            key={schema.key}
            schema={schema}
            value={fieldValue}
            onChange={(value) => updateField(schema.key, value)}
            context={threadFieldContext}
            disabled={isReadOnly}
            celContext="thread"
          />
        )
      })}

      <div className="border-t border-border/50 pt-3">
        <button
          type="button"
          onClick={() => setIsInjectExpanded(!isInjectExpanded)}
          className="w-full flex items-center gap-2 py-1.5 text-sm font-medium text-foreground hover:text-foreground/80 transition-colors"
        >
          {isInjectExpanded ? (
            <ChevronDown className="w-4 h-4 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-4 h-4 text-muted-foreground" />
          )}
          <span>Inject Message</span>
          <HelpPopover
            content={
              isInLoop
                ? 'Adds a message to the thread BEFORE node execution.\n\nIn loops:\n• Inherit mode: First iteration only\n• New/Fork + memo (default): First iteration only\n• New/Fork + memo:false: Every iteration (fresh thread each time)'
                : 'Adds a message to the thread BEFORE node execution.\n\n• Inherit: Added on first execution only\n• New: First message in the new thread\n• Fork: Added after copied messages'
            }
          />
          {hasInjectConfig && (
            <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">configured</span>
          )}
        </button>

        {isInjectExpanded && (
          <div className="pt-2 space-y-3">
            {INJECT_FIELD_SCHEMAS.map((schema) => (
              <ProtoFieldRenderer
                key={schema.key}
                schema={schema}
                value={getNestedConfigValue(compactConfig, schema.key)}
                onChange={(value) => updateField(schema.key, value)}
                context={threadFieldContext}
                disabled={isReadOnly}
                celContext="thread"
              />
            ))}

            {hasInjectConfig && !isReadOnly && (
              <button
                type="button"
                onClick={clearInjectConfig}
                className="text-xs text-muted-foreground hover:text-destructive transition-colors"
              >
                Clear inject message
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  )

  const badgeParts: string[] = []
  if (mode !== 'inherit') {
    badgeParts.push(mode)
  }
  if (hasInjectConfig) {
    badgeParts.push('inject')
  }
  const badgeText = badgeParts.length > 0 ? badgeParts.join(' \u00b7 ') : null

  // Flat variant: render only the requested section without any wrapper
  if (variant === 'flat') {
    if (section === 'inject') {
      return (
        <div className="space-y-3">
          {INJECT_FIELD_SCHEMAS.map((schema) => (
            <ProtoFieldRenderer
              key={schema.key}
              schema={schema}
              value={getNestedConfigValue(compactConfig, schema.key)}
              onChange={(value) => updateField(schema.key, value)}
              context={threadFieldContext}
              disabled={isReadOnly}
              celContext="thread"
            />
          ))}

          {hasInjectConfig && !isReadOnly && (
            <button
              type="button"
              onClick={clearInjectConfig}
              className="text-xs text-muted-foreground hover:text-destructive transition-colors"
            >
              Clear inject message
            </button>
          )}
        </div>
      )
    }

    // section === 'thread' or default
    return (
      <div className="space-y-4">
        {THREAD_FIELD_SCHEMAS.map((schema) => {
          const fieldKey = schema.key as ThreadFieldKey
          const fieldValue = compactConfig?.[fieldKey]

          return (
            <ProtoFieldRenderer
              key={schema.key}
              schema={schema}
              value={fieldValue}
              onChange={(value) => updateField(schema.key, value)}
              context={threadFieldContext}
              disabled={isReadOnly}
              celContext="thread"
            />
          )
        })}
      </div>
    )
  }

  // Collapsible variant (default)
  return (
    <div>
      <button
        type="button"
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center gap-2 py-1.5 text-sm font-medium text-foreground hover:text-foreground/80 transition-colors"
      >
        {isExpanded ? (
          <ChevronDown className="w-4 h-4 text-muted-foreground" />
        ) : (
          <ChevronRight className="w-4 h-4 text-muted-foreground" />
        )}
        <span>Thread Configuration</span>
        {badgeText && (
          <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">
            {badgeText}
          </span>
        )}
      </button>

      {isExpanded && <div className="pt-2">{threadContent}</div>}
    </div>
  )
}

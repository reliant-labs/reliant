import { useEffect, useMemo, useState } from 'react'
import { MessageSquarePlus, ChevronDown, ChevronRight } from 'lucide-react'
import type { SaveMessageConfig } from '../../types/workflow'
import { HelpPopover } from '../ui/HelpPopover'
import { ProtoFieldRenderer } from './ProtoFieldRenderer'
import type { ProtoFieldSchema } from '../../types/workflowFieldSchema'
import {
  normalizeProtoFieldValue,
  shouldOmitProtoFieldValue,
} from '../../types/workflowFieldSchema'
import { normalizeCelString } from '../../lib/celAdapter'

interface SaveMessageConfigEditorProps {
  config?: SaveMessageConfig
  onChange: (config: SaveMessageConfig | undefined) => void
  /** Always show expanded without collapsible header */
  alwaysExpanded?: boolean
  /** Whether the editor is in read-only mode */
  isReadOnly?: boolean
  /** Whether this node is inside a loop (affects help text) */
  isInLoop?: boolean
  /** 'collapsible' (default) wraps in bordered collapsible box; 'flat' renders fields directly */
  variant?: 'collapsible' | 'flat'
  /** Current node type — used for output.* completions */
  currentNodeType?: string
}

const ROLE_OPTIONS = [
  { value: 'user', label: 'User' },
  { value: 'assistant', label: 'Assistant' },
  { value: 'tool', label: 'Tool' },
  { value: 'system', label: 'System' },
] as const

type SaveMessageFieldKey = 'role' | 'content' | 'toolCalls' | 'toolResults' | 'condition'

const SAVE_MESSAGE_FIELD_SCHEMAS: ProtoFieldSchema[] = [
  {
    key: 'role',
    label: 'Message Role',
    widget: 'select',
    valueKind: 'string',
    celCapable: true,
    showCelModeToggle: true,
    options: [...ROLE_OPTIONS],
    defaultValue: 'user',
    omitIfDefault: true,
    helpText: 'Role of the saved message (user, assistant, tool, or system)',
  },
  {
    key: 'content',
    label: 'Content',
    widget: 'textarea',
    valueKind: 'string',
    celCapable: true,
    helpText: 'Message content. Use {{expression}} for dynamic values like {{output.text}}',
    placeholder: 'Message content with {{output.text}} expressions...',
    omitIfEmpty: true,
  },
  {
    key: 'toolCalls',
    label: 'Tool Calls',
    widget: 'text',
    valueKind: 'string',
    celCapable: true,
    helpText: 'Reference tool calls array with {{output.tool_calls}}. Only used for assistant role.',
    placeholder: '{{output.tool_calls}}',
    omitIfEmpty: true,
    isVisible: (context) => context.role === 'assistant' || context.roleIsDynamic === true,
  },
  {
    key: 'toolResults',
    label: 'Tool Results',
    widget: 'text',
    valueKind: 'string',
    celCapable: true,
    helpText: 'Reference tool results array with {{output.tool_results}}. Only used for tool role.',
    placeholder: '{{output.tool_results}}',
    omitIfEmpty: true,
    isVisible: (context) => context.role === 'tool' || context.roleIsDynamic === true,
  },
  {
    key: 'condition',
    label: 'Condition',
    widget: 'text',
    valueKind: 'string',
    celCapable: true,
    helpText: 'Only save if this CEL expression evaluates to true',
    placeholder: '{{output.text != ""}}',
    omitIfEmpty: true,
  },
]

const CEL_WRAPPER_REGEX = /\{\{[\s\S]*\}\}/

function isStandardRole(value: string): boolean {
  if (!value) {
    return true
  }

  return ROLE_OPTIONS.some((option) => option.value === value)
}

function compactSaveMessageConfig(config: SaveMessageConfig | undefined): SaveMessageConfig | undefined {
  const nextConfig: Record<string, unknown> = {}

  for (const schema of SAVE_MESSAGE_FIELD_SCHEMAS) {
    const fieldKey = schema.key as SaveMessageFieldKey
    const rawValue = config?.[fieldKey]
    const normalizedValue = normalizeProtoFieldValue(schema, rawValue)

    if (shouldOmitProtoFieldValue(schema, normalizedValue)) {
      continue
    }

    if (typeof normalizedValue === 'string' || typeof normalizedValue === 'boolean') {
      nextConfig[fieldKey] = normalizedValue
    }
  }

  return Object.keys(nextConfig).length > 0 ? (nextConfig as SaveMessageConfig) : undefined
}

export function SaveMessageConfigEditor({
  config,
  onChange,
  alwaysExpanded = false,
  isReadOnly = false,
  isInLoop = false,
  variant = 'collapsible',
  currentNodeType,
}: SaveMessageConfigEditorProps) {
  const compactConfig = useMemo(() => compactSaveMessageConfig(config), [config])
  const hasConfig = !!compactConfig
  const [isExpanded, setIsExpanded] = useState<boolean>(alwaysExpanded || hasConfig)

  useEffect(() => {
    if (alwaysExpanded) {
      setIsExpanded(true)
    }
  }, [alwaysExpanded])

  useEffect(() => {
    if (!alwaysExpanded && hasConfig) {
      setIsExpanded(true)
    }
  }, [alwaysExpanded, hasConfig])

  const role = normalizeCelString(compactConfig?.role, 'user')
  const roleIsDynamic = !isStandardRole(role) || CEL_WRAPPER_REGEX.test(role)

  const fieldContext = {
    isInLoop,
    role,
    roleIsDynamic,
  }

  const updateField = (fieldKey: SaveMessageFieldKey, value: unknown) => {
    const nextConfig: SaveMessageConfig = {
      ...(config ?? {}),
      [fieldKey]: value,
    }

    onChange(compactSaveMessageConfig(nextConfig))
  }

  const clearConfig = () => {
    onChange(undefined)
    if (!alwaysExpanded) {
      setIsExpanded(false)
    }
  }

  const configContent = (
    <div className="space-y-3">
      {SAVE_MESSAGE_FIELD_SCHEMAS.map((fieldSchema) => {
        const fieldKey = fieldSchema.key as SaveMessageFieldKey
        const fieldValue = compactConfig?.[fieldKey]

        return (
          <ProtoFieldRenderer
            key={fieldSchema.key}
            schema={fieldSchema}
            value={fieldValue}
            onChange={(value) => updateField(fieldKey, value)}
            context={fieldContext}
            disabled={isReadOnly}
            celContext="save_message"
            currentNodeType={currentNodeType}
          />
        )
      })}

      {hasConfig && !isReadOnly && (
        <div className="pt-2">
          <button
            type="button"
            onClick={clearConfig}
            className="text-xs text-muted-foreground hover:text-destructive transition-colors"
          >
            Clear save_message configuration
          </button>
        </div>
      )}
    </div>
  )

  const helpContent = isInLoop
    ? 'Saves a message to the thread AFTER node execution completes.\n\nIn loops: Runs on EVERY iteration.\nUse the Condition field to skip saving on specific iterations.'
    : 'Saves a message to the thread AFTER node execution completes. Use Condition to conditionally skip saving.'

  // Flat variant: render fields directly without any wrapper or header
  if (variant === 'flat') {
    return configContent
  }

  if (alwaysExpanded) {
    return (
      <div className="space-y-4">
        <div className="flex items-center gap-2 text-sm font-medium text-foreground">
          <MessageSquarePlus className="w-4 h-4" />
          <span>Save Message</span>
          <HelpPopover content={helpContent} />
          {hasConfig && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">configured</span>
          )}
        </div>
        {configContent}
      </div>
    )
  }

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
        <span>Save Message</span>
        <HelpPopover content={helpContent} />
        {hasConfig && (
          <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">configured</span>
        )}
      </button>

      {isExpanded && <div className="pt-2 space-y-3">{configContent}</div>}
    </div>
  )
}

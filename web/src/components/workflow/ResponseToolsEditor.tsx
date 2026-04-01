/**
 * ResponseToolsEditor - Editor for a single response tool in CallLLM steps
 *
 * Response tools use JSON Schema format for structured LLM outputs.
 *
 * Two modes are available:
 * - Simple Mode: Creates a choice/value schema with enum options
 * - Advanced Mode: Direct JSON Schema editing for custom schemas
 */

import { useState, useEffect } from 'react'
import { Plus, Trash2, ChevronDown, ChevronRight, Code, List } from 'lucide-react'
import { HelpPopover } from '../ui/HelpPopover'
import type { ResponseToolDefinition } from '../../types/workflow'

interface ResponseToolsEditorProps {
  tool: ResponseToolDefinition | null
  onChange: (tool: ResponseToolDefinition | null) => void
  isReadOnly?: boolean
}

// Fixed tool name for simplicity
const RESPONSE_TOOL_NAME = 'response'

type EditorMode = 'simple' | 'advanced'

// Helper to check if a schema is in the simple choice/value format
function isSimpleChoiceValueSchema(schema: Record<string, unknown>): boolean {
  if (schema.type !== 'object') return false
  const properties = schema.properties as Record<string, unknown> | undefined
  if (!properties) return false

  const choice = properties.choice as Record<string, unknown> | undefined
  const value = properties.value as Record<string, unknown> | undefined

  if (!choice || !value) return false
  if (choice.type !== 'string' || value.type !== 'string') return false
  if (!Array.isArray(choice.enum)) return false

  return true
}

// Extract options from a simple choice/value schema
function extractOptionsFromSchema(schema: Record<string, unknown>): Record<string, string> {
  const properties = schema.properties as Record<string, unknown> | undefined
  if (!properties) return {}

  const choice = properties.choice as Record<string, unknown> | undefined
  if (!choice || !Array.isArray(choice.enum)) return {}

  const enumValues = choice.enum as string[]
  const description = (choice.description as string) || ''

  // Parse the description to extract individual option descriptions
  // Format: "Choose one:\n- option1: description1\n- option2: description2"
  const options: Record<string, string> = {}
  const lines = description.split('\n')

  for (const enumValue of enumValues) {
    // Look for a matching line like "- option1: description1"
    const matchingLine = lines.find(line => {
      const match = line.match(/^-\s*([^:]+):\s*(.+)$/)
      return match && match[1].trim() === enumValue
    })

    if (matchingLine) {
      const match = matchingLine.match(/^-\s*([^:]+):\s*(.+)$/)
      options[enumValue] = match ? match[2].trim() : ''
    } else {
      options[enumValue] = ''
    }
  }

  return options
}

// Build a choice/value schema from options
function buildChoiceValueSchema(options: Record<string, string>): Record<string, unknown> {
  const enumValues = Object.keys(options)
  const choiceDescription = enumValues.length > 0
    ? 'Choose one:\n' + enumValues.map(key => `- ${key}: ${options[key]}`).join('\n')
    : 'Choose one of the available options'

  return {
    type: 'object',
    required: ['choice', 'value'],
    properties: {
      choice: {
        type: 'string',
        enum: enumValues,
        description: choiceDescription,
      },
      value: {
        type: 'string',
        description: 'Explanation or data for your choice',
      },
    },
  }
}

// Default simple schema
function createDefaultSimpleSchema(): Record<string, unknown> {
  return buildChoiceValueSchema({
    option_1: 'First option - describe when to choose this',
    option_2: 'Second option - describe when to choose this',
  })
}

export function ResponseToolsEditor({ tool, onChange, isReadOnly = false }: ResponseToolsEditorProps) {
  const [isExpanded, setIsExpanded] = useState(!!tool)

  // Determine initial mode based on schema format
  const [mode, setMode] = useState<EditorMode>(() => {
    if (!tool?.schema) return 'simple'
    return isSimpleChoiceValueSchema(tool.schema) ? 'simple' : 'advanced'
  })

  // For simple mode, extract options from schema
  const [simpleOptions, setSimpleOptions] = useState<Record<string, string>>(() => {
    if (!tool?.schema) return {}
    if (isSimpleChoiceValueSchema(tool.schema)) {
      return extractOptionsFromSchema(tool.schema)
    }
    return {}
  })

  // For advanced mode, keep the JSON string
  const [schemaJson, setSchemaJson] = useState<string>(() => {
    if (!tool?.schema) return ''
    return JSON.stringify(tool.schema, null, 2)
  })
  const [jsonError, setJsonError] = useState<string | null>(null)

  // Update state when tool changes externally
  useEffect(() => {
    if (tool?.schema) {
      const isSimple = isSimpleChoiceValueSchema(tool.schema)
      if (isSimple && mode === 'simple') {
        setSimpleOptions(extractOptionsFromSchema(tool.schema))
      }
      setSchemaJson(JSON.stringify(tool.schema, null, 2))
    }
  }, [tool?.schema, mode])

  const addTool = () => {
    const newTool: ResponseToolDefinition = {
      name: RESPONSE_TOOL_NAME,
      description: 'Provide your response using one of the available options',
      schema: createDefaultSimpleSchema(),
    }
    onChange(newTool)
    setIsExpanded(true)
    setMode('simple')
    setSimpleOptions({
      option_1: 'First option - describe when to choose this',
      option_2: 'Second option - describe when to choose this',
    })
    setSchemaJson(JSON.stringify(newTool.schema, null, 2))
    setJsonError(null)
  }

  const removeTool = () => {
    onChange(null)
    setIsExpanded(false)
  }

  const updateTool = (updates: Partial<ResponseToolDefinition>) => {
    if (!tool) return
    // Always ensure the name is fixed
    onChange({ ...tool, ...updates, name: RESPONSE_TOOL_NAME })
  }

  // Simple mode: add option
  const addOption = () => {
    const optionNum = Object.keys(simpleOptions).length + 1
    const newOptions = {
      ...simpleOptions,
      [`option_${optionNum}`]: 'Describe when to choose this option',
    }
    setSimpleOptions(newOptions)
    updateTool({ schema: buildChoiceValueSchema(newOptions) })
  }

  // Simple mode: update option
  const updateOption = (oldKey: string, newKey: string, newValue: string) => {
    const newOptions: Record<string, string> = {}
    for (const [key, value] of Object.entries(simpleOptions)) {
      if (key === oldKey) {
        newOptions[newKey || oldKey] = newValue
      } else {
        newOptions[key] = value
      }
    }
    setSimpleOptions(newOptions)
    updateTool({ schema: buildChoiceValueSchema(newOptions) })
  }

  // Simple mode: remove option
  const removeOption = (key: string) => {
    const newOptions = { ...simpleOptions }
    delete newOptions[key]
    setSimpleOptions(newOptions)
    updateTool({ schema: buildChoiceValueSchema(newOptions) })
  }

  // Advanced mode: update schema JSON
  const updateSchemaJson = (json: string) => {
    setSchemaJson(json)
    try {
      const parsed = JSON.parse(json)
      if (typeof parsed !== 'object' || parsed === null) {
        setJsonError('Schema must be a JSON object')
        return
      }
      setJsonError(null)
      updateTool({ schema: parsed })
    } catch (e) {
      setJsonError('Invalid JSON')
    }
  }

  // Switch between modes
  const switchMode = (newMode: EditorMode) => {
    if (newMode === mode) return

    if (newMode === 'simple') {
      // Try to extract options from current schema
      if (tool?.schema && isSimpleChoiceValueSchema(tool.schema)) {
        setSimpleOptions(extractOptionsFromSchema(tool.schema))
      } else {
        // Reset to default simple options
        const defaultOptions = {
          option_1: 'First option - describe when to choose this',
          option_2: 'Second option - describe when to choose this',
        }
        setSimpleOptions(defaultOptions)
        updateTool({ schema: buildChoiceValueSchema(defaultOptions) })
      }
    } else {
      // Advanced mode - keep current schema, update JSON display
      if (tool?.schema) {
        setSchemaJson(JSON.stringify(tool.schema, null, 2))
      }
    }
    setMode(newMode)
    setJsonError(null)
  }

  const optionCount = Object.keys(simpleOptions).length

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <label className="text-sm font-medium text-foreground flex items-center gap-1.5">
          Response Tool
          <HelpPopover
            title="Response Tool"
            content="Force the LLM to respond with structured data. Simple Mode creates a choice/value schema where the LLM picks an option and provides an explanation. Advanced Mode lets you write custom JSON Schema for any structure (objects, arrays, nested types)."
          />
        </label>
        {tool && !isReadOnly && (
          <button
            type="button"
            onClick={removeTool}
            className="text-xs text-muted-foreground hover:text-destructive transition-colors"
          >
            Remove
          </button>
        )}
      </div>

      {tool ? (
        <div className="border border-border rounded-md overflow-hidden">
          {/* Header */}
          <button
            type="button"
            onClick={() => setIsExpanded(!isExpanded)}
            className="w-full flex items-center gap-2 px-3 py-2 bg-muted/30 hover:bg-muted/50 transition-colors text-left"
          >
            {isExpanded ? (
              <ChevronDown className="w-4 h-4 text-muted-foreground flex-shrink-0" />
            ) : (
              <ChevronRight className="w-4 h-4 text-muted-foreground flex-shrink-0" />
            )}
            <span className="text-sm flex-1 truncate text-muted-foreground">
              {tool.description || 'Response tool configured'}
            </span>
            <span className="text-xs text-muted-foreground flex-shrink-0">
              {mode === 'simple' ? `${optionCount} ${optionCount === 1 ? 'option' : 'options'}` : 'JSON Schema'}
            </span>
          </button>

          {/* Body */}
          {isExpanded && (
            <div className="p-3 space-y-3 border-t border-border">
              {/* Description */}
              <div>
                <label className="block text-xs font-medium text-muted-foreground mb-1">
                  Description
                </label>
                <input
                  type="text"
                  value={tool.description}
                  onChange={(e) => updateTool({ description: e.target.value })}
                  className="w-full px-2 py-1.5 text-sm border border-input rounded bg-background disabled:opacity-60 disabled:cursor-not-allowed"
                  placeholder="Describe what this response tool is for..."
                  disabled={isReadOnly}
                />
              </div>

              {/* Mode Toggle */}
              {!isReadOnly && (
                <div className="flex gap-1 p-0.5 bg-muted/50 rounded-md w-fit">
                  <button
                    type="button"
                    onClick={() => switchMode('simple')}
                    className={`flex items-center gap-1.5 px-2.5 py-1 text-xs rounded transition-colors ${
                      mode === 'simple'
                        ? 'bg-background text-foreground shadow-sm'
                        : 'text-muted-foreground hover:text-foreground'
                    }`}
                  >
                    <List className="w-3 h-3" />
                    Simple
                  </button>
                  <button
                    type="button"
                    onClick={() => switchMode('advanced')}
                    className={`flex items-center gap-1.5 px-2.5 py-1 text-xs rounded transition-colors ${
                      mode === 'advanced'
                        ? 'bg-background text-foreground shadow-sm'
                        : 'text-muted-foreground hover:text-foreground'
                    }`}
                  >
                    <Code className="w-3 h-3" />
                    Advanced
                  </button>
                </div>
              )}

              {/* Simple Mode: Options Editor */}
              {mode === 'simple' && (
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-2">
                    Options
                    <span className="font-normal ml-1 text-muted-foreground/70">
                      (LLM will choose one)
                    </span>
                  </label>
                  <div className="space-y-2">
                    {Object.entries(simpleOptions).map(([key, value]) => (
                      <OptionRow
                        key={key}
                        optionKey={key}
                        optionValue={value}
                        onUpdate={(newKey, newValue) => updateOption(key, newKey, newValue)}
                        onRemove={() => removeOption(key)}
                        canRemove={optionCount > 1}
                        isReadOnly={isReadOnly}
                      />
                    ))}
                  </div>
                  {!isReadOnly && (
                    <button
                      type="button"
                      onClick={addOption}
                      className="mt-2 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                    >
                      <Plus className="w-3 h-3" />
                      Add Option
                    </button>
                  )}
                  <p className="mt-2 text-xs text-muted-foreground/70">
                    Output format: {'{ choice: "option_name", value: "explanation" }'}
                  </p>
                </div>
              )}

              {/* Advanced Mode: JSON Schema Editor */}
              {mode === 'advanced' && (
                <div>
                  <label className="block text-xs font-medium text-muted-foreground mb-1">
                    JSON Schema
                    <span className="font-normal ml-1 text-muted-foreground/70">
                      (defines the structure of LLM output)
                    </span>
                  </label>
                  <textarea
                    value={schemaJson}
                    onChange={(e) => updateSchemaJson(e.target.value)}
                    className={`w-full px-2 py-1.5 text-xs font-mono border rounded bg-background disabled:opacity-60 disabled:cursor-not-allowed resize-y min-h-[200px] ${
                      jsonError ? 'border-destructive' : 'border-input'
                    }`}
                    placeholder='{\n  "type": "object",\n  "properties": { ... }\n}'
                    disabled={isReadOnly}
                    spellCheck={false}
                  />
                  {jsonError && (
                    <p className="mt-1 text-xs text-destructive">{jsonError}</p>
                  )}
                  <p className="mt-2 text-xs text-muted-foreground/70">
                    Define a JSON Schema to structure LLM responses. The LLM will produce output matching this schema.
                  </p>
                </div>
              )}
            </div>
          )}
        </div>
      ) : (
        // No tool configured - show add button
        !isReadOnly && (
          <div className="space-y-1.5">
            <button
              type="button"
              onClick={addTool}
              className="w-full flex items-center justify-center gap-1.5 px-3 py-2 text-sm border border-dashed border-border rounded-md hover:bg-muted/50 transition-colors text-muted-foreground hover:text-foreground"
            >
              <Plus className="w-4 h-4" />
              Add Response Tool
            </button>
            <p className="text-xs text-muted-foreground text-center">
              Define a JSON Schema to get structured output from the LLM
            </p>
          </div>
        )
      )}

      {!tool && isReadOnly && (
        <p className="text-xs text-muted-foreground">
          No response tool configured.
        </p>
      )}
    </div>
  )
}

interface OptionRowProps {
  optionKey: string
  optionValue: string
  onUpdate: (newKey: string, newValue: string) => void
  onRemove: () => void
  canRemove: boolean
  isReadOnly?: boolean
}

function OptionRow({ optionKey, optionValue, onUpdate, onRemove, canRemove, isReadOnly = false }: OptionRowProps) {
  const [editingKey, setEditingKey] = useState(false)
  const [tempKey, setTempKey] = useState(optionKey)

  return (
    <div className="flex gap-2 items-start">
      {/* Option key (name) */}
      <div className="w-28 flex-shrink-0">
        {editingKey && !isReadOnly ? (
          <input
            type="text"
            value={tempKey}
            onChange={(e) => setTempKey(e.target.value.replace(/\s+/g, '_').toLowerCase())}
            onBlur={() => {
              if (tempKey && tempKey !== optionKey) {
                onUpdate(tempKey, optionValue)
              }
              setEditingKey(false)
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                if (tempKey && tempKey !== optionKey) {
                  onUpdate(tempKey, optionValue)
                }
                setEditingKey(false)
              }
              if (e.key === 'Escape') {
                setTempKey(optionKey)
                setEditingKey(false)
              }
            }}
            className="w-full px-2 py-1 text-xs border border-input rounded bg-background font-mono"
            autoFocus
          />
        ) : (
          <button
            type="button"
            onClick={() => !isReadOnly && setEditingKey(true)}
            className={`w-full px-2 py-1 text-xs font-mono text-left bg-muted/50 rounded truncate ${isReadOnly ? 'cursor-default' : 'hover:bg-muted'}`}
            title={isReadOnly ? optionKey : `Click to rename: ${optionKey}`}
            disabled={isReadOnly}
          >
            {optionKey}
          </button>
        )}
      </div>

      {/* Option value (description) */}
      <input
        type="text"
        value={optionValue}
        onChange={(e) => onUpdate(optionKey, e.target.value)}
        className="flex-1 px-2 py-1 text-xs border border-input rounded bg-background disabled:opacity-60 disabled:cursor-not-allowed"
        placeholder="Describe when to choose this option..."
        disabled={isReadOnly}
      />

      {/* Remove button */}
      {!isReadOnly && (
        <button
          type="button"
          onClick={onRemove}
          disabled={!canRemove}
          className="p-1 text-muted-foreground hover:text-destructive disabled:opacity-30 disabled:cursor-not-allowed flex-shrink-0"
          title={canRemove ? "Remove option" : "At least one option required"}
        >
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      )}
    </div>
  )
}

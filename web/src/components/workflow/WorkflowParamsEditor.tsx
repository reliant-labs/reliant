import { useState, useRef, useEffect } from 'react'
import { Plus, Trash2, ChevronDown, ChevronRight } from 'lucide-react'
import { ConfigurationPanel } from './ConfigurationPanel'
import { ToolsSelector } from './ToolsSelector'
import { presetGrpc, type Preset } from '../../api/preset-grpc'
import { useProjectStore } from '../../store/projectStore'
import { useModels } from '../../store/globalDataStore'
import { jsToProtoValue } from '../../api/proto-utils'
import { formatValueForDisplay } from '../../lib/paramUtils'
import type { Param } from '../../types/workflow'
import {
  getInputUI,
  getInputDefault,
  getInputEnumValues,
  getInputPattern,
  getInputMinLength,
  getInputMaxLength,
  getInputMinItems,
  getInputMaxItems,
  getInputProperties,
  getInputRequired,
  createInput,
  applyInputUpdates,
  type InputDef,
} from '../../lib/inputHelpers'

// Helper to get the JS default value from a param (reads through config oneof)
function getParamDefault(param: ParamLike): unknown {
  return getInputDefault(param as InputDef)
}

// Helper to convert a JS value to proto Value for the default field
function toDefaultValue(val: unknown): unknown {
  if (val === undefined || val === null || val === '') return undefined
  return jsToProtoValue(val)
}

// ParamLike is a looser type for functions that access proto Input fields via helpers.
// Param (MessageInitShape) lacks $typeName, so it can't be passed to helpers typed as Input.
// Casting through this alias keeps call sites readable.
type ParamLike = Param | ParamWithId

interface WorkflowParamsEditorProps {
  params: Record<string, Param>
  onUpdate: (params: Record<string, Param>) => void
  onClose: () => void
}

// Content-only props (no panel wrapper)
interface WorkflowParamsEditorContentProps {
  params: Record<string, Param>
  onUpdate: (params: Record<string, Param>) => void
}

// Semantic types for workflow parameters
// These determine how the parameter is rendered in the UI
const PARAM_TYPES = [
  // Basic types
  { value: 'string', label: 'String', group: 'Basic', description: 'Text input', hasDefault: true },
  { value: 'number', label: 'Number', group: 'Basic', description: 'Decimal number', hasDefault: true },
  { value: 'integer', label: 'Integer', group: 'Basic', description: 'Whole number', hasDefault: true },
  { value: 'boolean', label: 'Boolean', group: 'Basic', description: 'Toggle switch', hasDefault: true },
  { value: 'object', label: 'Object', group: 'Basic', description: 'Structured object with JSON Schema', hasDefault: true },
  
  // Selection types
  { value: 'enum', label: 'Enum', group: 'Selection', description: 'Dropdown with predefined options', hasDefault: true },
  { value: 'model', label: 'Model', group: 'Selection', description: 'Model selector dropdown', hasDefault: true },
  
  // Chat types
  { value: 'message', label: 'Message', group: 'Chat', description: 'Chat textarea (primary user input)', hasDefault: false },
  { value: 'attachments', label: 'Attachments', group: 'Chat', description: 'File picker (list)', hasDefault: false },
  
  // Configuration types
  { value: 'tools', label: 'Tools', group: 'Configuration', description: 'Tool multi-select (list)', hasDefault: true },
  { value: 'preset', label: 'Preset', group: 'Configuration', description: 'Preset picker', hasDefault: true },
  
  // Advanced
  { value: 'any', label: 'Any', group: 'Advanced', description: 'Generic input for complex types', hasDefault: true },
]

// Types that represent lists (empty array as implicit default)
const LIST_TYPES = ['tools', 'attachments']

// Internal representation with stable IDs
// Using intersection type because MessageInitShape can't be extended with interface
type ParamWithId = Param & {
  _id: string
  _name: string
}

let paramIdCounter = 0
function generateParamId(): string {
  return `param-${Date.now()}-${++paramIdCounter}`
}

// Shared hook for params state management
function useParamsState(params: Record<string, Param>, onUpdate: (params: Record<string, Param>) => void) {
  // Convert external params (keyed by name) to internal format (with stable IDs)
  const initialParamsRef = useRef<ParamWithId[] | null>(null)
  if (initialParamsRef.current === null) {
    initialParamsRef.current = Object.entries(params || {}).map(([name, param]) => ({
      ...param,
      _id: generateParamId(),
      _name: name,
    }))
  }
  
  const [localParams, setLocalParams] = useState<ParamWithId[]>(initialParamsRef.current)

  // Convert internal format back to external format for onUpdate
  const toExternalParams = (internalParams: ParamWithId[]): Record<string, Param> => {
    const result: Record<string, Param> = {}
    for (const p of internalParams) {
      const { _id, _name, ...param } = p
      result[_name] = param
    }
    return result
  }

  const updateParam = (id: string, updates: Record<string, unknown>) => {
    const newParams = localParams.map(p =>
      p._id === id ? applyInputUpdates(p as InputDef, updates) as ParamWithId : p
    )
    setLocalParams(newParams)
    onUpdate(toExternalParams(newParams))
  }

  const addParam = () => {
    // Generate unique name
    const existingNames = new Set(localParams.map(p => p._name))
    const baseName = 'param'
    let counter = 1
    while (existingNames.has(`${baseName}${counter}`)) {
      counter++
    }
    const name = `${baseName}${counter}`
    
    const newParam: ParamWithId = {
      ...createInput('string'),
      _id: generateParamId(),
      _name: name,
    }
    const newParams = [...localParams, newParam]
    setLocalParams(newParams)
    onUpdate(toExternalParams(newParams))
  }

  const removeParam = (id: string) => {
    const newParams = localParams.filter(p => p._id !== id)
    setLocalParams(newParams)
    onUpdate(toExternalParams(newParams))
  }

  const renameParam = (id: string, newName: string) => {
    const sanitizedName = newName.replace(/\s+/g, '_')
    // Check if name already exists (excluding the current param)
    const existingNames = new Set(localParams.filter(p => p._id !== id).map(p => p._name))
    if (existingNames.has(sanitizedName)) return // Don't overwrite existing
    
    updateParam(id, { _name: sanitizedName })
  }

  return { localParams, updateParam, addParam, removeParam, renameParam }
}

// Content-only version for embedding in other panels
export function WorkflowParamsEditorContent({ params, onUpdate }: WorkflowParamsEditorContentProps) {
  const { localParams, updateParam, addParam, removeParam, renameParam } = useParamsState(params, onUpdate)

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Define parameters for this workflow.
        Access in CEL as <code className="font-mono bg-muted px-1 rounded">inputs.&lt;name&gt;</code>
      </p>

      {localParams.length === 0 && (
        <div className="text-sm text-muted-foreground italic py-4 bg-muted rounded px-3 text-center">
          No parameters defined. Click "Add Parameter" to create one.
        </div>
      )}

      {localParams.map((param) => (
        <ParamEditor
          key={param._id}
          param={param}
          onUpdate={(updates) => updateParam(param._id, updates)}
          onRename={(newName) => renameParam(param._id, newName)}
          onRemove={() => removeParam(param._id)}
        />
      ))}

      <button
        onClick={addParam}
        className="flex items-center gap-1.5 px-3 py-2 text-sm text-primary border border-primary/40 rounded-md hover:bg-primary/10 transition-colors w-full justify-center"
      >
        <Plus className="w-4 h-4" />
        Add Parameter
      </button>

      {/* CEL Reference */}
      <div className="bg-muted/40 border border-border rounded p-3">
        <p className="text-xs font-medium text-foreground mb-2">Using Parameters in CEL</p>
        <div className="text-xs font-mono text-muted-foreground space-y-1">
          <div>inputs.&lt;name&gt; - access parameter value</div>
          <div>inputs.branch_name - example: string param</div>
          <div>inputs.max_retries - example: integer param</div>
        </div>
      </div>
    </div>
  )
}

// Full panel version (legacy, still used by standalone params editor)
export function WorkflowParamsEditor({ params, onUpdate, onClose }: WorkflowParamsEditorProps) {
  const { localParams, updateParam, addParam, removeParam, renameParam } = useParamsState(params, onUpdate)

  return (
    <ConfigurationPanel
      title="Workflow Parameters"
      subtitle={`${localParams.length} parameter${localParams.length !== 1 ? 's' : ''}`}
      onClose={onClose}
    >
      <div className="space-y-4">
        <p className="text-xs text-muted-foreground">
          Define parameters for this workflow.
          Access in CEL as <code className="font-mono bg-muted px-1 rounded">inputs.&lt;name&gt;</code>
        </p>

        {localParams.length === 0 && (
          <div className="text-sm text-muted-foreground italic py-4 bg-muted rounded px-3 text-center">
            No parameters defined. Click "Add Parameter" to create one.
          </div>
        )}

        {localParams.map((param) => (
          <ParamEditor
            key={param._id}
            param={param}
            onUpdate={(updates) => updateParam(param._id, updates)}
            onRename={(newName) => renameParam(param._id, newName)}
            onRemove={() => removeParam(param._id)}
          />
        ))}

        <button
          onClick={addParam}
          className="flex items-center gap-1.5 px-3 py-2 text-sm text-primary border border-primary/40 rounded-md hover:bg-primary/10 transition-colors w-full justify-center"
        >
          <Plus className="w-4 h-4" />
          Add Parameter
        </button>

        {/* CEL Reference */}
        <div className="bg-muted/40 border border-border rounded p-3">
          <p className="text-xs font-medium text-foreground mb-2">Using Parameters in CEL</p>
          <div className="text-xs font-mono text-muted-foreground space-y-1">
            <div>inputs.&lt;name&gt; - access parameter value</div>
            <div>inputs.branch_name - example: string param</div>
            <div>inputs.max_retries - example: integer param</div>
          </div>
        </div>
      </div>
    </ConfigurationPanel>
  )
}

// ============================================================================
// PARAM EDITOR COMPONENT
// ============================================================================

interface ParamEditorProps {
  param: ParamWithId
  onUpdate: (updates: Record<string, unknown>) => void
  onRename: (newName: string) => void
  onRemove: () => void
}

function ParamEditor({ param, onUpdate, onRename, onRemove }: ParamEditorProps) {
  const typeConfig = PARAM_TYPES.find(t => t.value === param.type)
  const isListType = param.type ? LIST_TYPES.includes(param.type) : false
  
  return (
    <div className="border border-border rounded-lg p-3 space-y-3">
      {/* Name and delete */}
      <div className="flex gap-2 items-start">
        <div className="flex-1">
          <label className="block text-xs font-medium text-foreground mb-1">
            Name
          </label>
          <input
            type="text"
            value={param._name}
            onChange={(e) => onRename(e.target.value)}
            className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
            placeholder="param_name"
          />
        </div>
        <button
          onClick={onRemove}
          className="text-red-600 hover:text-red-700 mt-5"
          title="Remove parameter"
        >
          <Trash2 className="w-4 h-4" />
        </button>
      </div>

      {/* Type selector */}
      <div>
        <label className="block text-xs font-medium text-foreground mb-1">
          Type
        </label>
        <select
          value={param.type}
          onChange={(e) => {
            const newType = e.target.value
            // Clear default when switching to a type that doesn't support it
            const newTypeConfig = PARAM_TYPES.find(t => t.value === newType)
            const updates: Record<string, unknown> = { type: newType }
            if (!newTypeConfig?.hasDefault) {
              updates.default = undefined
            }
            // Clear enumValues if switching away from enum
            if (newType !== 'enum') {
              updates.enumValues = undefined
            }
            onUpdate(updates)
          }}
          className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
        >
          {/* Group types by category */}
          {Object.entries(
            PARAM_TYPES.reduce((acc, t) => {
              const group = t.group || 'Other'
              if (!acc[group]) acc[group] = []
              acc[group].push(t)
              return acc
            }, {} as Record<string, typeof PARAM_TYPES>)
          ).map(([group, types]) => (
            <optgroup key={group} label={group}>
              {types.map((t) => (
                <option key={t.value} value={t.value}>
                  {t.label}
                </option>
              ))}
            </optgroup>
          ))}
        </select>
        {typeConfig?.description && (
          <p className="mt-1 text-xs text-muted-foreground">
            {typeConfig.description}
          </p>
        )}
      </div>

      {/* List types info */}
      {isListType && (
        <p className="text-xs text-muted-foreground bg-muted/50 px-2 py-1.5 rounded">
          List type - defaults to empty list if not provided
        </p>
      )}

      {/* Type-specific inputs */}
      <TypeSpecificInput param={param} onUpdate={onUpdate} />

      {/* Validation section - only show for relevant types */}
      <ValidationSection param={param} onUpdate={onUpdate} />

      {/* Visibility selector */}
      <div>
        <label className="block text-xs font-medium text-foreground mb-1">
          Visibility
        </label>
        <select
          value={getInputUI(param as InputDef) || 'config'}
          onChange={(e) => onUpdate({ ui: e.target.value as 'hidden' | 'config' | undefined })}
          className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
        >
          <option value="config">Visible in UI</option>
          <option value="hidden">Hidden (runtime only)</option>
        </select>
      </div>
    </div>
  )
}

// ============================================================================
// TYPE-SPECIFIC INPUT COMPONENTS
// ============================================================================

interface TypeSpecificInputProps {
  param: ParamWithId
  onUpdate: (updates: Record<string, unknown>) => void
}

function TypeSpecificInput({ param, onUpdate }: TypeSpecificInputProps) {
  switch (param.type) {
    case 'enum':
      return <EnumInput param={param} onUpdate={onUpdate} />
    case 'boolean':
      return <BooleanDefaultInput param={param} onUpdate={onUpdate} />
    case 'model':
      return <ModelDefaultInput param={param} onUpdate={onUpdate} />
    case 'tools':
      return <ToolsDefaultInput param={param} onUpdate={onUpdate} />
    case 'preset':
      return <PresetMultiSelectInput param={param} onUpdate={onUpdate} />
    case 'integer':
    case 'number':
      return <NumberDefaultInput param={param} onUpdate={onUpdate} />
    case 'object':
      return <ObjectSchemaDefinitionInput param={param} onUpdate={onUpdate} />
    case 'string':
    case 'any':
      return <StringDefaultInput param={param} onUpdate={onUpdate} />
    case 'message':
    case 'attachments':
      // No default input for these types
      return null
    default:
      return <StringDefaultInput param={param} onUpdate={onUpdate} />
  }
}

function EnumInput({ param, onUpdate }: TypeSpecificInputProps) {
  const enumValues = getInputEnumValues(param as InputDef) || []
  
  return (
    <>
      <div>
        <label className="block text-xs font-medium text-foreground mb-1">
          Allowed Values <span className="text-destructive">*</span>
        </label>
        <input
          type="text"
          value={enumValues.join(', ')}
          onChange={(e) => onUpdate({ 
            enumValues: e.target.value.split(',').map(s => s.trim()).filter(Boolean) 
          })}
          className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
          placeholder="option1, option2, option3"
        />
        <p className="mt-1 text-xs text-muted-foreground">
          Comma-separated list of allowed values
        </p>
      </div>
      
      {enumValues.length > 0 && (
        <div>
          <label className="block text-xs font-medium text-foreground mb-1">
            Default Value
          </label>
          <select
            value={formatValueForDisplay(getParamDefault(param)) ?? ''}
            onChange={(e) => {
              const newDefault = e.target.value || undefined
              onUpdate({ 
                default: toDefaultValue(newDefault),
              })
            }}
            className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
          >
            <option value="">No default (user must provide value)</option>
            {enumValues.map((val) => (
              <option key={val} value={val}>{val}</option>
            ))}
          </select>
        </div>
      )}
    </>
  )
}

function BooleanDefaultInput({ param, onUpdate }: TypeSpecificInputProps) {
  const defaultVal = getParamDefault(param)
  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-1">
        Default Value
      </label>
      <select
        value={defaultVal !== undefined ? formatValueForDisplay(defaultVal) : ''}
        onChange={(e) => {
          const newDefault = e.target.value === 'true' ? true : e.target.value === 'false' ? false : undefined
          onUpdate({ 
            default: toDefaultValue(newDefault),
          })
        }}
        className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
      >
        <option value="">No default (user must provide value)</option>
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    </div>
  )
}

function ModelDefaultInput({ param, onUpdate }: TypeSpecificInputProps) {
  // Use the global models store so it updates when API keys change
  const { models, loading } = useModels()
  const defaultVal = getParamDefault(param)

  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-1">
        Default Model
      </label>
      <select
        value={formatValueForDisplay(defaultVal ?? '')}
        onChange={(e) => {
          const newDefault = e.target.value || undefined
          onUpdate({ 
            default: toDefaultValue(newDefault),
          })
        }}
        disabled={loading}
        className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
      >
        <option value="">No default (user must provide value)</option>
        {models.map((model) => (
          <option key={model.id} value={model.id}>
            {model.name} ({model.provider})
          </option>
        ))}
      </select>
    </div>
  )
}

function ToolsDefaultInput({ param, onUpdate }: TypeSpecificInputProps) {
  // Parse the default value from proto Value
  const parseDefault = (): string[] => {
    const defaultVal = getParamDefault(param)
    if (!defaultVal) return []
    if (Array.isArray(defaultVal)) return defaultVal as string[]
    if (typeof defaultVal === 'string') {
      try {
        const parsed = JSON.parse(defaultVal)
        return Array.isArray(parsed) ? parsed : []
      } catch {
        return []
      }
    }
    return []
  }
  
  const [selectedTools, setSelectedTools] = useState<string[]>(parseDefault)

  const handleChange = (tools: string[]) => {
    setSelectedTools(tools)
    // Store as array directly
    onUpdate({ 
      default: toDefaultValue(tools.length > 0 ? tools : undefined)
    })
  }

  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-2">
        Default Tools
      </label>
      <ToolsSelector
        value={selectedTools}
        onChange={handleChange}
        description="Select tools to enable by default (empty = all tools available)"
      />
    </div>
  )
}

function PresetMultiSelectInput({ param, onUpdate }: TypeSpecificInputProps) {
  const { currentProject } = useProjectStore()
  const [availablePresets, setAvailablePresets] = useState<Preset[]>([])
  const [loading, setLoading] = useState(true)

  // Parse the default value from proto Value
  const parseDefault = (): string[] => {
    const defaultVal = getParamDefault(param)
    if (!defaultVal) return []
    if (Array.isArray(defaultVal)) return defaultVal as string[]
    if (typeof defaultVal === 'string') {
      try {
        const parsed = JSON.parse(defaultVal)
        return Array.isArray(parsed) ? parsed : []
      } catch {
        return []
      }
    }
    return []
  }
  
  const [selectedPresets, setSelectedPresets] = useState<string[]>(parseDefault)

  useEffect(() => {
    if (!currentProject?.id) {
      setLoading(false)
      return
    }
    presetGrpc.listPresets(currentProject.id)
      .then(presets => {
        setAvailablePresets(presets)
        setLoading(false)
      })
      .catch(err => {
        console.error('Failed to load presets:', err)
        setLoading(false)
      })
  }, [currentProject?.id])

  const handleToggle = (presetName: string) => {
    const newSelection = selectedPresets.includes(presetName)
      ? selectedPresets.filter(p => p !== presetName)
      : [...selectedPresets, presetName]
    setSelectedPresets(newSelection)
    onUpdate({ 
      default: toDefaultValue(newSelection.length > 0 ? newSelection : undefined)
    })
  }

  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-2">
        Default Presets
      </label>
      {loading ? (
        <div className="text-xs text-muted-foreground">Loading presets...</div>
      ) : availablePresets.length === 0 ? (
        <div className="text-xs text-muted-foreground">
          No presets found. Create presets in <code className="font-mono bg-muted px-1 rounded">.reliant/presets/</code>
        </div>
      ) : (
        <div className="border border-input rounded-md max-h-48 overflow-y-auto">
          {availablePresets.map(preset => {
            const isSelected = selectedPresets.includes(preset.name)
            return (
              <label
                key={preset.name}
                className={`flex items-start gap-2 px-3 py-2 cursor-pointer hover:bg-muted/50 border-b border-input last:border-b-0 ${
                  isSelected ? 'bg-primary/5' : ''
                }`}
              >
                <input
                  type="checkbox"
                  checked={isSelected}
                  onChange={() => handleToggle(preset.name)}
                  className="mt-0.5"
                />
                <div className="flex-1 min-w-0">
                  <div className="text-sm font-medium">{preset.name}</div>
                  {preset.description && (
                    <div className="text-xs text-muted-foreground truncate">
                      {preset.description}
                    </div>
                  )}
                </div>
              </label>
            )
          })}
        </div>
      )}
      <p className="mt-1 text-xs text-muted-foreground">
        Select presets to include by default (empty = none)
      </p>
    </div>
  )
}

function NumberDefaultInput({ param, onUpdate }: TypeSpecificInputProps) {
  const isInteger = param.type === 'integer'
  
  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-1">
        Default Value
      </label>
      <input
        type="number"
        step={isInteger ? 1 : 'any'}
        value={formatValueForDisplay(getParamDefault(param)) ?? ''}
        onChange={(e) => {
          const val = e.target.value
          const newDefault = val ? (isInteger ? parseInt(val, 10) : parseFloat(val)) : undefined
          onUpdate({ 
            default: toDefaultValue(newDefault),
          })
        }}
        className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
        placeholder={isInteger ? '0' : '0.0'}
      />
      <p className="mt-1 text-xs text-muted-foreground">
        Leave empty to require user input
      </p>
    </div>
  )
}

function StringDefaultInput({ param, onUpdate }: TypeSpecificInputProps) {
  const defaultVal = getParamDefault(param)
  return (
    <div>
      <label className="block text-xs font-medium text-foreground mb-1">
        Default Value
      </label>
      <input
        type="text"
        value={formatValueForDisplay(defaultVal ?? '')}
        onChange={(e) => {
          const newDefault = e.target.value || undefined
          onUpdate({ 
            default: toDefaultValue(newDefault),
          })
        }}
        className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
        placeholder="default value"
      />
      <p className="mt-1 text-xs text-muted-foreground">
        Leave empty to require user input
      </p>
    </div>
  )
}

// ============================================================================
// VALIDATION SECTION
// ============================================================================

// Types that support validation
const STRING_VALIDATION_TYPES = ['string']
const ARRAY_VALIDATION_TYPES = ['array', 'tools', 'attachments']

function ValidationSection({ param, onUpdate }: TypeSpecificInputProps) {
  const isStringType = param.type ? STRING_VALIDATION_TYPES.includes(param.type) : false
  const isArrayType = param.type ? ARRAY_VALIDATION_TYPES.includes(param.type) : false
  const hasValidation = !!(getInputPattern(param as InputDef) || getInputMinLength(param as InputDef) || getInputMaxLength(param as InputDef) || getInputMinItems(param as InputDef) || getInputMaxItems(param as InputDef))
  const [isExpanded, setIsExpanded] = useState(hasValidation)
  
  // Only show for types that support validation
  if (!isStringType && !isArrayType) {
    return null
  }

  return (
    <div className="border border-border rounded-md overflow-hidden">
      {/* Header - collapsible */}
      <button
        type="button"
        onClick={() => setIsExpanded(!isExpanded)}
        className="w-full flex items-center gap-2 px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted/50 transition-colors"
      >
        {isExpanded ? (
          <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
        ) : (
          <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
        )}
        <span>Validation</span>
        {hasValidation && (
          <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">
            configured
          </span>
        )}
      </button>

      {/* Expanded content */}
      {isExpanded && (
        <div className="px-2.5 pb-2.5 pt-1 border-t border-border space-y-2.5">
          {/* String validation fields */}
          {isStringType && (
            <>
              {/* Pattern */}
              <div>
                <label className="block text-xs font-medium text-foreground mb-1">
                  Pattern (Regex)
                </label>
                <input
                  type="text"
                  value={getInputPattern(param as InputDef) || ''}
                  onChange={(e) => onUpdate({ pattern: e.target.value || undefined })}
                  className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="^[a-z]+$"
                />
                <p className="mt-1 text-xs text-muted-foreground">
                  Regular expression pattern for validation
                </p>
              </div>

              {/* Min/Max Length */}
              <div className="grid grid-cols-2 gap-2">
                <div>
                  <label className="block text-xs font-medium text-foreground mb-1">
                    Min Length
                  </label>
                  <input
                    type="number"
                    min="0"
                    value={getInputMinLength(param as InputDef) ?? ''}
                    onChange={(e) => onUpdate({
                      minLength: e.target.value ? parseInt(e.target.value, 10) : undefined
                    })}
                    className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
                    placeholder="0"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-foreground mb-1">
                    Max Length
                  </label>
                  <input
                    type="number"
                    min="0"
                    value={getInputMaxLength(param as InputDef) ?? ''}
                    onChange={(e) => onUpdate({ 
                      maxLength: e.target.value ? parseInt(e.target.value, 10) : undefined 
                    })}
                    className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
                    placeholder="∞"
                  />
                </div>
              </div>
            </>
          )}

          {/* Array validation fields */}
          {isArrayType && (
            <div className="grid grid-cols-2 gap-2">
              <div>
                <label className="block text-xs font-medium text-foreground mb-1">
                  Min Items
                </label>
                <input
                  type="number"
                  min="0"
                  value={getInputMinItems(param as InputDef) ?? ''}
                  onChange={(e) => onUpdate({
                    minItems: e.target.value ? parseInt(e.target.value, 10) : undefined
                  })}
                  className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="0"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-foreground mb-1">
                  Max Items
                </label>
                <input
                  type="number"
                  min="0"
                  value={getInputMaxItems(param as InputDef) ?? ''}
                  onChange={(e) => onUpdate({ 
                    maxItems: e.target.value ? parseInt(e.target.value, 10) : undefined 
                  })}
                  className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="∞"
                />
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ============================================================================
// OBJECT SCHEMA DEFINITION INPUT
// ============================================================================

function ObjectSchemaDefinitionInput({ param, onUpdate }: TypeSpecificInputProps) {
  const [isExpanded, setIsExpanded] = useState(true)
  const properties = getInputProperties(param as InputDef) || {}
  const required = getInputRequired(param as InputDef) || []
  const propertyCount = Object.keys(properties).length

  // Add a new property to the schema
  const addProperty = () => {
    const existingNames = Object.keys(properties)
    let counter = 1
    let newName = 'property1'
    while (existingNames.includes(newName)) {
      counter++
      newName = `property${counter}`
    }

    onUpdate({
      properties: {
        ...properties,
        [newName]: {
          type: 'string',
          description: '',
        },
      },
    } as any)
  }

  // Remove a property from the schema
  const removeProperty = (name: string) => {
    const newProperties = { ...properties }
    delete newProperties[name]

    const newRequired = required.filter((r: string) => r !== name)

    onUpdate({
      properties: newProperties,
      required: newRequired.length > 0 ? newRequired : undefined,
    } as any)
  }

  // Update a property in the schema
  const updateProperty = (oldName: string, updates: any) => {
    const newProperties = { ...properties }
    
    // Handle name change
    if (updates.name && updates.name !== oldName) {
      const propData = newProperties[oldName]
      delete newProperties[oldName]
      newProperties[updates.name] = { ...propData, ...updates }
      
      // Update required array if property was required
      if (required.includes(oldName)) {
        const newRequired = required.map((r: string) => r === oldName ? updates.name : r)
        onUpdate({
          properties: newProperties,
          required: newRequired,
        } as any)
      } else {
        onUpdate({ properties: newProperties } as any)
      }
    } else {
      // Just update the property data
      newProperties[oldName] = { ...newProperties[oldName], ...updates }
      onUpdate({ properties: newProperties } as any)
    }
  }

  // Toggle required status
  const toggleRequired = (name: string) => {
    const newRequired = required.includes(name)
      ? required.filter((r: string) => r !== name)
      : [...required, name]

    onUpdate({
      required: newRequired.length > 0 ? newRequired : undefined,
    } as any)
  }

  return (
    <div className="space-y-3">
      <div className="border border-border rounded-md overflow-hidden">
        {/* Header - collapsible */}
        <button
          type="button"
          onClick={() => setIsExpanded(!isExpanded)}
          className="w-full flex items-center gap-2 px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted/50 transition-colors"
        >
          {isExpanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-muted-foreground" />
          )}
          <span>Object Properties</span>
          {propertyCount > 0 && (
            <span className="ml-auto text-xs px-1.5 py-0.5 rounded bg-primary/10 text-primary">
              {propertyCount} {propertyCount === 1 ? 'property' : 'properties'}
            </span>
          )}
        </button>

        {/* Expanded content */}
        {isExpanded && (
          <div className="px-2.5 pb-2.5 pt-1 border-t border-border space-y-2">
            {propertyCount === 0 ? (
              <p className="text-xs text-muted-foreground italic py-2">
                No properties defined. Add properties to define the object structure.
              </p>
            ) : (
              <div className="space-y-2">
                {Object.entries(properties).map(([name, propSchema]: [string, any]) => (
                  <ObjectPropertyEditor
                    key={name}
                    name={name}
                    schema={propSchema}
                    isRequired={required.includes(name)}
                    onUpdate={(updates) => updateProperty(name, updates)}
                    onRemove={() => removeProperty(name)}
                    onToggleRequired={() => toggleRequired(name)}
                  />
                ))}
              </div>
            )}

            <button
              type="button"
              onClick={addProperty}
              className="w-full flex items-center justify-center gap-1.5 px-2 py-1.5 text-xs text-primary border border-dashed border-primary/50 rounded hover:bg-primary/5 transition-colors"
            >
              <Plus className="w-3.5 h-3.5" />
              Add Property
            </button>
          </div>
        )}
      </div>

      {/* Default value input - optional */}
      <div>
        <label className="block text-xs font-medium text-foreground mb-1">
          Default Value (JSON)
        </label>
        <textarea
          value={(() => {
            const defaultVal = getParamDefault(param)
            return defaultVal ? JSON.stringify(defaultVal, null, 2) : ''
          })()}
          onChange={(e) => {
            try {
              const parsed = e.target.value ? JSON.parse(e.target.value) : undefined
              onUpdate({ default: toDefaultValue(parsed) })
            } catch {
              // Invalid JSON, don't update
            }
          }}
          rows={3}
          className="w-full px-3 py-2 border border-input rounded-md text-sm bg-background text-foreground focus:ring-2 focus:ring-ring/40 focus:border-ring transition-colors"
          placeholder='{\n  "key": "value"\n}'
        />
        <p className="mt-1 text-xs text-muted-foreground">
          Optional default value as JSON. Leave empty to require user input.
        </p>
      </div>
    </div>
  )
}

// Property editor for object schema properties
interface ObjectPropertyEditorProps {
  name: string
  schema: any
  isRequired: boolean
  onUpdate: (updates: any) => void
  onRemove: () => void
  onToggleRequired: () => void
}

function ObjectPropertyEditor({
  name,
  schema,
  isRequired,
  onUpdate,
  onRemove,
  onToggleRequired,
}: ObjectPropertyEditorProps) {
  const [isExpanded, setIsExpanded] = useState(false)

  return (
    <div className="border border-border/50 rounded p-2 space-y-2 bg-muted/20">
      {/* Property header */}
      <div className="flex items-start gap-2">
        <button
          type="button"
          onClick={() => setIsExpanded(!isExpanded)}
          className="p-0.5 hover:bg-accent rounded transition-colors"
        >
          {isExpanded ? (
            <ChevronDown className="w-3 h-3 text-muted-foreground" />
          ) : (
            <ChevronRight className="w-3 h-3 text-muted-foreground" />
          )}
        </button>

        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2">
            <input
              type="text"
              value={name}
              onChange={(e) => onUpdate({ name: e.target.value })}
              className="flex-1 px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
            />
            <span className="text-xs px-1 py-0.5 rounded bg-background border border-border">
              {schema.type || 'string'}
            </span>
            {isRequired && (
              <span className="text-xs px-1 py-0.5 rounded bg-destructive/10 text-destructive font-medium">
                Required
              </span>
            )}
          </div>
        </div>

        <button
          type="button"
          onClick={onRemove}
          className="p-0.5 hover:bg-destructive/10 hover:text-destructive rounded transition-colors"
          title="Remove property"
        >
          <Trash2 className="w-3 h-3" />
        </button>
      </div>

      {/* Expanded details */}
      {isExpanded && (
        <div className="space-y-2 pl-5">
          {/* Type selector */}
          <div>
            <label className="block text-xs font-medium text-foreground mb-0.5">
              Type
            </label>
            <select
              value={schema.type || 'string'}
              onChange={(e) => onUpdate({ type: e.target.value })}
              className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
            >
              <option value="string">String</option>
              <option value="number">Number</option>
              <option value="integer">Integer</option>
              <option value="boolean">Boolean</option>
              <option value="array">Array</option>
              <option value="object">Object</option>
            </select>
          </div>

          {/* Description */}
          <div>
            <label className="block text-xs font-medium text-foreground mb-0.5">
              Description
            </label>
            <input
              type="text"
              value={schema.description || ''}
              onChange={(e) => onUpdate({ description: e.target.value || undefined })}
              className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
              placeholder="Property description..."
            />
          </div>

          {/* Required checkbox */}
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id={`required-${name}`}
              checked={isRequired}
              onChange={onToggleRequired}
              className="w-3 h-3 rounded border-input text-primary focus:ring-1 focus:ring-ring"
            />
            <label htmlFor={`required-${name}`} className="text-xs text-foreground">
              Required field
            </label>
          </div>

          {/* Type-specific constraints */}
          {(schema.type === 'string' || !schema.type) && (
            <div className="grid grid-cols-2 gap-1.5">
              <div>
                <label className="block text-xs font-medium text-foreground mb-0.5">
                  Min Length
                </label>
                <input
                  type="number"
                  min="0"
                  value={schema.minLength ?? ''}
                  onChange={(e) => onUpdate({ minLength: e.target.value ? parseInt(e.target.value) : undefined })}
                  className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="0"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-foreground mb-0.5">
                  Max Length
                </label>
                <input
                  type="number"
                  min="0"
                  value={schema.maxLength ?? ''}
                  onChange={(e) => onUpdate({ maxLength: e.target.value ? parseInt(e.target.value) : undefined })}
                  className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="∞"
                />
              </div>
            </div>
          )}

          {(schema.type === 'number' || schema.type === 'integer') && (
            <div className="grid grid-cols-2 gap-1.5">
              <div>
                <label className="block text-xs font-medium text-foreground mb-0.5">
                  Minimum
                </label>
                <input
                  type="number"
                  value={schema.minimum ?? ''}
                  onChange={(e) => onUpdate({ minimum: e.target.value ? parseFloat(e.target.value) : undefined })}
                  className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="-∞"
                />
              </div>
              <div>
                <label className="block text-xs font-medium text-foreground mb-0.5">
                  Maximum
                </label>
                <input
                  type="number"
                  value={schema.maximum ?? ''}
                  onChange={(e) => onUpdate({ maximum: e.target.value ? parseFloat(e.target.value) : undefined })}
                  className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
                  placeholder="∞"
                />
              </div>
            </div>
          )}

          {/* Enum values */}
          <div>
            <label className="block text-xs font-medium text-foreground mb-0.5">
              Enum Values (optional)
            </label>
            <input
              type="text"
              value={schema.enum ? schema.enum.join(', ') : ''}
              onChange={(e) => {
                const values = e.target.value.split(',').map(s => s.trim()).filter(Boolean)
                onUpdate({ enum: values.length > 0 ? values : undefined })
              }}
              className="w-full px-2 py-1.5 text-xs border border-input rounded-md bg-background text-foreground focus:ring-1 focus:ring-ring/40 focus:border-ring transition-colors"
              placeholder="val1, val2, val3"
            />
            <p className="mt-0.5 text-xs text-muted-foreground">
              Comma-separated allowed values
            </p>
          </div>
        </div>
      )}
    </div>
  )
}
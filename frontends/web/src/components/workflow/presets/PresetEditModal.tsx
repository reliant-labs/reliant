// Copyright (c) 2025 Reliant Labs

/**
 * Edit a user preset's name, description, tag and parameters. Extracted
 * unchanged from the retired WorkflowHub (WORKFLOW_UI.md §1.2, decision 7).
 */

import { useState, useEffect, useMemo } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { Modal } from '../../ui/Modal'
import { Button } from '../../ui/Button'
import { ModelSelector } from '../../Chat/ModelSelector'
import { ToolsSelector } from '../ToolsSelector'
import { AgentSelector } from '../../Chat/AgentSelector'
import { MultiSelectDropdown } from '../../ui/MultiSelectDropdown'
import type { Preset } from '../../../store/globalDataStore'
import { presetGrpc } from '../../../api/preset-grpc'
import { workflowGrpc, type WorkflowResponse } from '../../../api/workflow-grpc'
import { formatValueForDisplay } from '../../../lib/paramUtils'
import { ProtoFieldRenderer } from '../ProtoFieldRenderer'
import { inputDefToSchema } from '../../../lib/nodeFieldAdapter'
import type { InputDef } from '../../../lib/inputHelpers'
import { getInputDefault, getInputDescription, setInputEnumValues } from '../../../lib/inputHelpers'
import { useModels } from '../../../store/globalDataStore'
import { useThinkingCapability, reconcileThinkingLevel } from '../../../hooks/useThinkingCapability'

function ParamField({ 
  label, 
  value, 
  onChange, 
  readOnly,
  availablePresets = [],
  schema,
  formValues: _formValues
}: {
  label: string;
  value: any;
  onChange: (val: any) => void;
  readOnly?: boolean;
  availablePresets?: Preset[];
  schema?: InputDef;
  formValues?: Record<string, unknown>;
}) {
  // 1. Model Selector
  if (label === 'model') {
    return (
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{label}</label>
        {readOnly ? (
          <div className="p-2 text-sm bg-muted/50 rounded-md border border-border/50 text-muted-foreground">
            {(typeof value === 'object' ? (value?.id || JSON.stringify(value)) : value) || 'Default'}
          </div>
        ) : (
          <ModelSelector
            defaultModel={typeof value === 'object' ? (value?.id || '') : (value ?? '')}
            onSelect={(modelId) => onChange(modelId ? { id: modelId } : { id: '' })}
          />
        )}
      </div>
    );
  }

  // 2. Tools Selector
  if (label === 'tools') {
    return (
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{label}</label>
        {readOnly ? (
          <div className="flex flex-wrap gap-1 p-2 bg-muted/50 rounded-md border border-border/50">
            {Array.isArray(value) && value.length > 0 ? (
              value.map((tool: string) => (
                <span key={tool} className="text-xs bg-background/50 px-1.5 py-0.5 rounded border border-border/50">
                  {tool}
                </span>
              ))
            ) : (
              <span className="text-sm text-muted-foreground">No tools selected</span>
            )}
          </div>
        ) : (
          <ToolsSelector
            value={Array.isArray(value) ? value : []}
            onChange={onChange}
          />
        )}
      </div>
    );
  }

  // 3. Agent Selector
  if (label === 'agent' || label === 'agent_id') {
    return (
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{label}</label>
        {readOnly ? (
          <div className="p-2 text-sm bg-muted/50 rounded-md border border-border/50 text-muted-foreground">
            {typeof value === 'object' ? JSON.stringify(value) : (value || 'General')}
          </div>
        ) : (
          <div className="w-full">
            <AgentSelector
              value={value}
              onChange={(val) => onChange(val)}
              className="w-full"
            />
          </div>
        )}
      </div>
    );
  }

  // 4. Presets Multi-Select (for spawn_presets)
  if (label === 'spawn_presets' || label === 'presets') {
    const options = availablePresets.map(p => ({
      value: p.name,
      label: p.name,
      description: p.description
    }));

    return (
      <div className="space-y-1.5">
        <label className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{label}</label>
        {readOnly ? (
          <div className="flex flex-wrap gap-1 p-2 bg-muted/50 rounded-md border border-border/50">
            {Array.isArray(value) && value.length > 0 ? (
              value.map((presetItem: any, idx: number) => {
                const displayName = typeof presetItem === 'string' ? presetItem : JSON.stringify(presetItem)
                return (
                  <span key={idx} className="text-xs bg-background/50 px-1.5 py-0.5 rounded border border-border/50">
                    {displayName}
                  </span>
                )
              })
            ) : (
              <span className="text-sm text-muted-foreground">No presets selected</span>
            )}
          </div>
        ) : (
          <MultiSelectDropdown
            options={options}
            value={Array.isArray(value) ? value : []}
            onChange={onChange}
            placeholder="Select presets..."
            emptyMessage="No presets found"
          />
        )}
      </div>
    );
  }

  // Use Schema-based input if available (and not one of the special types above)
  if (schema) {
    if (schema.type !== 'preset') {
        return (
            <div className="space-y-1.5">
                <ProtoFieldRenderer
                    schema={inputDefToSchema(label, schema)}
                    value={value}
                    onChange={onChange}
                    disabled={readOnly}
                    hideCELToggle
                />
            </div>
        )
    }
  }

  const type = Array.isArray(value) ? 'array' : typeof value

  if (type === 'boolean') {
    return (
      <div className="flex items-center justify-between py-2 border p-2 rounded-md border-border bg-background">
        <label className="text-sm font-medium text-foreground">{label}</label>
        <input
          type="checkbox"
          checked={value}
          onChange={(e) => onChange(e.target.checked)}
          disabled={readOnly}
          className="h-4 w-4 rounded border-border bg-background text-primary focus:ring-ring/20"
        />
      </div>
    )
  }

  if (type === 'array') {
    // Array of strings editor
    const items = Array.isArray(value) ? value : []
    return (
      <div className="space-y-2">
        <label className="text-sm font-medium text-foreground">{label}</label>
        <div className="space-y-2 pl-2 border-l-2 border-border">
            {items.map((item: any, idx: number) => {
                const displayValue = formatValueForDisplay(item)
                return (
                <div key={idx} className="flex gap-2">
                    <input
                        value={displayValue}
                        onChange={(e) => {
                            const newItems = [...items]
                            newItems[idx] = e.target.value
                            onChange(newItems)
                        }}
                        disabled={readOnly}
                        className="flex-1 px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                    />
                    {!readOnly && (
                        <button
                            onClick={() => {
                                const newItems = items.filter((_: any, i: number) => i !== idx)
                                onChange(newItems)
                            }}
                            className="p-2 hover:bg-destructive/10 text-muted-foreground hover:text-destructive-ink rounded-md transition-colors"
                        >
                            <Trash2 className="w-4 h-4" />
                        </button>
                    )}
                </div>
            )})}
            {!readOnly && (
                <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onChange([...items, ""])}
                    leftIcon={<Plus className="w-4 h-4" />}
                >
                    Add Item
                </Button>
            )}
        </div>
      </div>
    )
  }

  if (type === 'object' && value !== null) {
    return (
      <div className="space-y-1">
        <label className="text-sm font-medium text-foreground">{label}</label>
        <div className="text-xs text-muted-foreground mb-1">Complex object (JSON)</div>
        <textarea
          value={JSON.stringify(value, null, 2)}
          disabled={true}
          className="w-full px-3 py-2 text-sm border border-border rounded-md bg-muted text-muted-foreground font-mono resize-y"
          rows={4}
        />
      </div>
    )
  }

  // String, Number, or unknown
  const isLongString = typeof value === 'string' && (value.length > 50 || value.includes('\n'))

  return (
    <div className="space-y-1">
      <label className="text-sm font-medium text-foreground">{label}</label>
      {isLongString ? (
        <textarea
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={readOnly}
            rows={5}
            className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring font-mono resize-y"
        />
      ) : (
        <input
            type={typeof value === 'number' ? 'number' : 'text'}
            value={value}
            onChange={(e) => onChange(typeof value === 'number' ? Number(e.target.value) : e.target.value)}
            disabled={readOnly}
            className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
        />
      )}
    </div>
  )
}

interface PresetEditModalProps {
  preset: Preset
  projectId: string
  availablePresets?: Preset[]
  onSave: () => void
  onClose: () => void
}

// Default schemas for common parameters to ensure good UI even if workflow fetch fails
const DEFAULT_PARAM_SCHEMAS: Record<string, InputDef> = {
  mode: {
    type: 'enum',
    config: { case: 'enumInput', value: { base: { description: 'Execution mode: manual = requires approval, auto = auto-approves, plan = read-only tools' }, enumValues: ['manual', 'auto', 'plan'], default: 'auto' } },
  } as InputDef,
  temperature: {
    type: 'number',
    config: { case: 'numberInput', value: { base: { description: 'Response randomness (0 = focused, 1 = creative)' }, default: 1.0, min: 0, max: 1 } },
  } as InputDef,
  thinking_level: {
    type: 'enum',
    config: { case: 'enumInput', value: { base: { description: 'Extended thinking level (support is model/provider dependent)' }, enumValues: ['low', 'medium', 'high'], default: 'medium' } },
  } as InputDef,
  max_turns: {
    type: 'integer',
    config: { case: 'integerInput', value: { base: { description: 'Maximum agent loop iterations' }, default: BigInt(100), min: BigInt(1), max: BigInt(500) } },
  } as InputDef,
  compaction_threshold: {
    type: 'integer',
    config: { case: 'integerInput', value: { base: { description: 'Token count to trigger context compaction' }, default: BigInt(185000), min: BigInt(10000) } },
  } as InputDef,
}

export function PresetEditModal({ preset, projectId, availablePresets = [], onSave, onClose }: PresetEditModalProps) {
  const [name, setName] = useState(preset.name)
  const [description, setDescription] = useState(preset.description)
  const [tag, setTag] = useState(preset.tag || '')
  // Initialize params from preset. Ensure it's an object.
  const [params, setParams] = useState<Record<string, any>>(() => {
    return typeof preset.params === 'object' && preset.params !== null ? { ...preset.params } : {}
  })
  const [isSaving, setIsSaving] = useState(false)

  // Copy functionality state (only for builtin presets)
  const [copyName, setCopyName] = useState('')
  const [isCopying, setIsCopying] = useState(false)

  // Store reference schema from workflows to provide better input UI
  const [referenceSchema, setReferenceSchema] = useState<Record<string, InputDef>>(DEFAULT_PARAM_SCHEMAS)
  useModels()
  const thinkingCapability = useThinkingCapability('thinking_level', params)
  const supportedThinkingLevels = thinkingCapability.levels

  useEffect(() => {
    const current = typeof params.thinking_level === 'string' ? params.thinking_level : ''
    // Tag selector with no explicit level: the tier owns the effort; pinning
    // one here would override it server-side.
    if (!current && thinkingCapability.tag) return
    const fallback = reconcileThinkingLevel(current, thinkingCapability)
    if (fallback !== current) {
      setParams(prev => ({ ...prev, thinking_level: fallback }))
    }
  }, [params.thinking_level, thinkingCapability])

  // Fetch workflows to build reference schema
  useEffect(() => {
    let mounted = true
    
    const fetchSchemas = async () => {
      try {
        // List workflows to find available params
        const workflows = await workflowGrpc.listWorkflows(projectId)
        if (!mounted || !workflows.length) return

        // Prioritize builtin workflows as they're most likely to define standard params
        const workflowsToFetch = workflows
          .sort((a: WorkflowResponse, b: WorkflowResponse) => {
            if (a.source === 'builtin' && b.source !== 'builtin') return -1
            if (a.source !== 'builtin' && b.source === 'builtin') return 1
            return 0
          })
          .slice(0, 5) // Limit to top 5 to avoid too many requests

        // Fetch full definitions for selected workflows
        const schemas: Record<string, InputDef> = {}
        
        await Promise.all(workflowsToFetch.map(async (wf: WorkflowResponse) => {
          try {
            const details = await workflowGrpc.getWorkflow(projectId, { name: wf.name })
            const workflowInputs = details.workflow?.inputs
            if (workflowInputs) {
              // Merge inputs into schema
              Object.entries(workflowInputs).forEach(([key, schema]) => {
                // Don't overwrite existing keys (prioritize earlier workflows/builtin)
                if (!schemas[key]) {
                  schemas[key] = schema as InputDef
                }
              })
            }
          } catch (err) {
            console.warn(`Failed to fetch workflow details for ${wf.name}:`, err)
          }
        }))
        
        if (mounted) {
          setReferenceSchema(schemas)
        }
      } catch (err) {
        console.error('Failed to fetch workflow schemas:', err)
      }
    }

    fetchSchemas()
    
    return () => {
      mounted = false
    }
  }, [projectId])

  const isEditable = preset.source === 'user'

  const handleSave = async () => {
    setIsSaving(true)
    try {
      // Use preset.name for the API call (works for both user and project presets)
      const result = await presetGrpc.updatePreset(projectId, preset.name, {
        newName: name !== preset.name ? name : undefined,
        newDescription: description !== preset.description ? description : undefined,
        newTag: tag !== (preset.tag || '') ? tag : undefined,
        newParams: params
      })

      if (result.success) {
        toast.success('Preset updated')
        onSave()
        onClose()
      } else {
        toast.error(result.error || 'Failed to update preset')
      }
    } catch (error) {
      console.error('Failed to update preset:', error)
      toast.error('Failed to update preset')
    } finally {
      setIsSaving(false)
    }
  }

  const handleCopy = async () => {
    if (!copyName.trim()) {
      toast.error('Please enter a name for the new preset')
      return
    }

    setIsCopying(true)
    try {
      // Create new preset with same params/description
      const result = await presetGrpc.createPreset(projectId, {
        name: copyName,
        description: preset.description,
        params: preset.params || {},
        tag: preset.tag
      })

      if (result.success) {
        toast.success('Preset copied successfully')
        onSave() // Refresh list
        onClose()
      } else {
        toast.error(result.error || 'Failed to copy preset')
      }
    } catch (error) {
      console.error('Failed to copy preset:', error)
      toast.error('Failed to copy preset')
    } finally {
      setIsCopying(false)
    }
  }

  // Get keys from initial preset params to lock the schema
  const paramKeys = useMemo(() => {
    return Object.keys(params)
  }, [params])

  // Get available params that are not yet in the preset
  const availableParamsToAdd = useMemo(() => {
    const existingKeys = new Set(Object.keys(params))
    return Object.keys(referenceSchema)
      .filter(key => !existingKeys.has(key))
      .sort()
  }, [params, referenceSchema])

  const [paramToAdd, setParamToAdd] = useState('')

  const handleAddParam = () => {
    if (!paramToAdd || !referenceSchema[paramToAdd]) return

    const schema = referenceSchema[paramToAdd]
    const explicitDefault = getInputDefault(schema)
    // When the schema doesn't declare a default, fall back to a kind-correct
    // empty value so object/array params don't get an empty-string initial
    // value that the ParamField branches would route to the wrong renderer.
    const fallbackDefault =
      schema?.config?.case === 'objectInput'
        ? {}
        : schema?.config?.case === 'arrayInput'
        ? []
        : ''
    setParams(prev => ({
      ...prev,
      [paramToAdd]: explicitDefault ?? fallbackDefault
    }))
    setParamToAdd('')
  }

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title={isEditable ? `Edit Preset: ${preset.name}` : `View Preset: ${preset.name}`}
      size="lg"
      hideCloseButton={true}
      headerActions={
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>
            Close
          </Button>
          {isEditable && (
            <Button size="sm" onClick={handleSave} disabled={isSaving}>
              {isSaving ? 'Saving...' : 'Save Changes'}
            </Button>
          )}
        </div>
      }
    >
      <div className="space-y-4">
        {/* Name (Editable) */}
        <div>
          <label className="block text-sm font-medium text-foreground mb-1">Name</label>
          <input
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={!isEditable || isSaving}
            className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring disabled:opacity-50 disabled:cursor-not-allowed"
          />
          {!isEditable && (
            <p className="text-xs text-muted-foreground mt-1">
              Built-in presets cannot be renamed.
            </p>
          )}
        </div>

        {/* Description (Editable) */}
        <div>
          <label className="block text-sm font-medium text-foreground mb-1">Description</label>
          <textarea
            value={description || ''}
            onChange={(e) => setDescription(e.target.value)}
            disabled={!isEditable || isSaving}
            rows={2}
            className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring disabled:opacity-50 disabled:cursor-not-allowed resize-none"
          />
        </div>

        {/* Tag (Editable) */}
        <div>
          <label className="block text-sm font-medium text-foreground mb-1">Tag</label>
          <input
            type="text"
            value={tag}
            onChange={(e) => setTag(e.target.value)}
            disabled={!isEditable || isSaving}
            placeholder="e.g. agent, orchestration"
            className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring disabled:opacity-50 disabled:cursor-not-allowed"
          />
          <p className="text-xs text-muted-foreground mt-1">
            Presets are filtered by tag when configuring workflows. Matches workflow/group tags.
          </p>
        </div>

        {/* Parameters (Form Fields) */}
        <div>
          <label className="block text-sm font-medium text-foreground mb-3">Parameters</label>
          
          <div className="space-y-4">
            {paramKeys.length === 0 ? (
                <div className="text-sm text-muted-foreground italic p-4 text-center border border-dashed border-border rounded-md">
                    No parameters to configure.
                </div>
            ) : (
                paramKeys.map(key => (
                    <ParamField
                        key={key}
                        label={key}
                        value={params[key]}
                        onChange={(newValue) => setParams(prev => ({ ...prev, [key]: newValue }))}
                        readOnly={!isEditable || isSaving}
                        availablePresets={availablePresets}
                        schema={key === 'thinking_level' && referenceSchema[key]
                          ? setInputEnumValues(referenceSchema[key], supportedThinkingLevels)
                          : referenceSchema[key]}
                        formValues={params}
                    />
                ))
            )}
          </div>
          <p className="text-xs text-muted-foreground mt-2">
            Parameter keys are fixed to ensure workflow compatibility.
          </p>

          {/* Add Parameter Section */}
          {isEditable && availableParamsToAdd.length > 0 && (
            <div className="mt-4 pt-4 border-t border-border">
              <label className="block text-sm font-medium text-foreground mb-2">Add Parameter</label>
              <div className="flex gap-2">
                <select
                  value={paramToAdd}
                  onChange={(e) => setParamToAdd(e.target.value)}
                  className="flex-1 px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                >
                  <option value="">Select parameter to add...</option>
                  {availableParamsToAdd.map(key => (
                    <option key={key} value={key}>
                      {key} {getInputDescription(referenceSchema[key]) ? ` - ${getInputDescription(referenceSchema[key])!.slice(0, 50)}...` : ''}
                    </option>
                  ))}
                </select>
                <Button 
                  onClick={handleAddParam} 
                  disabled={!paramToAdd || isSaving}
                  leftIcon={<Plus className="w-4 h-4" />}
                >
                  Add
                </Button>
              </div>
            </div>
          )}
        </div>

        {/* Copy section - only for builtin presets */}
        {preset.source === 'builtin' && (
          <div className="pt-4 border-t border-border mt-4">
            <h4 className="text-sm font-medium text-foreground mb-2">Copy to New Preset</h4>
            <div className="flex gap-2">
              <input
                type="text"
                value={copyName}
                onChange={(e) => setCopyName(e.target.value)}
                placeholder="New preset name"
                className="flex-1 px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                onKeyDown={(e) => e.key === 'Enter' && handleCopy()}
              />
              <Button onClick={handleCopy} disabled={isCopying || !copyName.trim()}>
                {isCopying ? 'Creating...' : 'Create Copy'}
              </Button>
            </div>
            <p className="text-xs text-muted-foreground mt-1.5">
              This will create a new editable preset with the same parameters.
            </p>
          </div>
        )}
      </div>
    </Modal>
  )
}

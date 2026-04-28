import { useState } from 'react'
import { Plus, Trash2, Tag } from 'lucide-react'
import type { Param, ThreadConfig } from '../../types/workflow'
import { ConfigurationPanel } from './ConfigurationPanel'
import { WorkflowParamsEditorContent } from './WorkflowParamsEditor'
import { CELExpressionInput } from './CELInput'
import { MultiSelectDropdown, type MultiSelectOption } from '../ui/MultiSelectDropdown'
import { NodeThreadConfigEditor } from './NodeThreadConfigEditor'
import { HelpPopover } from '../ui/HelpPopover'
import { ConfigPanelTabBar, type ConfigTab } from './config/ConfigPanelTabBar'

interface WorkflowSettingsEditorProps {
  params: Record<string, Param>
  entry?: string | string[]
  outputs?: Record<string, string>
  tag?: string
  thread?: ThreadConfig
  nodeIds: string[]
  onUpdateParams: (params: Record<string, Param>) => void
  onUpdateEntry: (entry: string | string[] | undefined) => void
  onUpdateOutputs: (outputs: Record<string, string>) => void
  onUpdateTag: (tag: string | undefined) => void
  onUpdateThread: (thread: ThreadConfig | undefined) => void
  onClose: () => void
  bottomOffset?: number
  topOffset?: number
}

type Tab = 'params' | 'entry' | 'outputs' | 'advanced'

export function WorkflowSettingsEditor({
  params,
  entry,
  outputs,
  tag,
  thread,
  nodeIds,
  onUpdateParams,
  onUpdateEntry,
  onUpdateOutputs,
  onUpdateTag,
  onUpdateThread,
  onClose,
  bottomOffset,
  topOffset,
}: WorkflowSettingsEditorProps) {
  const [activeTab, setActiveTab] = useState<Tab>('params')
  
  const paramCount = Object.keys(params || {}).length
  const outputCount = Object.keys(outputs || {}).length
  const hasEntry = entry !== undefined && (Array.isArray(entry) ? entry.length > 0 : entry !== '')
  const hasAdvanced = !!(tag || thread)
  const tabs: ConfigTab[] = [
    { id: 'params', label: paramCount > 0 ? `Params (${paramCount})` : 'Params' },
    { id: 'entry', label: 'Entry', hasBadge: hasEntry },
    { id: 'outputs', label: outputCount > 0 ? `Outputs (${outputCount})` : 'Outputs' },
    { id: 'advanced', label: 'Advanced', hasBadge: hasAdvanced },
  ]

  return (
    <ConfigurationPanel
      title="Workflow Settings"
      subtitle="Configure parameters, entry points, and outputs"
      subtitleMono={false}
      onClose={onClose}
      bottomOffset={bottomOffset}
      topOffset={topOffset}
    >
      <ConfigPanelTabBar
        tabs={tabs}
        activeTab={activeTab}
        onTabChange={(tabId) => setActiveTab(tabId as Tab)}
      />

      {/* Tab Content */}
      {activeTab === 'params' && (
        <WorkflowParamsEditorContent
          params={params}
          onUpdate={onUpdateParams}
        />
      )}

      {activeTab === 'entry' && (
        <EntryEditor
          entry={entry}
          nodeIds={nodeIds}
          onUpdate={onUpdateEntry}
        />
      )}

      {activeTab === 'outputs' && (
        <OutputsEditor
          outputs={outputs || {}}
          onUpdate={onUpdateOutputs}
          nodeIds={nodeIds}
          params={params}
        />
      )}

      {activeTab === 'advanced' && (
        <AdvancedEditor
          tag={tag}
          thread={thread}
          onUpdateTag={onUpdateTag}
          onUpdateThread={onUpdateThread}
        />
      )}
    </ConfigurationPanel>
  )
}

// Entry point editor - select which node(s) start the workflow
function EntryEditor({
  entry,
  nodeIds,
  onUpdate,
}: {
  entry?: string | string[]
  nodeIds: string[]
  onUpdate: (entry: string | string[] | undefined) => void
}) {
  const entryArray = Array.isArray(entry) ? entry : entry ? [entry] : []
  
  // Convert node IDs to dropdown options
  const options: MultiSelectOption[] = nodeIds.map(nodeId => ({
    value: nodeId,
    label: nodeId,
  }))

  const handleChange = (selected: string[]) => {
    if (selected.length === 0) {
      onUpdate(undefined)
    } else if (selected.length === 1) {
      onUpdate(selected[0])
    } else {
      onUpdate(selected)
    }
  }

  return (
    <div className="space-y-4">
      <div>
        <p className="text-sm text-muted-foreground mb-3">
          Select which node(s) should start when the workflow is triggered. 
          Multiple nodes will run in parallel.
        </p>
      </div>

      {nodeIds.length === 0 ? (
        <p className="text-sm text-muted-foreground italic">
          No nodes in workflow. Add nodes to configure entry points.
        </p>
      ) : (
        <MultiSelectDropdown
          options={options}
          value={entryArray}
          onChange={handleChange}
          placeholder="Select entry point(s)..."
          emptyMessage="No nodes found"
        />
      )}

      {entryArray.length > 1 && (
        <p className="text-xs text-muted-foreground mt-2">
          {entryArray.length} nodes selected - they will run in parallel when triggered.
        </p>
      )}
    </div>
  )
}

// Outputs editor - define CEL expressions for workflow outputs
function OutputsEditor({
  outputs,
  onUpdate,
  nodeIds,
  params,
}: {
  outputs: Record<string, string>
  onUpdate: (outputs: Record<string, string>) => void
  nodeIds: string[]
  params: Record<string, Param>
}) {
  const [newKey, setNewKey] = useState('')
  const [showSuggestions, setShowSuggestions] = useState(false)

  // Generate suggested CEL expressions based on available nodes and inputs
  const suggestions = [
    // Node outputs (most common)
    ...nodeIds.map(nodeId => ({
      label: `nodes.${nodeId}.result`,
      description: `Output from ${nodeId}`,
      value: `nodes.${nodeId}.result`,
    })),
    // Input passthrough
    ...Object.keys(params).map(paramName => ({
      label: `inputs.${paramName}`,
      description: `Workflow input: ${paramName}`,
      value: `inputs.${paramName}`,
    })),
  ]

  const addOutput = () => {
    if (!newKey.trim()) return
    if (outputs[newKey]) return // Already exists
    onUpdate({ ...outputs, [newKey]: '' })
    setNewKey('')
  }

  const addOutputWithValue = (name: string, value: string) => {
    if (outputs[name]) return // Already exists
    onUpdate({ ...outputs, [name]: value })
    setShowSuggestions(false)
  }

  const updateOutput = (key: string, value: string) => {
    onUpdate({ ...outputs, [key]: value })
  }

  const removeOutput = (key: string) => {
    const newOutputs = { ...outputs }
    delete newOutputs[key]
    onUpdate(newOutputs)
  }

  const outputEntries = Object.entries(outputs)
  const unusedSuggestions = suggestions.filter(
    s => !outputEntries.some(([_, value]) => value === s.value)
  )

  return (
    <div className="space-y-4">
      <div>
        <p className="text-sm text-muted-foreground mb-3">
          Define workflow outputs using CEL expressions. These values are available 
          to parent workflows after this workflow completes.
        </p>
      </div>

      {/* Existing outputs */}
      {outputEntries.length > 0 && (
        <div className="space-y-3">
          {outputEntries.map(([key, value]) => (
            <div key={key} className="space-y-1">
              <div className="flex items-center justify-between">
                <label className="text-sm font-medium text-foreground">
                  {key}
                </label>
                <button
                  onClick={() => removeOutput(key)}
                  className="p-1 text-muted-foreground hover:text-destructive transition-colors"
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
              <CELExpressionInput
                value={value}
                onChange={(val) => updateOutput(key, val)}
                placeholder="nodes.step_id.field"
                hideCELHint
              />
            </div>
          ))}
        </div>
      )}

      {/* Quick add from suggestions */}
      {unusedSuggestions.length > 0 && (
        <div className="relative">
          <button
            type="button"
            onClick={() => setShowSuggestions(!showSuggestions)}
            className="flex items-center gap-2 text-sm text-primary hover:text-primary/80 transition-colors"
          >
            <Plus className="w-4 h-4" />
            Add from available outputs
          </button>
          
          {showSuggestions && (
            <div className="absolute left-0 top-full mt-1 z-50 w-full bg-popover border border-border rounded-lg shadow-lg overflow-hidden">
              <div className="max-h-[200px] overflow-y-auto">
                {unusedSuggestions.map((suggestion) => {
                  // Derive output name from expression (last part)
                  const parts = suggestion.value.split('.')
                  const suggestedName = parts[parts.length - 1] === 'result' 
                    ? parts[parts.length - 2] // Use node name for .result
                    : parts[parts.length - 1] // Use last part otherwise
                  
                  return (
                    <button
                      key={suggestion.value}
                      type="button"
                      onClick={() => addOutputWithValue(suggestedName, suggestion.value)}
                      className="w-full text-left px-3 py-2 text-sm hover:bg-muted/50 transition-colors"
                    >
                      <div className="text-foreground">{suggestion.label}</div>
                      <div className="text-xs text-muted-foreground">{suggestion.description}</div>
                    </button>
                  )
                })}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Add custom output */}
      <div className="pt-2 border-t border-border">
        <p className="text-xs text-muted-foreground mb-2">Or add a custom output:</p>
        <div className="flex gap-2">
          <input
            type="text"
            value={newKey}
            onChange={(e) => setNewKey(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && addOutput()}
            placeholder="output_name"
            className="flex-1 px-3 py-2 border border-input rounded-md focus:ring-2 focus:ring-ring focus:border-ring bg-background text-foreground font-mono text-sm"
          />
          <button
            onClick={addOutput}
            disabled={!newKey.trim() || outputs[newKey] !== undefined}
            className="flex items-center gap-1 px-3 py-2 text-sm font-medium rounded-md bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <Plus className="w-4 h-4" />
            Add
          </button>
        </div>
      </div>

      {/* Help text */}
      <div className="text-xs text-muted-foreground space-y-1 bg-muted/50 p-3 rounded-lg">
        <p className="font-medium">Available namespaces:</p>
        <ul className="list-disc list-inside space-y-0.5">
          <li><code className="text-xs">nodes.&lt;step_id&gt;.*</code> - Step outputs</li>
          <li><code className="text-xs">inputs.*</code> - Workflow inputs</li>
        </ul>
      </div>
    </div>
  )
}

// Advanced settings editor - tag and thread configuration
function AdvancedEditor({
  tag,
  thread,
  onUpdateTag,
  onUpdateThread,
}: {
  tag?: string
  thread?: ThreadConfig
  onUpdateTag: (tag: string | undefined) => void
  onUpdateThread: (thread: ThreadConfig | undefined) => void
}) {
  return (
    <div className="space-y-6">
      {/* Workflow Tag */}
      <div>
        <div className="flex items-center gap-2 mb-2">
          <Tag className="w-4 h-4 text-muted-foreground" />
          <label className="text-sm font-medium text-foreground">
            Workflow Tag
          </label>
          <HelpPopover 
            content="Tag for preset matching. Presets with matching tag can apply to this workflow's ungrouped inputs. Common values: agent, workflow, tool"
          />
        </div>
        <input
          type="text"
          value={tag || ''}
          onChange={(e) => onUpdateTag(e.target.value || undefined)}
          placeholder="e.g., agent"
          className="w-full px-3 py-2 border border-input rounded-md focus:ring-2 focus:ring-ring focus:border-ring bg-background text-foreground text-sm"
        />
        <p className="text-xs text-muted-foreground mt-1.5">
          Used for preset matching on ungrouped inputs.
        </p>
      </div>

      {/* Workflow Thread Configuration */}
      <div className="pt-4 border-t border-border">
        <p className="text-sm text-muted-foreground mb-3">
          Configure how this workflow manages its conversation thread.
        </p>
        <NodeThreadConfigEditor
          config={thread}
          onChange={onUpdateThread}
        />
      </div>
    </div>
  )
}
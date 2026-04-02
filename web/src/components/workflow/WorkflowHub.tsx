import { useState, useMemo, useRef, useEffect, useCallback } from 'react'
import {
  Plus,
  Workflow as WorkflowIcon,
  Trash2,
  Upload,
  Download,
  Star,
  Settings2,
  Sparkles,
  Pencil,
  Layers,
  Copy,
  Eye,
  EyeOff,
  BookOpen,
  Expand,
  AlertTriangle,
  Loader2
} from 'lucide-react'
import { cn } from '../../lib/utils'
import { toast } from 'sonner'
import { Modal } from '../ui/Modal'
import { Button } from '../ui/Button'
import { Tooltip } from '../ui/Tooltip'
import { ModelSelector } from '../Chat/ModelSelector'
import { ToolsSelector } from './ToolsSelector'
import { AgentSelector } from '../Chat/AgentSelector'
import { MultiSelectDropdown } from '../ui/MultiSelectDropdown'
import type { Preset } from '../../store/globalDataStore'
import { presetGrpc, type InvalidPreset } from '../../api/preset-grpc'
import { workflowGrpc, type InvalidWorkflow, type WorkflowResponse } from '../../api/workflow-grpc'
import { normalizeWorkflowRef } from './useWorkflowInputs'
import { formatValueForDisplay, unwrapProtoValue } from '../../lib/paramUtils'
import { usePreferencesStore } from '../../store/preferencesStore'
import { useActiveBuilderChats } from '../../store/chatStoreHooks'
import { ProtoFieldRenderer } from './ProtoFieldRenderer'
import { inputDefToSchema } from '../../lib/nodeFieldAdapter'
import type { InputDef } from '../../lib/inputHelpers'
import { getInputPresetConfig, getInputDefault, getInputDescription, setInputEnumValues } from '../../lib/inputHelpers'
import { useModels } from '../../store/globalDataStore'
import { useThinkingCapability, reconcileThinkingLevel } from '../../hooks/useThinkingCapability'

// =============================================================================
// Types
// =============================================================================

interface WorkflowItem {
  name: string
  description?: string
  source?: 'builtin' | 'user' | 'project'
  is_hidden?: boolean
  has_preset_groups?: boolean // True if workflow has any tags that can have presets
  builderChatId?: string
}

interface ImportConflict {
  slug: string
  existingId: string
  yamlContent: string
}

type TabType = 'workflows' | 'presets'

interface WorkflowHubProps {
  onCreateNew: () => void
  onSelectWorkflow: (workflowName: string) => void
  onDeleteWorkflow?: (workflowName: string) => void
  onImportWorkflow?: (yamlContent: string, overwrite?: boolean) => Promise<{
    success: boolean
    conflict?: boolean
    existingId?: string
    slug?: string
    message?: string
  }>
  onExportWorkflow?: (workflowSlug: string) => Promise<void>
  onForkWorkflow?: (workflowName: string) => Promise<void>
  onToggleVisibility?: (workflowName: string, isHidden: boolean) => Promise<void>
  existingWorkflows?: WorkflowItem[]
  invalidWorkflows?: InvalidWorkflow[]
  isLoading?: boolean
  presets?: Preset[]
  defaultWorkflow?: string
  onSetDefaultWorkflow?: (workflowName: string) => void
  projectId?: string
}

// =============================================================================
// Helpers
// =============================================================================


// =============================================================================
// Tab Button Component
// =============================================================================

interface TabButtonProps {
  active: boolean
  onClick: () => void
  children: React.ReactNode
  count?: number
}

function TabButton({ active, onClick, children, count }: TabButtonProps) {
  return (
    <button
      onClick={onClick}
      className={cn(
        "px-4 py-2 text-sm font-medium rounded-lg transition-colors",
        active
          ? "bg-zinc-800 text-zinc-100 dark:bg-zinc-100 dark:text-zinc-900"
          : "text-muted-foreground hover:text-foreground hover:bg-muted"
      )}
    >
      {children}
      {count !== undefined && (
        <span className={cn(
          "ml-2 px-1.5 py-0.5 text-xs rounded-full",
          active ? "bg-white/20 dark:bg-black/20" : "bg-muted-foreground/20"
        )}>
          {count}
        </span>
      )}
    </button>
  )
}

// =============================================================================
// Workflow Card Component
// =============================================================================

interface WorkflowCardProps {
  name: string
  displayName: string
  description?: string
  source?: 'builtin' | 'user' | 'project'
  isDefaultWorkflow?: boolean
  isHidden?: boolean
  presetDefaults?: Record<string, string>
  isBuilderActive?: boolean
  onClick: () => void
  onDelete?: () => void
  onExport?: () => void
  onCopy?: () => void
  onSetDefault?: () => void
  onConfigurePresets?: () => void
  onToggleVisibility?: () => void
}

function WorkflowCard({
  displayName,
  description,
  source,
  isDefaultWorkflow,
  isHidden,
  presetDefaults,
  isBuilderActive,
  onClick,
  onDelete,
  onExport,
  onCopy,
  onSetDefault,
  onConfigurePresets,
  onToggleVisibility
}: WorkflowCardProps) {
  const isBuiltin = source === 'builtin'
  const isProject = source === 'project'
  const isUser = source === 'user'

  // Format preset defaults for display
  const presetDisplay = useMemo(() => {
    if (!presetDefaults || Object.keys(presetDefaults).length === 0) return null

    const entries = Object.entries(presetDefaults)
    if (entries.length === 1 && entries[0][0] === '') {
      // Single workflow-level preset
      const val = entries[0][1]
      return typeof val === 'string' ? val : JSON.stringify(val)
    }
    // Multiple group presets - show count
    return `${entries.length} groups`
  }, [presetDefaults])

  return (
    <div
      onClick={onClick}
      className={cn(
        "group relative p-4 rounded-xl border cursor-pointer transition-all duration-200",
        "hover:shadow-md hover:border-primary/30",
        isDefaultWorkflow
          ? "border-amber-500/50 bg-amber-500/5"
          : "border-border bg-card"
      )}
    >
      {/* Default workflow star badge */}
      {isDefaultWorkflow && (
        <div className="absolute -top-2 -right-2 bg-amber-500 text-amber-950 rounded-full p-1.5 shadow-sm">
          <Star className="w-3 h-3 fill-current" />
        </div>
      )}

      {/* Activity indicator - shows when the workflow builder assistant is active */}
      {isBuilderActive && (
        <Tooltip content="Builder assistant is editing this workflow">
          <div className="absolute bottom-3 right-3 flex items-center gap-1.5 px-2 py-1 rounded-full bg-violet-500/10 text-violet-600 text-xs font-medium">
            <Loader2 className="w-3 h-3 animate-spin" />
            <span>Editing</span>
          </div>
        </Tooltip>
      )}

      <div className="flex items-start gap-3">
        {/* Icon */}
        <div className={cn(
          "w-10 h-10 rounded-lg flex items-center justify-center flex-shrink-0",
          isBuiltin ? "bg-blue-500/10 text-blue-500" :
          isProject ? "bg-emerald-500/10 text-emerald-500" :
          "bg-violet-500/10 text-violet-500"
        )}>
          <WorkflowIcon className="w-5 h-5" />
        </div>

        {/* Content */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h3 className="font-medium text-foreground">{displayName}</h3>
            {isHidden && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-500/10 text-zinc-500 font-medium uppercase flex items-center gap-1">
                <EyeOff className="w-2.5 h-2.5" />
                Hidden
              </span>
            )}
          </div>

          {description && (
            <p className="text-sm text-muted-foreground mt-1 line-clamp-2">{description}</p>
          )}

          {/* Badges row */}
          <div className="mt-2 flex items-center gap-2 flex-wrap">
            <span className={cn(
              "text-[10px] px-2 py-0.5 rounded-full font-medium",
              isBuiltin ? "bg-blue-500/10 text-blue-600" :
              isProject ? "bg-emerald-500/10 text-emerald-600" :
              "bg-violet-500/10 text-violet-600"
            )}>
              {isBuiltin ? 'Built-in' : isProject ? 'Project' : 'Custom'}
            </span>

            {presetDisplay && (
              <span className="text-[10px] px-2 py-0.5 rounded-full bg-primary/10 text-primary font-medium">
                Preset: {presetDisplay}
              </span>
            )}
          </div>
        </div>

      </div>

      {/* Hover actions */}
      <div className="absolute top-3 right-3 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
        {onConfigurePresets && (
          <Tooltip content="Configure presets">
            <button
              onClick={(e) => { e.stopPropagation(); onConfigurePresets(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-primary transition-colors"
            >
              <Settings2 className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {onSetDefault && !isDefaultWorkflow && (
          <Tooltip content="Set as default workflow">
            <button
              onClick={(e) => { e.stopPropagation(); onSetDefault(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-amber-500 transition-colors"
            >
              <Star className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {onToggleVisibility && (
          <Tooltip content={isHidden ? "Show in dropdown" : "Hide from dropdown"}>
            <button
              onClick={(e) => { e.stopPropagation(); onToggleVisibility(); }}
              className={cn(
                "p-1.5 rounded-md bg-background/80 border border-border/50 transition-colors",
                isHidden
                  ? "text-zinc-500 hover:bg-zinc-500/10"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground"
              )}
            >
              {isHidden ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
            </button>
          </Tooltip>
        )}
        {onCopy && (
          <Tooltip content="Copy workflow">
            <button
              onClick={(e) => { e.stopPropagation(); onCopy(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-foreground transition-colors"
            >
              <Copy className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {isUser && onExport && (
          <Tooltip content="Export">
            <button
              onClick={(e) => { e.stopPropagation(); onExport(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-foreground transition-colors"
            >
              <Download className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {isUser && onDelete && (
          <Tooltip content="Delete">
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-destructive/10 border border-border/50 text-muted-foreground hover:text-destructive transition-colors"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}

// =============================================================================
// Preset Card Component
// =============================================================================

interface PresetCardProps {
  preset: Preset
  isHidden?: boolean
  onClick?: () => void
  onEdit?: () => void
  onDelete?: () => void
  onCopy?: () => void
  onToggleVisibility?: () => void
}

function PresetCard({ preset, isHidden, onClick, onEdit, onDelete, onCopy, onToggleVisibility }: PresetCardProps) {
  const isBuiltin = preset.source === 'builtin'
  const isProject = preset.source === 'project'
  const isEditable = preset.source === 'user'
  
  // Safe accessors to prevent crashes if data is malformed
  const safeName = typeof preset.name === 'string' ? preset.name : JSON.stringify(preset.name)
  const safeDesc = typeof preset.description === 'string' ? preset.description : ''
  const safeTag = typeof preset.tag === 'string' ? preset.tag : (preset.tag ? JSON.stringify(preset.tag) : undefined)

  return (
    <div
      onClick={onClick}
      className={cn(
        "group relative p-4 rounded-xl border transition-all duration-200",
        "hover:shadow-md hover:border-primary/30 border-border bg-card",
        onClick && "cursor-pointer"
      )}
    >
      <div className="flex items-start gap-3">
        {/* Icon */}
        <div className={cn(
          "w-10 h-10 rounded-lg flex items-center justify-center flex-shrink-0",
          isBuiltin ? "bg-blue-500/10 text-blue-500" :
          isProject ? "bg-emerald-500/10 text-emerald-500" :
          "bg-violet-500/10 text-violet-500"
        )}>
          <Layers className="w-5 h-5" />
        </div>

        {/* Content */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h3 className="font-medium text-foreground">{safeName}</h3>
            {isHidden && (
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-zinc-500/10 text-zinc-500 font-medium uppercase flex items-center gap-1">
                <EyeOff className="w-2.5 h-2.5" />
                Hidden
              </span>
            )}
          </div>

          {safeDesc && (
            <p className="text-sm text-muted-foreground mt-1 line-clamp-2">{safeDesc}</p>
          )}

          {/* Badges row */}
          <div className="mt-2 flex items-center gap-2 flex-wrap">
            <span className={cn(
              "text-[10px] px-2 py-0.5 rounded-full font-medium",
              isBuiltin ? "bg-blue-500/10 text-blue-600" :
              isProject ? "bg-emerald-500/10 text-emerald-600" :
              "bg-violet-500/10 text-violet-600"
            )}>
              {isBuiltin ? 'Built-in' : isProject ? 'Project' : 'Custom'}
            </span>

            {safeTag && (
              <span className="text-[10px] px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-medium font-mono">
                {safeTag}
              </span>
            )}
          </div>
        </div>

      </div>

      {/* Hover actions */}
      <div className="absolute top-3 right-3 flex items-center gap-1 opacity-0 group-hover:opacity-100 transition-opacity">
        {/* View/expand button */}
        {onClick && (
          <Tooltip content="View details">
            <button
              onClick={(e) => { e.stopPropagation(); onClick(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-foreground transition-colors"
            >
              <Expand className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {/* Copy button for builtin presets */}
        {isBuiltin && onCopy && (
          <Tooltip content="Copy to new preset">
            <button
              onClick={(e) => { e.stopPropagation(); onCopy(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-foreground transition-colors"
            >
              <Copy className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {/* Visibility toggle */}
        {onToggleVisibility && (
          <Tooltip content={isHidden ? "Show in preset picker" : "Hide from preset picker"}>
            <button
              onClick={(e) => { e.stopPropagation(); onToggleVisibility(); }}
              className={cn(
                "p-1.5 rounded-md bg-background/80 border border-border/50 transition-colors",
                isHidden
                  ? "text-zinc-500 hover:bg-zinc-500/10"
                  : "text-muted-foreground hover:bg-muted hover:text-foreground"
              )}
            >
              {isHidden ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
            </button>
          </Tooltip>
        )}
        {/* Edit button for editable presets */}
        {isEditable && onEdit && (
          <Tooltip content="Edit preset">
            <button
              onClick={(e) => { e.stopPropagation(); onEdit(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-primary transition-colors"
            >
              <Pencil className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
        {/* Delete button for editable presets */}
        {isEditable && onDelete && (
          <Tooltip content="Delete preset">
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-destructive/10 border border-border/50 text-muted-foreground hover:text-destructive transition-colors"
            >
              <Trash2 className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
      </div>
    </div>
  )
}

// =============================================================================
// Invalid Item Card Component (for workflows/presets that failed to load)
// =============================================================================

interface InvalidItemCardProps {
  name: string
  source: 'builtin' | 'project' | 'user'
  path: string
  errors: string[]
  type: 'workflow' | 'preset'
}

function InvalidItemCard({ name, source, path, errors, type }: InvalidItemCardProps) {
  const isBuiltin = source === 'builtin'
  const isProject = source === 'project'

  return (
    <div className="group relative p-4 rounded-xl border border-destructive/30 bg-destructive/5 transition-all duration-200">
      <div className="flex items-start gap-3">
        {/* Icon */}
        <div className="w-10 h-10 rounded-lg flex items-center justify-center flex-shrink-0 bg-destructive/10 text-destructive">
          <AlertTriangle className="w-5 h-5" />
        </div>

        {/* Content */}
        <div className="flex-1 min-w-0">
          <div className="flex items-center gap-2 flex-wrap">
            <h3 className="font-medium text-foreground">{name}</h3>
            <span className="text-[10px] px-1.5 py-0.5 rounded bg-destructive/10 text-destructive font-medium uppercase">
              Invalid
            </span>
          </div>

          {/* Path */}
          <p className="text-xs text-muted-foreground mt-1 font-mono truncate" title={path}>
            {path}
          </p>

          {/* Errors */}
          <div className="mt-2 space-y-1">
            {errors.map((error, i) => (
              <p key={i} className="text-xs text-destructive">
                • {typeof error === 'string' ? error : JSON.stringify(error)}
              </p>
            ))}
          </div>

          {/* Fix instructions */}
          <p className="text-xs text-muted-foreground mt-2 italic">
            Unable to parse {type}. Please fix the YAML file directly or use the Workflow Assistant to resolve this error.
          </p>

          {/* Source badge */}
          <div className="mt-2">
            <span className={cn(
              "text-[10px] px-2 py-0.5 rounded-full font-medium",
              isBuiltin ? "bg-blue-500/10 text-blue-600" :
              isProject ? "bg-emerald-500/10 text-emerald-600" :
              "bg-violet-500/10 text-violet-600"
            )}>
              {isBuiltin ? 'Built-in' : isProject ? 'Project' : 'Custom'} {type}
            </span>
          </div>
        </div>
      </div>
    </div>
  )
}

// =============================================================================
// Empty State Component
// =============================================================================

function EmptyState({ onAction, type = 'workflows' }: { onAction?: () => void, type?: 'workflows' | 'presets' }) {
  if (type === 'presets') {
    return (
      <div className="flex flex-col items-center justify-center py-12 px-4 text-center border-2 border-dashed border-border/50 rounded-xl bg-muted/20">
        <div className="w-12 h-12 rounded-xl bg-muted/50 flex items-center justify-center mb-3 text-muted-foreground">
          <Layers className="w-6 h-6" />
        </div>
        <h3 className="text-sm font-medium text-foreground mb-1">No custom presets yet</h3>
        <p className="text-xs text-muted-foreground">Create presets from the chat page when configuring workflows</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col items-center justify-center py-12 px-4 text-center border-2 border-dashed border-border/50 rounded-xl bg-muted/20">
      <div className="w-12 h-12 rounded-xl bg-muted/50 flex items-center justify-center mb-3 text-muted-foreground">
        <Sparkles className="w-6 h-6" />
      </div>
      <h3 className="text-sm font-medium text-foreground mb-1">No custom workflows yet</h3>
      <p className="text-xs text-muted-foreground mb-4">Create your first workflow or copy a built-in one</p>
      {onAction && (
        <Button onClick={onAction} size="sm" variant="outline" leftIcon={<Plus className="w-3.5 h-3.5" />}>
          Create Workflow
        </Button>
      )}
    </div>
  )
}

// =============================================================================
// Preset Config Modal
// =============================================================================

interface PresetConfigModalProps {
  workflowName: string
  projectId: string
  availablePresets: Preset[]
  onSave: () => void
  onClose: () => void
}

interface GroupInfo {
  name: string  // "" for top-level
  label: string
  tag?: string
}

function PresetConfigModal({
  workflowName,
  projectId,
  availablePresets,
  onSave,
  onClose
}: PresetConfigModalProps) {
  const [groups, setGroups] = useState<GroupInfo[]>([])
  const [selectedPresets, setSelectedPresets] = useState<Record<string, string>>({})
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const { preferences, loadPreferences } = usePreferencesStore()

  // Ensure preferences are loaded when modal opens
  useEffect(() => {
    if (!preferences) {
      loadPreferences()
    }
  }, [preferences, loadPreferences])

  // Load workflow definition and current defaults
  useEffect(() => {
    const load = async () => {
      setIsLoading(true)
      try {
        // Wait for preferences to be loaded if they're not already
        if (!preferences) {
          await loadPreferences()
        }

        // Fetch workflow definition to get groups
        const result = await workflowGrpc.getWorkflow(projectId, { name: workflowName })
        const workflow = result.workflow
        if (!workflow) {
          throw new Error('Workflow not found')
        }

        const groupInfos: GroupInfo[] = []

        // Add top-level if it has a tag
        if (workflow.presets?.tag) {
          groupInfos.push({ name: "", label: "Top-level", tag: workflow.presets.tag })
        }

        // Add named groups (inputs with type: "group")
        const workflowInputs = workflow.inputs
        if (workflowInputs) {
          for (const [groupName, param] of Object.entries(workflowInputs)) {
            const presetCfg = getInputPresetConfig(param as InputDef)
            if (param.type === "group" && presetCfg?.tag) {
              groupInfos.push({ name: groupName, label: groupName, tag: presetCfg.tag })
            }
          }
        }

        setGroups(groupInfos)

        // Fetch current defaults
        const defaults = await presetGrpc.getDefaultPresets(projectId, workflowName)
        setSelectedPresets(defaults)
      } catch (error) {
        console.error('Failed to load workflow config:', error)
        toast.error('Failed to load workflow configuration')
      } finally {
        setIsLoading(false)
      }
    }
    load()
  }, [projectId, workflowName, preferences, loadPreferences])

  const handleSave = async () => {
    setIsSaving(true)
    try {
      // Save each group's default
      for (const group of groups) {
        const presetName = selectedPresets[group.name] || null
        await presetGrpc.setDefaultPreset(projectId, workflowName, group.name, presetName)
      }
      onSave()
      onClose()
      toast.success('Default presets saved')
    } catch {
      toast.error('Failed to save preset configuration')
    } finally {
      setIsSaving(false)
    }
  }

  // Get presets that match a tag
  const getPresetsForTag = (tag?: string): Preset[] => {
    if (!tag) return []
    return availablePresets.filter(p => p.tag === tag)
  }

  const displayName = normalizeWorkflowRef(workflowName)

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title={`Default Presets: ${displayName}`}
      size="md"
    >
      <div className="space-y-4">
        <p className="text-sm text-muted-foreground">
          Select the default preset for each group. These presets will be automatically applied when starting a new chat.
        </p>

        {isLoading ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            Loading...
          </div>
        ) : groups.length === 0 ? (
          <div className="py-4 text-center text-sm text-muted-foreground">
            This workflow has no configurable preset groups.
          </div>
        ) : (
          <div className="space-y-4">
            {groups.map(group => {
              const groupPresets = getPresetsForTag(group.tag)
              return (
                <div key={group.name || '_toplevel'}>
                  <label className="block text-sm font-medium text-foreground mb-2">
                    {group.label}
                    {group.tag && (
                      <span className="ml-2 text-xs text-muted-foreground font-normal">
                        (tag: {typeof group.tag === 'string' ? group.tag : JSON.stringify(group.tag)})
                      </span>
                    )}
                  </label>
                  <select
                    value={selectedPresets[group.name] || ''}
                    onChange={(e) => setSelectedPresets(prev => ({
                      ...prev,
                      [group.name]: e.target.value
                    }))}
                    className="w-full px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                  >
                    <option value="">No default (use system default)</option>
                    {groupPresets.map(preset => (
                      <option key={preset.name} value={preset.name}>
                        {preset.name} ({preset.source})
                      </option>
                    ))}
                  </select>
                </div>
              )
            })}
          </div>
        )}

        <div className="flex justify-end gap-2 pt-4 border-t border-border">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={handleSave} disabled={isSaving || isLoading}>
            {isSaving ? 'Saving...' : 'Save'}
          </Button>
        </div>
      </div>
    </Modal>
  )
}

// =============================================================================
// Preset Edit Modal
// =============================================================================

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
                            className="p-2 hover:bg-destructive/10 text-muted-foreground hover:text-destructive rounded-md transition-colors"
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

function PresetEditModal({ preset, projectId, availablePresets = [], onSave, onClose }: PresetEditModalProps) {
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
    setParams(prev => ({
      ...prev,
      [paramToAdd]: getInputDefault(schema) ?? ''
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

// =============================================================================
// Preset View Modal
// =============================================================================

interface PresetViewModalProps {
  preset: Preset
  projectId: string
  onCopy: () => void
  onClose: () => void
}

function PresetViewModal({ preset, projectId, onCopy, onClose }: PresetViewModalProps) {
  const [isCopying, setIsCopying] = useState(false)
  const [copyName, setCopyName] = useState(`my-${preset.name}`)

  const handleCopy = async () => {
    if (!copyName.trim()) return

    setIsCopying(true)
    try {
      const result = await presetGrpc.createPreset(projectId, {
        name: copyName.trim(),
        description: preset.description || `Copy of ${preset.name}`,
        params: preset.params,
        tag: preset.tag,
      })

      if (result.success) {
        toast.success(`Created preset "${copyName}"`)
        onCopy()
        onClose()
      } else {
        toast.error(result.error || 'Failed to create preset')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to create preset')
    } finally {
      setIsCopying(false)
    }
  }

  const systemPrompt = preset.params?.system_prompt as string | undefined
  const otherParams = Object.entries(preset.params || {}).filter(([k]) => k !== 'system_prompt')

  return (
    <Modal
      isOpen={true}
      onClose={onClose}
      title={preset.name}
      size="lg"
      hideCloseButton={true}
      headerActions={
        <div className="flex items-center gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>
            Close
          </Button>
        </div>
      }
    >
      <div className="space-y-6 pb-2">
        {/* Header Badges */}
        <div className="flex items-center gap-2">
          <span className={cn(
            "text-xs px-2 py-0.5 rounded-full font-medium",
            preset.source === 'builtin' ? "bg-blue-500/10 text-blue-600" :
            preset.source === 'project' ? "bg-emerald-500/10 text-emerald-600" :
            "bg-violet-500/10 text-violet-600"
          )}>
            {preset.source === 'builtin' ? 'Built-in' : preset.source === 'project' ? 'Project' : 'Custom'}
          </span>
          {preset.tag && (
            <span className="text-xs px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-medium font-mono">
              {typeof preset.tag === 'string' ? preset.tag : JSON.stringify(preset.tag)}
            </span>
          )}
        </div>

        {/* Description */}
        {preset.description && (
          <div>
            <h3 className="text-sm font-medium text-foreground mb-1.5">Description</h3>
            <p className="text-sm text-muted-foreground leading-relaxed">{preset.description}</p>
          </div>
        )}

        {/* System Prompt - Featured */}
        {systemPrompt && (
          <div>
            <div className="flex items-center justify-between mb-2">
              <h3 className="text-sm font-medium text-foreground flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full bg-primary/70" />
                System Prompt
              </h3>
              <Button
                variant="ghost"
                size="sm"
                className="h-6 w-6 p-0"
                onClick={() => {
                  navigator.clipboard.writeText(String(systemPrompt))
                  toast.success('System prompt copied to clipboard')
                }}
              >
                <Copy className="w-3.5 h-3.5" />
                <span className="sr-only">Copy system prompt</span>
              </Button>
            </div>
            <div className="bg-muted/50 text-foreground p-4 rounded-lg font-mono text-xs whitespace-pre-wrap max-h-[400px] overflow-y-auto border border-border/50 shadow-inner">
              {String(systemPrompt)}
            </div>
          </div>
        )}

        {/* Other Parameters */}
        {otherParams.length > 0 && (
          <div>
            <h3 className="text-sm font-medium text-foreground mb-3">Parameters</h3>
            <div className="grid gap-3 sm:grid-cols-2">
              {otherParams.map(([key, value]) => (
                <div key={key} className="bg-muted/30 border border-border/50 rounded-lg p-3">
                  <div className="text-xs font-medium text-muted-foreground uppercase tracking-wider mb-1.5">{key}</div>
                  <div className="font-mono text-xs text-foreground break-all">
                    {Array.isArray(value) ? (
                      <div className="flex flex-wrap gap-1.5">
                        {value.map((item: any, i: number) => (
                          <span key={i} className="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-muted text-muted-foreground">
                            {formatValueForDisplay(item)}
                          </span>
                        ))}
                      </div>
                    ) : typeof value === 'object' && value !== null ? (
                      <pre className="whitespace-pre-wrap text-[10px] bg-background/50 p-2 rounded border border-border/50">{JSON.stringify(unwrapProtoValue(value as any) ?? value, null, 2)}</pre>
                    ) : (
                      <span className="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium bg-muted text-muted-foreground">
                        {formatValueForDisplay(value)}
                      </span>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Copy Section (Built-in only) */}
        {preset.source === 'builtin' && (
          <div className="bg-muted/30 -mx-6 -mb-6 px-6 py-4 border-t border-border/60 mt-8 flex flex-col gap-3">
            <div>
              <h4 className="text-sm font-medium text-foreground">Create Editable Copy</h4>
              <p className="text-xs text-muted-foreground mt-0.5">Start with this preset's configuration to create your own.</p>
            </div>
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
          </div>
        )}
      </div>
    </Modal>
  )
}

// =============================================================================
// Main Component
// =============================================================================

export function WorkflowHub({
  onCreateNew,
  onSelectWorkflow,
  onDeleteWorkflow,
  onImportWorkflow,
  onExportWorkflow,
  onForkWorkflow: _onForkWorkflow,
  onToggleVisibility,
  existingWorkflows = [],
  invalidWorkflows = [],
  isLoading = false,
  presets: _propPresets = [],
  defaultWorkflow,
  onSetDefaultWorkflow,
  projectId
}: WorkflowHubProps) {
  const [activeTab, setActiveTab] = useState<TabType>('workflows')
  const [importConflict, setImportConflict] = useState<ImportConflict | null>(null)
  const [newWorkflowName, setNewWorkflowName] = useState('')
  const [isImporting, setIsImporting] = useState(false)
  const [presetConfigWorkflow, setPresetConfigWorkflow] = useState<string | null>(null)
  const [editingPreset, setEditingPreset] = useState<Preset | null>(null)
  const [viewingPreset, setViewingPreset] = useState<Preset | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  // Track preset defaults per workflow (fetched from backend, includes both system and user defaults)
  const [presetDefaults, setPresetDefaults] = useState<Record<string, Record<string, string>>>({})

  // Local presets state - fetched with includeHidden=true for management UI
  const [presets, setPresets] = useState<Preset[]>(_propPresets)
  const [invalidPresets, setInvalidPresets] = useState<InvalidPreset[]>([])

  // Fetch presets with includeHidden for management view
  const refreshPresets = useCallback(async () => {
    if (!projectId) return
    try {
      const result = await presetGrpc.listPresetsWithErrors(projectId, true) // includeHidden for management
      setPresets(result.presets)
      setInvalidPresets(result.invalidPresets)
    } catch (err) {
      console.error('Failed to load presets for hub:', err)
    }
  }, [projectId])

  // Load presets on mount and when projectId changes
  useEffect(() => {
    refreshPresets()
  }, [refreshPresets])

  // Function to refresh preset defaults from backend (includes merged system + user defaults)
  const refreshPresetDefaults = useCallback(async () => {
    if (!projectId || existingWorkflows.length === 0) return

    const defaults: Record<string, Record<string, string>> = {}

    // Fetch default presets for all workflows from the backend API.
    // The backend returns merged system + user defaults.
    await Promise.all(
      existingWorkflows.map(async (workflow) => {
        try {
          const groupDefaults = await presetGrpc.getDefaultPresets(projectId, workflow.name)
          if (Object.keys(groupDefaults).length > 0) {
            defaults[workflow.name] = groupDefaults
          }
        } catch {
          // Ignore errors - workflow may not support presets
        }
      })
    )

    setPresetDefaults(defaults)
  }, [projectId, existingWorkflows])

  // Fetch preset defaults when workflows change
  useEffect(() => {
    refreshPresetDefaults()
  }, [refreshPresetDefaults])

  // Sort and categorize workflows
  const { customWorkflows, builtinWorkflows } = useMemo(() => {
    const custom = existingWorkflows
      .filter(w => w.source === 'user' || w.source === 'project')
      .sort((a, b) => normalizeWorkflowRef(a.name).localeCompare(normalizeWorkflowRef(b.name)))
    const builtin = existingWorkflows
      .filter(w => w.source === 'builtin')
      .sort((a, b) => normalizeWorkflowRef(a.name).localeCompare(normalizeWorkflowRef(b.name)))
    return { customWorkflows: custom, builtinWorkflows: builtin }
  }, [existingWorkflows])

  // Build map of workflow name -> builder chat ID for active-editing indicators
  const builderChatIds = useMemo(() => {
    const map = new Map<string, string>()
    for (const w of existingWorkflows) {
      if (w.builderChatId) map.set(w.name, w.builderChatId)
    }
    return map
  }, [existingWorkflows])
  const activeBuilderWorkflows = useActiveBuilderChats(builderChatIds)

  // Sort and categorize presets
  const { customPresets, builtinPresets } = useMemo(() => {
    const custom = presets
      .filter(p => p.source === 'user' || p.source === 'project')
      .sort((a, b) => a.name.localeCompare(b.name))
    const builtin = presets
      .filter(p => p.source === 'builtin')
      .sort((a, b) => a.name.localeCompare(b.name))
    return { customPresets: custom, builtinPresets: builtin }
  }, [presets])

  // Handlers
  const handleDeleteWorkflow = async (workflowName: string) => {
    if (!onDeleteWorkflow) return
    const displayName = normalizeWorkflowRef(workflowName)
    if (!window.confirm(`Delete "${displayName}"? This cannot be undone.`)) return

    try {
      await onDeleteWorkflow(workflowName)
      toast.success(`Deleted "${displayName}"`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete')
    }
  }

  const handleExportWorkflow = async (workflowName: string) => {
    if (!onExportWorkflow) return
    try {
      await onExportWorkflow(workflowName)
      toast.success('Exported successfully')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to export')
    }
  }

  const handleCopyWorkflow = async (workflowName: string) => {
    if (!projectId) return
    const displayName = normalizeWorkflowRef(workflowName)
    try {
      const result = await workflowGrpc.copyWorkflow(projectId, workflowName)
      if (result.success) {
        toast.success(`Created "${result.slug}" from "${displayName}"`)
        // Refresh workflow list
        window.location.reload() // TODO: Better refresh mechanism
      } else {
        toast.error(result.message || 'Failed to copy workflow')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to copy workflow')
    }
  }

  const handleFileSelect = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0]
    if (!file || !onImportWorkflow) return
    event.target.value = ''

    try {
      setIsImporting(true)
      const yamlContent = await file.text()
      const result = await onImportWorkflow(yamlContent, false)

      if (result.conflict) {
        // Generate unique name with random suffix
        const randomSuffix = Math.random().toString(36).substring(2, 8)
        setNewWorkflowName(`${result.slug || 'workflow'}-${randomSuffix}`)
        setImportConflict({
          slug: result.slug || 'unknown',
          existingId: result.existingId || '',
          yamlContent,
        })
      } else if (result.success) {
        toast.success('Imported successfully')
      } else {
        toast.error(result.message || 'Failed to import')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to import')
    } finally {
      setIsImporting(false)
    }
  }

  const handleConflictReplace = async () => {
    if (!importConflict || !onImportWorkflow) return
    try {
      setIsImporting(true)
      const result = await onImportWorkflow(importConflict.yamlContent, true)
      if (result.success) {
        toast.success('Replaced successfully')
        setImportConflict(null)
      } else {
        toast.error(result.message || 'Failed to replace')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to replace')
    } finally {
      setIsImporting(false)
    }
  }

  const handleConflictSaveAsNew = async () => {
    if (!importConflict || !onImportWorkflow || !newWorkflowName.trim()) return
    try {
      setIsImporting(true)
      const modifiedYaml = importConflict.yamlContent.replace(
        /^name:\s*.+$/m,
        `name: ${newWorkflowName.trim()}`
      )
      const result = await onImportWorkflow(modifiedYaml, false)
      if (result.success) {
        toast.success('Saved as new workflow')
        setImportConflict(null)
      } else if (result.conflict) {
        toast.error('Name already exists, try a different name')
      } else {
        toast.error(result.message || 'Failed to save')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save')
    } finally {
      setIsImporting(false)
    }
  }

  const handleSetDefault = async (workflowName: string) => {
    if (!onSetDefaultWorkflow) return
    try {
      await onSetDefaultWorkflow(workflowName)
      toast.success(`Set "${normalizeWorkflowRef(workflowName)}" as default`)
    } catch (err) {
      toast.error('Failed to set default')
    }
  }

  const handleDeletePreset = async (preset: Preset) => {
    if (!projectId || preset.source === 'builtin') return
    if (!window.confirm(`Delete preset "${preset.name}"? This cannot be undone.`)) return

    try {
      // Use preset.name for the API call (works for both user and project presets)
      const result = await presetGrpc.deletePreset(projectId, preset.name)
      if (result.success) {
        toast.success(`Deleted "${preset.name}"`)
        refreshPresets() // Refresh the management list
      } else {
        toast.error(result.error || 'Failed to delete preset')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete preset')
    }
  }

  // Get preset defaults for a workflow (fetched from backend, includes merged system + user defaults)
  const getPresetDefaults = (workflowName: string): Record<string, string> | undefined => {
    return presetDefaults[workflowName]
  }

  // Get hidden state and toggle function from preferences store
  const { isWorkflowHidden, toggleWorkflowVisibility, isPresetHidden, togglePresetVisibility } = usePreferencesStore()

  const handleToggleVisibility = async (workflow: WorkflowItem) => {
    const displayName = normalizeWorkflowRef(workflow.name)
    
    // For user workflows, use the API (which stores in DB)
    if (workflow.source === 'user' && onToggleVisibility) {
      try {
        const newHiddenState = !workflow.is_hidden
        await onToggleVisibility(workflow.name, newHiddenState)
        toast.success(newHiddenState ? `"${displayName}" hidden from dropdown` : `"${displayName}" visible in dropdown`)
      } catch (err) {
        toast.error(err instanceof Error ? err.message : 'Failed to update visibility')
      }
      return
    }
    
    // For builtin/project workflows, use preferences store
    try {
      const currentlyHidden = isWorkflowHidden(workflow.name)
      await toggleWorkflowVisibility(workflow.name)
      toast.success(!currentlyHidden ? `"${displayName}" hidden from dropdown` : `"${displayName}" visible in dropdown`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to update visibility')
    }
  }

  // Determine if a workflow is hidden (from DB for user workflows, from preferences for others)
  const getIsHidden = (workflow: WorkflowItem): boolean => {
    if (workflow.source === 'user') {
      return workflow.is_hidden || false
    }
    return isWorkflowHidden(workflow.name)
  }

  // Render workflow card
  const renderWorkflowCard = (workflow: WorkflowItem) => {
    // A workflow can have presets configured if:
    // 1. Backend says it has preset groups (has tags on workflow or groups)
    // 2. OR it has system default presets configured (fallback for backwards compat)
    const hasPresetSupport = workflow.has_preset_groups || !!getPresetDefaults(workflow.name)

    return (
      <WorkflowCard
        key={workflow.name}
        name={workflow.name}
        displayName={normalizeWorkflowRef(workflow.name)}
        description={workflow.description}
        source={workflow.source}
        isDefaultWorkflow={defaultWorkflow === workflow.name}
        isHidden={getIsHidden(workflow)}
        presetDefaults={getPresetDefaults(workflow.name)}
        isBuilderActive={activeBuilderWorkflows.has(workflow.name)}
        onClick={() => onSelectWorkflow(workflow.name)}
        onDelete={workflow.source === 'user' ? () => handleDeleteWorkflow(workflow.name) : undefined}
        onExport={workflow.source === 'user' && onExportWorkflow ? () => handleExportWorkflow(workflow.name) : undefined}
        onCopy={projectId ? () => handleCopyWorkflow(workflow.name) : undefined}
        onSetDefault={onSetDefaultWorkflow ? () => handleSetDefault(workflow.name) : undefined}
        onConfigurePresets={hasPresetSupport ? () => setPresetConfigWorkflow(workflow.name) : undefined}
        onToggleVisibility={() => handleToggleVisibility(workflow)}
      />
    )
  }

  // Handle preset visibility toggle
  const handleTogglePresetVisibility = async (preset: Preset) => {
    try {
      const currentlyHidden = isPresetHidden(preset.name)
      await togglePresetVisibility(preset.name)
      toast.success(!currentlyHidden ? `"${preset.name}" hidden from preset picker` : `"${preset.name}" visible in preset picker`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to update visibility')
    }
  }

  // Render preset card
  const renderPresetCard = (preset: Preset) => (
    <PresetCard
      key={`${preset.source}-${preset.name}`}
      preset={preset}
      isHidden={isPresetHidden(preset.name)}
      onClick={() => setViewingPreset(preset)}
      onEdit={preset.source === 'user' ? () => setEditingPreset(preset) : undefined}
      onDelete={preset.source === 'user' ? () => handleDeletePreset(preset) : undefined}
      onCopy={preset.source === 'builtin' ? () => setViewingPreset(preset) : undefined}
      onToggleVisibility={() => handleTogglePresetVisibility(preset)}
    />
  )

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <div className="flex-shrink-0 px-6 pt-6 pb-4 border-b border-border/50">
        <div className="max-w-[1000px] mx-auto">
          <div className="flex items-center justify-between mb-4">
            <div>
              <h1 className="text-xl font-semibold text-foreground">
                {activeTab === 'workflows' ? 'Workflows' : 'Presets'}
              </h1>
              <p className="text-sm text-muted-foreground mt-0.5">
                {activeTab === 'workflows'
                  ? 'Manage your automation workflows'
                  : 'View and edit your saved presets. Create new presets from the chat page when configuring workflow parameters.'}
                {activeTab === 'workflows' && (
                  <>
                    {' · '}
                    <a
                      href="https://docs.reliantlabs.io/"
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-primary hover:underline"
                    >
                      <BookOpen className="w-3 h-3" />
                      Docs
                    </a>
                  </>
                )}
              </p>
            </div>
            {activeTab === 'workflows' && (
              <div className="flex items-center gap-2">
                {onImportWorkflow && (
                  <>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => fileInputRef.current?.click()}
                      disabled={isImporting}
                      leftIcon={<Upload className="w-4 h-4" />}
                    >
                      Import
                    </Button>
                    <input
                      ref={fileInputRef}
                      type="file"
                      accept=".yaml,.yml"
                      onChange={handleFileSelect}
                      className="hidden"
                    />
                  </>
                )}
                <Button size="sm" onClick={onCreateNew} leftIcon={<Plus className="w-4 h-4" />}>
                  New Workflow
                </Button>
              </div>
            )}
          </div>

          {/* Tabs */}
          <div className="flex items-center gap-2">
            <TabButton
              active={activeTab === 'workflows'}
              onClick={() => setActiveTab('workflows')}
              count={existingWorkflows.length}
            >
              Workflows
            </TabButton>
            <TabButton
              active={activeTab === 'presets'}
              onClick={() => setActiveTab('presets')}
              count={presets.length}
            >
              Presets
            </TabButton>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-6">
        <div className="max-w-[1000px] mx-auto" data-onboarding="workflow-hub">
          {isLoading ? (
            <div className="flex items-center justify-center py-16">
              <div className="flex flex-col items-center gap-3">
                <div className="animate-spin rounded-full h-8 w-8 border-2 border-primary border-t-transparent" />
                <p className="text-sm text-muted-foreground">Loading...</p>
              </div>
            </div>
          ) : activeTab === 'workflows' ? (
            <div className="space-y-8">
              {/* Your Workflows Section */}
              <section>
                <div className="flex items-center justify-between mb-4">
                  <h2 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
                    Your Workflows
                    <span className="ml-2 text-muted-foreground/60">({customWorkflows.length})</span>
                  </h2>
                </div>

                {customWorkflows.length === 0 ? (
                  <EmptyState onAction={onCreateNew} type="workflows" />
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {customWorkflows.map(renderWorkflowCard)}
                  </div>
                )}
              </section>

              {/* Built-in Workflows Section */}
              {builtinWorkflows.length > 0 && (
                <section>
                  <div className="flex items-center justify-between mb-4">
                    <h2 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
                      Built-in Workflows
                      <span className="ml-2 text-muted-foreground/60">({builtinWorkflows.length})</span>
                    </h2>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {builtinWorkflows.map(renderWorkflowCard)}
                  </div>
                </section>
              )}

              {/* Invalid Workflows Section - only show if there are invalid items */}
              {invalidWorkflows.length > 0 && (
                <section>
                  <div className="flex items-center justify-between mb-4">
                    <h2 className="text-sm font-medium text-destructive uppercase tracking-wider flex items-center gap-2">
                      <AlertTriangle className="w-4 h-4" />
                      Failed to Load
                      <span className="text-destructive/60">({invalidWorkflows.length})</span>
                    </h2>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {invalidWorkflows.map((inv) => (
                      <InvalidItemCard
                        key={`${inv.source}-${inv.name}`}
                        name={inv.name}
                        source={inv.source}
                        path={inv.path}
                        errors={inv.errors}
                        type="workflow"
                      />
                    ))}
                  </div>
                </section>
              )}
            </div>
          ) : (
            /* Presets Tab */
            <div className="space-y-8">
              {/* Your Presets Section */}
              <section>
                <div className="flex items-center justify-between mb-4">
                  <h2 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
                    Your Presets
                    <span className="ml-2 text-muted-foreground/60">({customPresets.length})</span>
                  </h2>
                </div>

                {customPresets.length === 0 ? (
                  <EmptyState type="presets" />
                ) : (
                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {customPresets.map(renderPresetCard)}
                  </div>
                )}
              </section>

              {/* Built-in Presets Section */}
              {builtinPresets.length > 0 && (
                <section>
                  <div className="flex items-center justify-between mb-4">
                    <h2 className="text-sm font-medium text-muted-foreground uppercase tracking-wider">
                      Built-in Presets
                      <span className="ml-2 text-muted-foreground/60">({builtinPresets.length})</span>
                    </h2>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {builtinPresets.map(renderPresetCard)}
                  </div>
                </section>
              )}

              {/* Invalid Presets Section - only show if there are invalid items */}
              {invalidPresets.length > 0 && (
                <section>
                  <div className="flex items-center justify-between mb-4">
                    <h2 className="text-sm font-medium text-destructive uppercase tracking-wider flex items-center gap-2">
                      <AlertTriangle className="w-4 h-4" />
                      Failed to Load
                      <span className="text-destructive/60">({invalidPresets.length})</span>
                    </h2>
                  </div>

                  <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
                    {invalidPresets.map((inv) => (
                      <InvalidItemCard
                        key={`${inv.source}-${inv.name}`}
                        name={inv.name}
                        source={inv.source}
                        path={inv.path}
                        errors={inv.errors}
                        type="preset"
                      />
                    ))}
                  </div>
                </section>
              )}
            </div>
          )}
        </div>
      </div>

      {/* Import Conflict Modal */}
      {importConflict && (
        <Modal
          isOpen={true}
          onClose={() => setImportConflict(null)}
          title="Workflow Already Exists"
          size="md"
        >
          <div className="space-y-4">
            <p className="text-sm text-muted-foreground">
              A workflow named <span className="font-mono font-semibold text-foreground">"{importConflict.slug}"</span> already exists.
            </p>

            <div className="space-y-2">
              <label className="block text-sm font-medium text-foreground">
                Save with a new name
              </label>
              <div className="flex gap-2">
                <input
                  type="text"
                  value={newWorkflowName}
                  onChange={(e) => setNewWorkflowName(e.target.value)}
                  placeholder="Enter new name"
                  className="flex-1 px-3 py-2 text-sm border border-border rounded-md bg-background focus:outline-none focus:ring-2 focus:ring-ring/20 focus:border-ring"
                  disabled={isImporting}
                  onKeyDown={(e) => e.key === 'Enter' && handleConflictSaveAsNew()}
                />
                <Button onClick={handleConflictSaveAsNew} disabled={isImporting || !newWorkflowName.trim()}>
                  {isImporting ? 'Saving...' : 'Save'}
                </Button>
              </div>
            </div>

            <div className="flex justify-between items-center pt-4 border-t border-border">
              <Button variant="outline" onClick={() => setImportConflict(null)}>
                Cancel
              </Button>
              <Button variant="destructive" onClick={handleConflictReplace} disabled={isImporting}>
                {isImporting ? 'Replacing...' : 'Replace Existing'}
              </Button>
            </div>
          </div>
        </Modal>
      )}

      {/* Preset Config Modal */}
      {presetConfigWorkflow && projectId && (
        <PresetConfigModal
          workflowName={presetConfigWorkflow}
          projectId={projectId}
          availablePresets={presets}
          onSave={() => {
            // Refresh preset defaults after save
            refreshPresetDefaults()
          }}
          onClose={() => setPresetConfigWorkflow(null)}
        />
      )}

      {/* Preset Edit Modal */}
      {editingPreset && projectId && (
        <PresetEditModal
          preset={editingPreset}
          projectId={projectId}
          availablePresets={presets}
          onSave={() => {
            refreshPresets() // Refresh the management list
          }}
          onClose={() => setEditingPreset(null)}
        />
      )}

      {/* Preset View Modal */}
      {viewingPreset && projectId && (
        <PresetViewModal
          preset={viewingPreset}
          projectId={projectId}
          onCopy={() => {
            refreshPresets() // Refresh the management list
          }}
          onClose={() => setViewingPreset(null)}
        />
      )}
    </div>
  )
}

export default WorkflowHub
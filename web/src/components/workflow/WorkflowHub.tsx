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
  Loader2,
  Play
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
import { useModels, useGlobalDataStore } from '../../store/globalDataStore'
import { DraftStatusBadge } from './DraftStatusBadge'
import { RunWorkflowDialog } from './run/RunWorkflowDialog'
import { splitFindings, type DraftStatus, type Finding } from './workflowDraftStatus'
import { useThinkingCapability, reconcileThinkingLevel } from '../../hooks/useThinkingCapability'

// =============================================================================
// Types
// =============================================================================

interface WorkflowItem {
  name: string
  description?: string
  source?: 'builtin' | 'user' | 'project'
  /** Lifecycle; drafts are never runnable. Absent ⇒ complete (builtin/project). */
  status?: DraftStatus
  /** Current findings, computed on read by the backend. */
  validationErrors?: Finding[]
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
  /** Set for a draft: how many validation errors it currently has. */
  draftErrorCount?: number
  presetDefaults?: Record<string, string>
  isBuilderActive?: boolean
  onClick: () => void
  /** Opens the Run… form. Absent for a workflow that cannot run (a draft). */
  onRun?: () => void
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
  draftErrorCount,
  presetDefaults,
  isBuilderActive,
  onClick,
  onRun,
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
            {draftErrorCount !== undefined && <DraftStatusBadge errorCount={draftErrorCount} />}
            {isHidden && (
              <span className="text-2xs px-1.5 py-0.5 rounded bg-zinc-500/10 text-zinc-500 font-medium uppercase flex items-center gap-1">
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
              "text-2xs px-2 py-0.5 rounded-full font-medium",
              isBuiltin ? "bg-blue-500/10 text-blue-600" :
              isProject ? "bg-emerald-500/10 text-emerald-600" :
              "bg-violet-500/10 text-violet-600"
            )}>
              {isBuiltin ? 'Built-in' : isProject ? 'Project' : 'Custom'}
            </span>

            {presetDisplay && (
              <span className="text-2xs px-2 py-0.5 rounded-full bg-primary/10 text-primary font-medium">
                Preset: {presetDisplay}
              </span>
            )}
          </div>
        </div>

      </div>

      {/* Hover actions. Revealed on keyboard focus too, so Run… and the rest
          are reachable without a pointer. */}
      <div className="absolute top-3 right-3 flex items-center gap-1 opacity-0 group-hover:opacity-100 focus-within:opacity-100 transition-opacity">
        {onRun && (
          <Tooltip content="Run with inputs">
            <button
              type="button"
              aria-label={`Run ${displayName}`}
              onClick={(e) => { e.stopPropagation(); onRun(); }}
              className="p-1.5 rounded-md bg-background/80 hover:bg-muted border border-border/50 text-muted-foreground hover:text-primary transition-colors"
            >
              <Play className="w-3.5 h-3.5" />
            </button>
          </Tooltip>
        )}
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
              <span className="text-2xs px-1.5 py-0.5 rounded bg-zinc-500/10 text-zinc-500 font-medium uppercase flex items-center gap-1">
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
              "text-2xs px-2 py-0.5 rounded-full font-medium",
              isBuiltin ? "bg-blue-500/10 text-blue-600" :
              isProject ? "bg-emerald-500/10 text-emerald-600" :
              "bg-violet-500/10 text-violet-600"
            )}>
              {isBuiltin ? 'Built-in' : isProject ? 'Project' : 'Custom'}
            </span>

            {safeTag && (
              <span className="text-2xs px-2 py-0.5 rounded-full bg-muted text-muted-foreground font-medium font-mono">
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
            <span className="text-2xs px-1.5 py-0.5 rounded bg-destructive/10 text-destructive font-medium uppercase">
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
              "text-2xs px-2 py-0.5 rounded-full font-medium",
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


// =============================================================================
// Preset Edit Modal
// =============================================================================


// =============================================================================
// Preset View Modal
// =============================================================================


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
  const [runWorkflowName, setRunWorkflowName] = useState<string | null>(null)
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

    // ONE request for every workflow on screen. This used to map over the
    // workflow list issuing a GetDefaultPreset each, which is the fan-out the
    // user saw as "dozens of parallel calls, one per agent".
    // The backend returns merged system + user defaults, and omits workflows
    // that have none.
    const defaults = await presetGrpc.getDefaultPresetsBatch(
      projectId,
      existingWorkflows.map((workflow) => workflow.name),
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
        // Refresh the workflow list (signals the parent to reload detailed
        // data) before navigating, otherwise the hub renders empty while the
        // global store catches up.
        window.dispatchEvent(new Event('workflow-saved'))
        try {
          await useGlobalDataStore.getState().refetchWorkflows(projectId)
        } catch {
          // Best-effort; the workflow-saved event triggers a fallback refresh.
        }
        // Open the new copy directly — workflow-hub copy is a navigation
        // affordance, not a passive list mutation.
        if (result.slug) {
          onSelectWorkflow(result.slug)
        }
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
        draftErrorCount={
          workflow.status === 'draft'
            ? splitFindings(workflow.validationErrors ?? []).errors.length
            : undefined
        }
        presetDefaults={getPresetDefaults(workflow.name)}
        isBuilderActive={activeBuilderWorkflows.has(workflow.name)}
        onClick={() => onSelectWorkflow(workflow.name)}
        // A draft cannot run until it validates, so it gets no Run….
        onRun={projectId && workflow.status !== 'draft' ? () => setRunWorkflowName(workflow.name) : undefined}
        onDelete={workflow.source === 'user' ? () => handleDeleteWorkflow(workflow.name) : undefined}
        onExport={workflow.source === 'user' && onExportWorkflow ? () => handleExportWorkflow(workflow.name) : undefined}
        onCopy={projectId ? () => handleCopyWorkflow(workflow.name) : undefined}
        // A default must be runnable, so drafts cannot be made the default.
        onSetDefault={onSetDefaultWorkflow && workflow.status !== 'draft' ? () => handleSetDefault(workflow.name) : undefined}
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
      {projectId && (
        <RunWorkflowDialog
          open={runWorkflowName !== null}
          onClose={() => setRunWorkflowName(null)}
          projectId={projectId}
          workflowRef={runWorkflowName ?? ''}
        />
      )}

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
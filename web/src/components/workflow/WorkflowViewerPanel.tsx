/**
 * WorkflowViewerPanel - Panel wrapper for WorkflowViewer with navigation support
 * 
 * Handles:
 * - Fetching workflow definitions
 * - Navigation stack for drilling into sub-workflows
 * - Breadcrumb display and navigation
 */

import { useState, useEffect, useCallback } from 'react'
import { WorkflowViewer } from './WorkflowViewer'
import { WorkflowBreadcrumb, type BreadcrumbLevel } from './WorkflowBreadcrumb'
import { getWorkflow } from '../../api/workflow-grpc'
import type { Workflow } from '../../types/workflow'
import type { WorkflowExecution, StepExecution } from '../Chat/ExecutionSidebar/types'
import { Loader2 } from 'lucide-react'

/** Navigation stack entry */
interface NavigationLevel {
  /** Unique ID for this level */
  id: string
  /** The workflow definition at this level */
  workflow: Workflow | null
  /** The workflow name (for fetching) */
  workflowName: string
  /** The execution at this level */
  execution?: WorkflowExecution
  /** Display label (node ID that spawned it, or "Root") */
  label: string
  /** Loading state for this level's workflow */
  loading: boolean
  /** Error message if fetch failed */
  error?: string
}

interface WorkflowViewerPanelProps {
  /** Project ID for fetching workflow */
  projectId: string
  /** Chat ID — connects the diagram to the authoritative node_execution stream */
  chatId?: string | null
  /** Workflow name to display */
  workflowName: string
  /** Current execution state (optional) */
  execution?: WorkflowExecution
  /** Callback when panel is closed */
  onClose?: () => void
  /** Callback when a node is clicked */
  onNodeClick?: (nodeId: string, step?: StepExecution) => void
  /** Show in compact mode */
  compact?: boolean
  /** Hide the fullscreen/expand button */
  hideFullscreen?: boolean
  /** Current viewer mode (for inline/side toggle) */
  viewerMode?: 'inline' | 'side'
  /** Callback to toggle between inline and side panel modes */
  onToggleViewerMode?: () => void
  /** Callback when expanded state changes */
  onExpandedChange?: (expanded: boolean) => void
}

export function WorkflowViewerPanel({
  projectId,
  chatId,
  workflowName,
  execution,
  onClose,
  onNodeClick,
  compact = false,
  hideFullscreen = false,
  viewerMode,
  onToggleViewerMode,
  onExpandedChange,
}: WorkflowViewerPanelProps) {
  // Navigation stack - first entry is root, last is current view
  const [navStack, setNavStack] = useState<NavigationLevel[]>([
    {
      id: 'root',
      workflow: null,
      workflowName,
      execution,
      label: 'Root',
      loading: true,
    }
  ])

  // Current level is always the last in the stack
  const currentLevel = navStack[navStack.length - 1]

  // Reset entire stack when execution changes (e.g., switching chats)
  // Use execution.id as the key - it's unique per chat/run even if workflowName is the same
  useEffect(() => {
    setNavStack([{
      id: 'root',
      workflow: null,
      workflowName,
      execution,
      label: 'Root',
      loading: true,
    }])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, execution?.id, workflowName])

  // Update root execution when execution prop changes (same workflow, new data)
  useEffect(() => {
    setNavStack(prev => {
      // Only update if we're still on the same workflow
      if (prev[0]?.workflowName !== workflowName) return prev
      const newStack = [...prev]
      if (newStack[0]) {
        newStack[0] = { ...newStack[0], execution }
      }
      return newStack
    })
  }, [execution, workflowName])

  // Fetch workflow for current level
  useEffect(() => {
    let cancelled = false

    async function fetchWorkflow() {
      if (!projectId || !currentLevel.workflowName) return
      if (currentLevel.workflow || !currentLevel.loading) return

      try {
        const result = await getWorkflow(projectId, currentLevel.workflowName)
        if (!cancelled) {
          setNavStack(prev => {
            const newStack = [...prev]
            const idx = newStack.findIndex(l => l.id === currentLevel.id)
            if (idx >= 0) {
              newStack[idx] = { 
                ...newStack[idx], 
                workflow: result, 
                loading: false,
                error: undefined,
              }
            }
            return newStack
          })
        }
      } catch (err) {
        if (!cancelled) {
          console.error('Failed to fetch workflow:', err)
          setNavStack(prev => {
            const newStack = [...prev]
            const idx = newStack.findIndex(l => l.id === currentLevel.id)
            if (idx >= 0) {
              newStack[idx] = { 
                ...newStack[idx], 
                loading: false,
                error: err instanceof Error ? err.message : 'Failed to load workflow',
              }
            }
            return newStack
          })
        }
      }
    }

    fetchWorkflow()

    return () => {
      cancelled = true
    }
  }, [projectId, currentLevel.id, currentLevel.workflowName, currentLevel.workflow, currentLevel.loading])

  // Handle drilling into a sub-workflow
  const handleViewSubWorkflow = useCallback((childWorkflow: WorkflowExecution) => {
    const newLevel: NavigationLevel = {
      id: `child-${childWorkflow.id}`,
      workflow: null,
      workflowName: childWorkflow.workflowName,
      execution: childWorkflow,
      label: childWorkflow.spawnedByNodeId || childWorkflow.workflowName,
      loading: true,
    }
    
    setNavStack(prev => [...prev, newLevel])
  }, [])

  // Handle breadcrumb navigation
  const handleNavigate = useCallback((levelIndex: number) => {
    // Trim the stack to the selected level + 1
    setNavStack(prev => prev.slice(0, levelIndex + 1))
  }, [])

  // Build breadcrumb levels
  const breadcrumbLevels: BreadcrumbLevel[] = navStack.map(level => ({
    id: level.id,
    label: level.label,
    workflowName: level.workflowName,
  }))

  // Loading state
  if (currentLevel.loading) {
    return (
      <div className="flex flex-col bg-background overflow-hidden h-full">
        {navStack.length > 1 && (
          <WorkflowBreadcrumb 
            levels={breadcrumbLevels} 
            onNavigate={handleNavigate} 
          />
        )}
        <div className="flex-1 flex items-center justify-center">
          <div className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            <span className="text-sm">Loading workflow...</span>
          </div>
        </div>
      </div>
    )
  }

  // Error state
  if (currentLevel.error) {
    return (
      <div className="flex flex-col bg-background overflow-hidden h-full">
        {navStack.length > 1 && (
          <WorkflowBreadcrumb 
            levels={breadcrumbLevels} 
            onNavigate={handleNavigate} 
          />
        )}
        <div className="flex-1 flex items-center justify-center">
          <div className="text-center">
            <p className="text-sm text-destructive">{currentLevel.error}</p>
            {navStack.length > 1 ? (
              <button
                onClick={() => handleNavigate(navStack.length - 2)}
                className="mt-2 text-xs text-muted-foreground hover:text-foreground"
              >
                Go back
              </button>
            ) : onClose && (
              <button
                onClick={onClose}
                className="mt-2 text-xs text-muted-foreground hover:text-foreground"
              >
                Close
              </button>
            )}
          </div>
        </div>
      </div>
    )
  }

  // No workflow found
  if (!currentLevel.workflow) {
    return (
      <div className="flex flex-col bg-background overflow-hidden h-full">
        {navStack.length > 1 && (
          <WorkflowBreadcrumb 
            levels={breadcrumbLevels} 
            onNavigate={handleNavigate} 
          />
        )}
        <div className="flex-1 flex items-center justify-center">
          <p className="text-sm text-muted-foreground">No workflow found</p>
        </div>
      </div>
    )
  }

  // Wrap with breadcrumb when navigated into sub-workflows
  const hasBreadcrumb = navStack.length > 1
  
  return (
    <div className="flex flex-col bg-background overflow-hidden h-full">
      {hasBreadcrumb && (
        <WorkflowBreadcrumb 
          levels={breadcrumbLevels} 
          onNavigate={handleNavigate} 
        />
      )}
      <div className="flex-1 min-h-0">
        <WorkflowViewer
          workflow={currentLevel.workflow}
          execution={currentLevel.execution}
          projectId={projectId}
          chatId={chatId}
          workflowName={currentLevel.workflowName}
          onClose={navStack.length === 1 ? onClose : undefined}
          onNodeClick={onNodeClick}
          onViewSubWorkflow={handleViewSubWorkflow}
          compact={compact}
          hideFullscreen={hideFullscreen}
          viewerMode={viewerMode}
          onToggleViewerMode={onToggleViewerMode}
          onExpandedChange={onExpandedChange}
        />
      </div>
    </div>
  )
}
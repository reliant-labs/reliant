/**
 * ActivityLog - Chronological event timeline for workflow execution
 * 
 * Shows all execution events in order:
 * - Step executions (actions completing)
 * - Child workflow spawns and completions
 * 
 * Clicking an event highlights the corresponding node and opens details panel.
 */

import { memo, useMemo, useRef, useEffect, useState, useCallback } from 'react'
import { ChevronDown, ChevronRight, Clock, CheckCircle, XCircle, Loader2, Play, GitBranch, GripHorizontal } from 'lucide-react'
import type { WorkflowExecution, StepExecution } from '../Chat/ExecutionSidebar/types'
import { formatNodeId } from '../Chat/thread-views/threadUtils'
import { normalizeWorkflowRef } from './useWorkflowInputs'

interface ActivityLogProps {
  /** The workflow execution to display events for */
  execution?: WorkflowExecution
  /** Currently highlighted node ID */
  highlightedNodeId?: string
  /** Callback when an event is clicked - passes the full event data */
  onEventClick?: (event: ActivityEvent) => void
  /** Whether the log is expanded */
  isExpanded: boolean
  /** Toggle expansion */
  onToggleExpand: () => void
  /** Node IDs from workflow for step resolution */
  workflowNodeIds: string[]
}

/** Unified event type for timeline */
export interface ActivityEvent {
  id: string
  type: 'step' | 'workflow_spawn' | 'workflow_complete'
  timestamp: number
  nodeId: string
  displayName: string
  status: 'running' | 'completed' | 'failed' | 'pending'
  durationMs?: number
  activityName?: string
  workflowName?: string
  stepExecution?: StepExecution
  childWorkflow?: WorkflowExecution
}

/**
 * Resolve a step ID to a workflow node ID using pattern matching
 */
function resolveStepToNode(stepId: string, nodeIds: string[]): string | null {
  // Direct match
  if (nodeIds.includes(stepId)) return stepId
  
  // Prefix match: "agent_loop-save" → "agent_loop"
  for (const nodeId of nodeIds) {
    if (stepId.startsWith(nodeId + '-') || stepId.startsWith(nodeId + '_')) {
      return nodeId
    }
  }
  
  // Base name: "call_llm" from "call_llm-save"
  const baseName = stepId.split('-')[0]
  if (nodeIds.includes(baseName)) return baseName
  
  return null
}

/** Format timestamp to time string */
function formatTime(timestamp: number): string {
  return new Date(timestamp).toLocaleTimeString([], { 
    hour: '2-digit', 
    minute: '2-digit', 
    second: '2-digit' 
  })
}

/** Format duration */
function formatDuration(ms?: number): string {
  if (ms === undefined || ms === null) return ''
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}m`
}

/** Get display name from step ID */
function getDisplayName(stepId: string): string {
  // Remove common suffixes and clean up
  return stepId
    .replace(/-save$/, '')
    .replace(/_save$/, '')
    .replace(/-/g, ' ')
    .replace(/_/g, ' ')
}

/** Status icon component */
function StatusIcon({ status, className = '' }: { status: string; className?: string }) {
  switch (status) {
    case 'running':
      return <Loader2 className={`w-3.5 h-3.5 text-blue-500 animate-spin ${className}`} />
    case 'completed':
      return <CheckCircle className={`w-3.5 h-3.5 text-emerald-500 ${className}`} />
    case 'failed':
      return <XCircle className={`w-3.5 h-3.5 text-red-500 ${className}`} />
    default:
      return <Clock className={`w-3.5 h-3.5 text-gray-400 ${className}`} />
  }
}

/** Event type icon */
function EventTypeIcon({ type }: { type: ActivityEvent['type'] }) {
  switch (type) {
    case 'workflow_spawn':
      return <Play className="w-3 h-3 text-purple-500" />
    case 'workflow_complete':
      return <GitBranch className="w-3 h-3 text-purple-500" />
    default:
      return null
  }
}

/** Individual activity event row */
const ActivityLogItem = memo(function ActivityLogItem({
  event,
  isHighlighted,
  onClick,
}: {
  event: ActivityEvent
  isHighlighted: boolean
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={`w-full flex items-center gap-2 py-1.5 text-left text-xs transition-colors ${
        isHighlighted 
          ? 'bg-blue-50 border-l-2 border-blue-500' 
          : 'hover:bg-muted/50 border-l-2 border-transparent'
      }`}
    >
      {/* Timestamp */}
      <span className="text-muted-foreground font-mono w-20 flex-shrink-0">
        {formatTime(event.timestamp)}
      </span>
      
      {/* Status icon */}
      <StatusIcon status={event.status} />
      
      {/* Event type icon (for workflow events) */}
      <EventTypeIcon type={event.type} />
      
      {/* Event name */}
      <span className={`flex-1 truncate ${isHighlighted ? 'font-medium text-foreground' : 'text-foreground'}`}>
        {event.displayName}
        {event.workflowName && (
          <span className="text-muted-foreground ml-1">
            → {normalizeWorkflowRef(event.workflowName)}
          </span>
        )}
      </span>
      
      {/* Activity name (abbreviated) */}
      {event.activityName && (
        <span className="text-muted-foreground truncate max-w-24" title={event.activityName}>
          {event.activityName.replace('V2_', '')}
        </span>
      )}
      
      {/* Duration */}
      {event.durationMs !== undefined && event.durationMs > 0 && (
        <span className="text-muted-foreground font-mono w-16 text-right flex-shrink-0">
          {formatDuration(event.durationMs)}
        </span>
      )}
    </button>
  )
})

export const ActivityLog = memo(function ActivityLog({
  execution,
  highlightedNodeId,
  onEventClick,
  isExpanded,
  onToggleExpand,
  workflowNodeIds,
}: ActivityLogProps) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState(192) // Default ~max-h-48 (12rem = 192px)
  const isResizing = useRef(false)
  
  // Handle resize drag
  const handleResizeStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    e.stopPropagation()
    isResizing.current = true
    const startY = e.clientY
    const startHeight = height
    
    const onMouseMove = (e: MouseEvent) => {
      if (!isResizing.current) return
      // Dragging up increases height, dragging down decreases
      const delta = startY - e.clientY
      const newHeight = Math.max(100, Math.min(500, startHeight + delta))
      setHeight(newHeight)
    }
    
    const onMouseUp = () => {
      isResizing.current = false
      document.removeEventListener('mousemove', onMouseMove)
      document.removeEventListener('mouseup', onMouseUp)
    }
    
    document.addEventListener('mousemove', onMouseMove)
    document.addEventListener('mouseup', onMouseUp)
  }, [height])
  
  // Build unified event list from execution
  const events = useMemo(() => {
    if (!execution) return []
    
    const eventList: ActivityEvent[] = []
    
    // Add step executions
    for (const step of execution.steps) {
      const resolvedNodeId = resolveStepToNode(step.stepId, workflowNodeIds) || step.stepId
      
      eventList.push({
        id: `step-${step.id}`,
        type: 'step',
        timestamp: step.createdAt,
        nodeId: resolvedNodeId,
        displayName: getDisplayName(step.stepId),
        status: step.status === 'failed' ? 'failed' : 'completed',
        durationMs: step.durationMs,
        activityName: step.activityName,
        stepExecution: step,
      })
    }
    
    // Add child workflow events
    for (const child of execution.children) {
      const nodeId = child.spawnedByNodeId || child.workflowName
      
      // Spawn event
      eventList.push({
        id: `spawn-${child.id}`,
        type: 'workflow_spawn',
        timestamp: child.createdAt,
        nodeId,
        displayName: child.threadTitle ? formatNodeId(child.threadTitle) : (child.spawnedByNodeId || 'Thread'),
        status: child.status === 'running' ? 'running' : child.status === 'failed' ? 'failed' : 'completed',
        workflowName: child.workflowName,
        childWorkflow: child,
      })
      
      // Completion event (if completed)
      if (child.completedAt && child.status !== 'running') {
        eventList.push({
          id: `complete-${child.id}`,
          type: 'workflow_complete',
          timestamp: child.completedAt,
          nodeId,
          displayName: `${child.threadTitle ? formatNodeId(child.threadTitle) : (child.spawnedByNodeId || 'Thread')} finished`,
          status: child.status === 'failed' ? 'failed' : 'completed',
          durationMs: child.completedAt - child.createdAt,
          workflowName: child.workflowName,
          childWorkflow: child,
        })
      }
    }
    
    // Sort by timestamp
    return eventList.sort((a, b) => a.timestamp - b.timestamp)
  }, [execution, workflowNodeIds])
  
  // Count running events
  const runningCount = events.filter(e => e.status === 'running').length
  
  // Auto-scroll to latest when new events arrive
  useEffect(() => {
    if (isExpanded && scrollRef.current && execution?.status === 'running') {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [events.length, isExpanded, execution?.status])
  
  if (!execution) {
    return null
  }
  
  return (
    <div className="border-t border-border bg-background relative">
      {/* Resize handle - only shown when expanded */}
      {isExpanded && (
        <div
          onMouseDown={handleResizeStart}
          className="absolute top-0 left-0 right-0 h-1 cursor-ns-resize hover:bg-primary/30 transition-colors z-10 flex items-center justify-center group"
          title="Drag to resize"
        >
          <GripHorizontal className="w-4 h-4 text-muted-foreground/0 group-hover:text-muted-foreground/50 transition-colors" />
        </div>
      )}
      
      {/* Header / Toggle - aligned with chat header */}
      <div className="px-4 sm:px-6 lg:px-8">
        <div className="max-w-[1200px] mx-auto">
          <button
            onClick={onToggleExpand}
            className="flex items-center gap-2 w-full py-2 text-sm font-medium text-foreground hover:bg-muted/50 transition-colors"
          >
            {isExpanded ? (
              <ChevronDown className="w-4 h-4 text-muted-foreground" />
            ) : (
              <ChevronRight className="w-4 h-4 text-muted-foreground" />
            )}
            <span>Activity Log</span>
            <span className="text-xs text-muted-foreground">
              ({events.length} events{runningCount > 0 ? `, ${runningCount} running` : ''})
            </span>
          </button>
        </div>
      </div>
      
      {/* Event list - aligned with chat header */}
      {isExpanded && (
        <div 
          ref={scrollRef}
          className="overflow-y-auto border-t border-border"
          style={{ height }}
        >
          <div className="px-4 sm:px-6 lg:px-8">
            <div className="max-w-[1200px] mx-auto">
              {events.length === 0 ? (
                <div className="py-4 text-xs text-muted-foreground text-center italic">
                  No activity yet
                </div>
              ) : (
                <div className="py-1">
                  {events.map(event => (
                    <ActivityLogItem
                      key={event.id}
                      event={event}
                      isHighlighted={event.nodeId === highlightedNodeId}
                      onClick={() => onEventClick?.(event)}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
})

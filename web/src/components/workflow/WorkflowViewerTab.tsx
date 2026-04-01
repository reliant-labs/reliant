/**
 * WorkflowViewerTab - Viewer tab wrapper for WorkflowViewerPanel
 * 
 * Shows a list of all workflow executions for a chat.
 * Auto-selects the running workflow when opened.
 */

import { useMemo, useState, useEffect } from 'react'
import { WorkflowViewerPanel } from './WorkflowViewerPanel'
import { useWorkflowExecutions } from '../../hooks/useWorkflowExecutions'
import { transformWorkflowExecution } from '../Chat/ExecutionSidebar'
import { Loader2, ChevronLeft, CheckCircle, XCircle, Clock } from 'lucide-react'

interface WorkflowViewerTabProps {
  projectId: string
  chatId: string
  workflowName: string
}

/** Format relative time */
function formatRelativeTime(timestamp: number): string {
  const now = Date.now()
  const diff = now - timestamp
  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  
  if (seconds < 60) return 'just now'
  if (minutes < 60) return `${minutes}m ago`
  if (hours < 24) return `${hours}h ago`
  return new Date(timestamp).toLocaleDateString()
}

/** Status icon for workflow */
function WorkflowStatusIcon({ status }: { status: string }) {
  switch (status) {
    case 'running':
      return <Loader2 className="w-4 h-4 text-sky-500 animate-spin" />
    case 'completed':
      return <CheckCircle className="w-4 h-4 text-emerald-500" />
    case 'failed':
      return <XCircle className="w-4 h-4 text-red-500" />
    default:
      return <Clock className="w-4 h-4 text-muted-foreground" />
  }
}

export function WorkflowViewerTab({ projectId, chatId, workflowName }: WorkflowViewerTabProps) {
  // Fetch workflow execution data for this chat
  const { allWorkflows, hasRunningWorkflow, isLoading } = useWorkflowExecutions(chatId)
  
  // Transform all workflows
  const transformedWorkflows = useMemo(() => {
    return allWorkflows.map(transformWorkflowExecution)
  }, [allWorkflows])
  
  // Selected workflow index (null = show list)
  const [selectedIndex, setSelectedIndex] = useState<number | null>(null)
  
  // Auto-select running workflow on mount or when a workflow starts running
  useEffect(() => {
    if (transformedWorkflows.length === 0) return
    
    // Find running workflow
    const runningIndex = transformedWorkflows.findIndex(wf => wf.status === 'running')
    if (runningIndex >= 0) {
      setSelectedIndex(runningIndex)
    } else if (selectedIndex === null) {
      // No running workflow, select the most recent (first in list)
      setSelectedIndex(0)
    }
  }, [transformedWorkflows, hasRunningWorkflow]) // eslint-disable-line react-hooks/exhaustive-deps
  
  const selectedWorkflow = selectedIndex !== null ? transformedWorkflows[selectedIndex] : null

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full">
        <Loader2 className="w-6 h-6 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (transformedWorkflows.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground">
        No workflow executions found
      </div>
    )
  }

  // Show workflow list if nothing selected or user clicked back
  if (selectedIndex === null) {
    return (
      <div className="h-full flex flex-col">
        <div className="px-4 py-3 border-b border-border">
          <h3 className="font-medium text-foreground">Workflow Executions</h3>
          <p className="text-xs text-muted-foreground mt-0.5">
            {transformedWorkflows.length} execution{transformedWorkflows.length !== 1 ? 's' : ''}
          </p>
        </div>
        <div className="flex-1 overflow-y-auto">
          {transformedWorkflows.map((wf, index) => (
            <button
              key={wf.id}
              onClick={() => setSelectedIndex(index)}
              className="w-full px-4 py-3 flex items-center gap-3 hover:bg-muted/50 border-b border-border transition-colors text-left"
            >
              <WorkflowStatusIcon status={wf.status} />
              <div className="flex-1 min-w-0">
                <div className="font-medium text-sm text-foreground truncate">
                  {wf.workflowName.replace('builtin://', '')}
                </div>
                <div className="text-xs text-muted-foreground">
                  {formatRelativeTime(wf.createdAt)} • {wf.status}
                </div>
              </div>
              {wf.status === 'running' && (
                <span className="text-xs bg-sky-100 text-sky-700 px-2 py-0.5 rounded-full">
                  Active
                </span>
              )}
            </button>
          ))}
        </div>
      </div>
    )
  }

  // Show selected workflow with back button
  return (
    <div className="h-full flex flex-col">
      {/* Back button - only show if multiple workflows */}
      {transformedWorkflows.length > 1 && (
        <button
          onClick={() => setSelectedIndex(null)}
          className="flex items-center gap-1 px-3 py-2 text-sm text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors border-b border-border"
        >
          <ChevronLeft className="w-4 h-4" />
          <span>All executions ({transformedWorkflows.length})</span>
        </button>
      )}
      <div className="flex-1 min-h-0">
        <WorkflowViewerPanel
          projectId={projectId}
          workflowName={selectedWorkflow?.workflowName || workflowName}
          execution={selectedWorkflow || undefined}
          compact={false}
          hideFullscreen={true}
        />
      </div>
    </div>
  )
}

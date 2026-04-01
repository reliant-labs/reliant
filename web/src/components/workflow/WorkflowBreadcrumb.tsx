/**
 * WorkflowBreadcrumb - Navigation breadcrumb for sub-workflow drill-down
 * 
 * Shows the navigation path when drilling into child workflow executions.
 * Each segment is clickable to navigate back to that level.
 */

import { memo } from 'react'
import { ChevronRight, ArrowLeft } from 'lucide-react'
import { normalizeWorkflowRef } from './useWorkflowInputs'

export interface BreadcrumbLevel {
  /** Unique ID for this level */
  id: string
  /** Display label (node ID that spawned it, or "Root") */
  label: string
  /** The workflow name at this level */
  workflowName: string
}

interface WorkflowBreadcrumbProps {
  /** Navigation stack - first is root, last is current */
  levels: BreadcrumbLevel[]
  /** Navigate to a specific level by index */
  onNavigate: (levelIndex: number) => void
}

export const WorkflowBreadcrumb = memo(function WorkflowBreadcrumb({
  levels,
  onNavigate,
}: WorkflowBreadcrumbProps) {
  // Don't show if only one level (root)
  if (levels.length <= 1) {
    return null
  }
  
  const canGoBack = levels.length > 1
  
  return (
    <div className="flex items-center gap-1 px-3 py-1.5 bg-muted/50 border-b border-border text-sm">
      {/* Back button */}
      {canGoBack && (
        <button
          onClick={() => onNavigate(levels.length - 2)}
          className="p-1 hover:bg-muted rounded mr-1 transition-colors"
          title="Go back"
        >
          <ArrowLeft className="w-4 h-4 text-muted-foreground" />
        </button>
      )}
      
      {/* Breadcrumb segments */}
      {levels.map((level, index) => {
        const isLast = index === levels.length - 1
        const isClickable = !isLast
        
        return (
          <div key={level.id} className="flex items-center">
            {index > 0 && (
              <ChevronRight className="w-4 h-4 text-muted-foreground mx-1" />
            )}
            
            {isClickable ? (
              <button
                onClick={() => onNavigate(index)}
                className="text-blue-600 hover:text-blue-700 hover:underline transition-colors truncate max-w-32"
                title={`${level.label} (${level.workflowName})`}
              >
                {level.label}
              </button>
            ) : (
              <span 
                className="text-foreground font-medium truncate max-w-48"
                title={`${level.label} (${level.workflowName})`}
              >
                {level.label}
                <span className="text-muted-foreground font-normal ml-1 text-xs">
                  ({normalizeWorkflowRef(level.workflowName)})
                </span>
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
})

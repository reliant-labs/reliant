import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { GitFork } from 'lucide-react'
import type { Step } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

/** Shape of a candidate workflow from the proto RouterArgs.workflows repeated field */
interface RouterWorkflowCandidate {
  ref?: string
  presets?: string[]
  description?: string
}

/** Shape of a candidate node from the proto RouterArgs.nodes repeated field */
interface RouterNodeCandidate {
  id?: string
  description?: string
}

/** Raw router args shape from the proto oneof (args.case === 'router') */
interface RouterArgsShape {
  workflows?: RouterWorkflowCandidate[]
  nodes?: RouterNodeCandidate[]
  systemPrompt?: unknown
  model?: unknown
  thread?: unknown
  fallback?: string
  project?: unknown
}

interface RouterNodeProps {
  id: string
  data: {
    step: Step
    label: string
    executionStatus?: NodeExecutionStatus
    /** Execution data from RouterOutput — which candidate was selected */
    executionData?: {
      selectedWorkflow?: string
      selectedPreset?: string
      selectedNode?: string
      reasoning?: string
    }
  }
  selected?: boolean
}

/** Extract router args from a step's args oneof */
function getRouterArgs(step: Step): RouterArgsShape | undefined {
  if (step.args?.case === 'router') {
    return step.args.value as RouterArgsShape
  }
  // Fallback for when type says router but case doesn't match
  if (step.type === 'router' && step.args?.value) {
    return step.args.value as RouterArgsShape
  }
  return undefined
}

/** Truncate a string with ellipsis */
function truncate(s: string, max: number): string {
  return s.length > max ? s.slice(0, max) + '…' : s
}

/** Strip protocol prefix from a workflow ref for display */
function displayRef(ref: string): string {
  return ref.replace(/^(builtin|project|workflow):\/\//, '')
}

export const RouterNode = memo(({ id: _id, data, selected }: RouterNodeProps) => {
  const { step, label, executionStatus, executionData } = data
  const routerArgs = getRouterArgs(step)

  const isNodeMode = (routerArgs?.nodes?.length ?? 0) > 0
  const workflowCandidates = routerArgs?.workflows ?? []
  const nodeCandidates = routerArgs?.nodes ?? []

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="amber"
      minWidth={220}
      maxWidth={320}
      className="!p-0 !rounded-xl"
    >
      {/* Input handle (left) */}
      <Handle
        type="target"
        position={Position.Left}
        className={buildHandleClassName('amber', isTargetConnected, executionStatus)}
      />

      {/* Header */}
      <div className="px-3 py-2 flex items-center gap-2 border-b border-amber-200 dark:border-amber-700">
        <div className="w-8 h-8 rounded-lg bg-amber-500 flex items-center justify-center flex-shrink-0">
          <GitFork className="w-4 h-4 text-white" />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-[10px] font-bold uppercase tracking-wide text-amber-600">
            ROUTER{isNodeMode ? ' · NODES' : ''}
          </div>
          <div className="font-semibold text-foreground text-sm leading-tight truncate">
            {label || 'Router'}
          </div>
        </div>
      </div>

      {/* Candidate sub-boxes */}
      <div className="p-2 space-y-1.5">
        {isNodeMode ? (
          // Node routing mode
          nodeCandidates.length === 0 ? (
            <div className="text-xs text-muted-foreground italic px-1">No candidates configured</div>
          ) : (
            nodeCandidates.map((candidate, index) => {
              const nodeId = candidate.id || ''
              const isSelected = executionData?.selectedNode === nodeId

              return (
                <div
                  key={`${nodeId}-${index}`}
                  className={`
                    px-2.5 py-1.5 rounded-lg text-xs border
                    ${isSelected
                      ? 'border-emerald-500 bg-emerald-50 dark:bg-emerald-900/20'
                      : executionData?.selectedNode
                        ? 'border-muted bg-muted/50 opacity-50'
                        : 'border-muted bg-muted'
                    }
                  `}
                >
                  <div className="font-medium text-foreground truncate font-mono" title={nodeId}>
                    {truncate(nodeId || `candidate ${index + 1}`, 30)}
                  </div>
                  {candidate.description && (
                    <div className="text-muted-foreground truncate mt-0.5" title={candidate.description}>
                      {truncate(candidate.description, 35)}
                    </div>
                  )}
                </div>
              )
            })
          )
        ) : (
          // Workflow routing mode
          workflowCandidates.length === 0 ? (
            <div className="text-xs text-muted-foreground italic px-1">No candidates configured</div>
          ) : (
            workflowCandidates.map((candidate, index) => {
              const ref = candidate.ref || ''
              const presets = candidate.presets ?? []
              const isSelected =
                executionData?.selectedWorkflow === ref ||
                executionData?.selectedWorkflow === displayRef(ref)

              return (
                <div
                  key={`${ref}-${index}`}
                  className={`
                    px-2.5 py-1.5 rounded-lg text-xs border
                    ${isSelected
                      ? 'border-emerald-500 bg-emerald-50 dark:bg-emerald-900/20'
                      : executionData?.selectedWorkflow
                        ? 'border-muted bg-muted/50 opacity-50'
                        : 'border-muted bg-muted'
                    }
                  `}
                >
                  <div className="font-medium text-foreground truncate" title={ref}>
                    {truncate(displayRef(ref) || `candidate ${index + 1}`, 30)}
                  </div>
                  {presets.length > 0 && (
                    <div className="text-muted-foreground truncate mt-0.5" title={presets.join(', ')}>
                      {truncate(presets.join(', '), 35)}
                    </div>
                  )}
                </div>
              )
            })
          )
        )}
      </div>

      {/* Output handle (right) — single handle, not per-candidate */}
      <Handle
        type="source"
        position={Position.Right}
        className={buildHandleClassName('amber', isSourceConnected, executionStatus)}
      />
    </NodeStatusWrapper>
  )
})

RouterNode.displayName = 'RouterNode'

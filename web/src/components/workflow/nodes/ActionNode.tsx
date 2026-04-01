import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import type { Step } from '../../../types/workflow'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'
import { getNodeIcon, getNodeColor, getNodeDisplayName, getNodeTheme } from '../../../lib/node-metadata'

interface ActionNodeProps {
  data: {
    step: Step
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

export const ActionNode = memo(({ data, selected }: ActionNodeProps) => {
  const { step, label, executionStatus, layoutDirection = 'horizontal' } = data
  
  // type is snake_case activity name (e.g., "call_llm", "save_message")
  const activityType = step.type || 'action'

  const displayName = getNodeDisplayName(activityType)
  const Icon = getNodeIcon(activityType)
  const colors = getNodeColor(activityType)
  const theme = getNodeTheme(activityType)

  const targetConnections = useNodeConnections({ handleType: 'target' })
  const sourceConnections = useNodeConnections({ handleType: 'source' })

  const isTargetConnected = targetConnections.length > 0
  const isSourceConnected = sourceConnections.length > 0

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme={theme}
    >
      {/* Connection handles - dynamic based on layout direction */}
      <Handle
        type="target"
        position={layoutDirection === 'vertical' ? Position.Top : Position.Left}
        className={buildHandleClassName(theme, isTargetConnected, executionStatus)}
      />
      <Handle
        type="source"
        position={layoutDirection === 'vertical' ? Position.Bottom : Position.Right}
        className={buildHandleClassName(theme, isSourceConnected, executionStatus)}
      />

      <div className="flex flex-col gap-1.5">
        {/* Header with icon and activity type name */}
        <div className="flex items-center gap-2">
          <div className={`w-8 h-8 rounded-lg ${colors.bg} flex items-center justify-center flex-shrink-0`}>
            <Icon className="w-4 h-4 text-white" />
          </div>
          <div className="flex-1 min-w-0">
            <div className={`text-[10px] font-bold uppercase tracking-wide ${colors.text}`}>{displayName}</div>
            <div className="font-medium text-muted-foreground text-xs leading-tight truncate">{label}</div>
          </div>
        </div>
      </div>
    </NodeStatusWrapper>
  )
})

ActionNode.displayName = 'ActionNode'

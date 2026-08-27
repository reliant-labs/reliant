import { memo } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { Zap, MessageCircle, FileText, Clock, Rocket } from 'lucide-react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { NodeStatusWrapper, buildHandleClassName, type NodeTheme } from './NodeStatusWrapper'
import { cn } from '../../../lib/utils'

interface EventNodeProps {
  data: {
    eventType: string
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

const eventIcons: Record<string, React.ElementType> = {
  'started': Rocket,
  'message_created': MessageCircle,
  'pre_tool_use': FileText,
  'post_tool_use': FileText,
  'chat_complete': Clock,
  'manual': Zap,
}

const eventLabels: Record<string, string> = {
  'started': 'Workflow Start',
  'message_created': 'User Message',
  'pre_tool_use': 'Before Tool Use',
  'post_tool_use': 'After Tool Use',
  'chat_complete': 'Chat Complete',
  'manual': 'Manual Trigger',
}

export const EventNode = memo(({ data, selected }: EventNodeProps) => {
  const { eventType, executionStatus, layoutDirection = 'horizontal' } = data
  const Icon = eventIcons[eventType] || Zap
  const eventLabel = eventLabels[eventType] || eventType
  
  const sourceConnections = useNodeConnections({ handleType: 'source' })
  const isSourceConnected = sourceConnections.length > 0

  // Use primary theme color for 'started' event, orange for others
  const isStartEvent = eventType === 'started'
  const theme: NodeTheme = isStartEvent ? 'primary' : 'orange'

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme={theme}
      minWidth={180}
      maxWidth={280}
    >
      {/* Only source handle - dynamic based on layout direction */}
      <Handle
        type="source"
        position={layoutDirection === 'vertical' ? Position.Bottom : Position.Right}
        className={buildHandleClassName(theme, isSourceConnected, executionStatus)}
        isConnectable={true}
      />

      <div className="flex items-center gap-3">
        {/* Icon - Standardized for all events */}
        <div className={cn(
          "w-8 h-8 rounded-lg flex items-center justify-center flex-shrink-0",
          isStartEvent ? "bg-green-600" : "bg-orange-500"
        )}>
          <Icon className="w-4 h-4 text-white" />
        </div>
        
        {/* Content */}
        <div className="flex-1 min-w-0">
          <div className={cn(
            "text-2xs font-bold uppercase tracking-wide",
            isStartEvent ? "text-green-600" : "text-orange-600"
          )}>
            {isStartEvent ? 'START' : 'EVENT'}
          </div>
          <div className="font-semibold text-foreground text-sm leading-tight break-words">
            {eventLabel}
          </div>
        </div>
      </div>
    </NodeStatusWrapper>
  )
})

EventNode.displayName = 'EventNode'

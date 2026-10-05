/**
 * TriggerRailNode — the builder's entry node, drawn as "how this workflow
 * gets started" (research/WORKFLOW_UI.md §3.2).
 *
 *   Starts when
 *   ○ Someone starts a chat        always present (chat.start)
 *   ◷ Nightly triage  Weekdays…    one line per trigger naming this workflow
 *   + Add trigger
 *
 * A PROJECTION, not a node in the definition: the lines come from ListTriggers
 * (via TriggerRailContext) at render time and nothing about them is in the
 * node's data, so the saved graph and YAML cannot change because of the rail.
 * Read-only and builtin workflows still list and add triggers, because a
 * trigger is a separate row and does not edit the definition.
 *
 * The in-graph event types (message_created, pre_tool_use, …) stay EventNode.
 */

import { Tooltip } from "../../ui/Tooltip";
import { memo, type MouseEvent } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { Clock, MessageCircle, Plus, Rocket } from 'lucide-react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { triggerRailLines } from '../../../lib/triggerRail'
import { useTriggers } from '../../../hooks/trigger-queries'
import { cn } from '../../../lib/utils'
import { useTriggerRailContext } from '../TriggerRailContext'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'

interface TriggerRailNodeProps {
  data: {
    eventType: string
    label: string
    executionStatus?: NodeExecutionStatus
    layoutDirection?: 'horizontal' | 'vertical'
  }
  selected?: boolean
}

// Controls inside a React Flow node must not start a drag or pan, and their
// clicks must not also select the node (which opens the payload panel).
const nodeControlClass = 'nodrag nopan'

function stop(handler: () => void) {
  return (event: MouseEvent) => {
    event.stopPropagation()
    handler()
  }
}

const lineClass =
  'flex w-full min-w-0 items-center gap-2 rounded-md px-1.5 py-1 text-left text-xs'

export const TriggerRailNode = memo(({ data, selected }: TriggerRailNodeProps) => {
  const { executionStatus, layoutDirection = 'horizontal' } = data
  const rail = useTriggerRailContext()

  const sourceConnections = useNodeConnections({ handleType: 'source' })
  const isSourceConnected = sourceConnections.length > 0

  const triggersQuery = useTriggers(rail?.projectId, { enabled: !!rail?.projectId })
  const lines =
    rail && triggersQuery.data
      ? triggerRailLines(triggersQuery.data, rail.workflowRef, rail.projectId)
      : []

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="primary"
      minWidth={220}
      maxWidth={300}
    >
      <Handle
        type="source"
        position={layoutDirection === 'vertical' ? Position.Bottom : Position.Right}
        className={buildHandleClassName('primary', isSourceConnected, executionStatus)}
        isConnectable={true}
      />

      <div className="flex items-center gap-2 pb-1.5">
        <div className="flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-md bg-primary">
          <Rocket className="h-3.5 w-3.5 text-primary-foreground" aria-hidden />
        </div>
        <div className="text-2xs font-bold uppercase tracking-wide text-primary">Starts when</div>
      </div>

      <ul aria-label="How this workflow starts" className="space-y-0.5">
        <li className={cn(lineClass, 'text-foreground')}>
          <MessageCircle className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
          <span className="truncate font-medium">Someone starts a chat</span>
        </li>

        {lines.map(({ trigger, scheduleText, health, paused, failing }) => (
          <li key={trigger.id}>
            <Tooltip content={health.detail ?? health.label} placement="bottom" delay={300} wrapperClassName="inline-flex">
<button
              type="button"
              onClick={stop(() => rail?.onEditTrigger(trigger))}
              data-paused={paused ? 'true' : undefined}
              aria-label={`${trigger.name}, ${scheduleText}${paused || failing ? `, ${health.label}` : ''}. Edit trigger`}
              className={cn(
                nodeControlClass,
                lineClass,
                'text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                paused && 'opacity-50',
              )}
            >
              <Clock className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate font-medium">{trigger.name}</span>
              <span className="max-w-[45%] flex-shrink-0 truncate text-muted-foreground">{scheduleText}</span>
              {failing && (
                <span
                  data-testid="trigger-rail-failing-dot"
                  className="h-1.5 w-1.5 flex-shrink-0 rounded-full bg-destructive"
                  aria-hidden
                />
              )}
            </button>
</Tooltip>
          </li>
        ))}

        {triggersQuery.isError && (
          <li className={cn(lineClass, 'text-muted-foreground')}>
            <span className="flex-1 truncate">Couldn't load triggers</span>
            <button
              type="button"
              onClick={stop(() => void triggersQuery.refetch())}
              className={cn(nodeControlClass, 'font-medium text-primary hover:underline')}
            >
              Retry
            </button>
          </li>
        )}
      </ul>

      {rail && (
        <div className="mt-1 border-t border-border/60 pt-1">
          <button
            type="button"
            onClick={stop(() => rail.onAddTrigger())}
            disabled={!rail.canAddTrigger}
            aria-label="Add trigger"
            className={cn(
              nodeControlClass,
              lineClass,
              'font-medium text-primary transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:text-muted-foreground disabled:hover:bg-transparent',
            )}
          >
            <Plus className="h-3.5 w-3.5 flex-shrink-0" aria-hidden />
            Add trigger
          </button>
          {!rail.canAddTrigger && (
            <p className="px-1.5 text-2xs text-muted-foreground">Save the workflow to add a trigger.</p>
          )}
        </div>
      )}
    </NodeStatusWrapper>
  )
})

TriggerRailNode.displayName = 'TriggerRailNode'

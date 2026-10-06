/**
 * TriggerRailNode — the builder's entry node, drawn as "how this workflow
 * gets started".
 *
 *   Starts when
 *   ○ Someone starts a chat          always present (chat.start)
 *   ⚡ new-issue  GitHub: issues…     a DECLARED trigger (the definition's
 *      ● Active · 1 activation        `triggers:`), with the caller's
 *      or  Activate                   activations of it
 *   ◷ Nightly triage  Weekdays…      an AD HOC automation running this
 *                                    workflow with its own inline source
 *   + Add trigger
 *
 * Declared triggers come from the definition (research/INTEGRATIONS_V1_BRIEF.md
 * §3a); activations and ad hoc automations from ListTriggers. Validation
 * findings for a declared trigger (`triggers[i](name).field`) render on its
 * line, so a bad cron is visible where it is.
 *
 * The in-graph event types (message_created, pre_tool_use, …) stay EventNode.
 */

import { Tooltip } from "../../ui/Tooltip";
import { memo, type MouseEvent } from 'react'
import { Handle, Position, useNodeConnections } from '@xyflow/react'
import { AlertOctagon, AlertTriangle, CalendarClock, Clock, MessageCircle, Plug, Plus, Rocket, Webhook, Workflow, Zap } from 'lucide-react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { declaredRailLines, triggerRailLines, type DeclaredRailLine } from '../../../lib/triggerRail'
import { describeDeclaredSource, findingFieldLabel, integrationOf, sourceCase } from '../../../lib/declaredTriggers'
import { describeSchedule } from '../../../lib/cronText'
import { useTriggers } from '../../../hooks/trigger-queries'
import { cn } from '../../../lib/utils'
import { useTriggerRailContext } from '../TriggerRailContext'
import { NodeStatusWrapper, buildHandleClassName } from './NodeStatusWrapper'
import { IntegrationLogo } from '../../icons/IntegrationLogo'
import { NODE_DIMENSIONS } from '../../../lib/workflow-node-dimensions'

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

const STATE_TEXT: Record<DeclaredRailLine['state'], string> = {
  inactive: 'Not active',
  active: 'Active',
  paused: 'Paused',
  failing: 'Failing',
  broken: 'Broken',
}

function DeclaredIcon({ line }: { line: DeclaredRailLine }) {
  const kind = sourceCase(line.declared)
  if (kind === 'integration') return <IntegrationLogo icon={integrationOf(line.declared)?.integration} size="sm" />
  const Icon = kind === 'schedule' ? CalendarClock : kind === 'webhook' ? Webhook : kind === 'workflowEvent' ? Workflow : Zap
  return <Icon className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
}

export const TriggerRailNode = memo(({ data, selected }: TriggerRailNodeProps) => {
  const { executionStatus, layoutDirection = 'horizontal' } = data
  const rail = useTriggerRailContext()

  const sourceConnections = useNodeConnections({ handleType: 'source' })
  const isSourceConnected = sourceConnections.length > 0

  // Activations can be in any project, so the declared lines read the
  // every-project list; ad hoc lines stay scoped to this project.
  const triggersQuery = useTriggers(undefined, { enabled: !!rail?.workflowRef })
  const all = triggersQuery.data ?? []
  const lines = rail && triggersQuery.data ? triggerRailLines(all, rail.workflowRef, rail.projectId) : []
  const declared = rail
    ? declaredRailLines(rail.declared, all, rail.workflowRef)
    : { lines: [], orphans: [] }

  return (
    <NodeStatusWrapper
      status={executionStatus}
      selected={selected}
      theme="primary"
      minWidth={NODE_DIMENSIONS.triggerRail.width}
      maxWidth={NODE_DIMENSIONS.triggerRail.width}
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

        {rail && declared.lines.map((line) => {
          const findings = rail.findingsFor(line.index, line.declared.name ?? '')
          const sourceText = describeDeclaredSource(line.declared, describeSchedule)
          const unsaved = rail.unsavedDeclared.has(line.declared.name ?? '')
          const count = line.activations.length
          const stateWords = line.state === 'inactive' ? '' : `${STATE_TEXT[line.state]}${count > 1 ? ` · ${count} activations` : ''}`
          return (
            <li key={`declared-${line.index}`} data-testid={`rail-declared-${line.declared.name}`} data-state={line.state} className="space-y-0.5">
              <div className="flex items-center gap-1">
                <button
                  type="button"
                  onClick={stop(() => rail.onEditDeclared(line.index))}
                  aria-label={`${line.declared.name}, ${sourceText}${stateWords ? `, ${stateWords}` : ', not active'}${findings.length ? `, ${findings.length} problem${findings.length > 1 ? 's' : ''}` : ''}. Edit trigger`}
                  className={cn(
                    nodeControlClass,
                    lineClass,
                    'flex-1 text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                  )}
                >
                  <DeclaredIcon line={line} />
                  <span className="min-w-0 flex-1 truncate font-medium">{line.declared.name}</span>
                  {/* Information, not decoration: foreground at normal weight (the
                      name is medium), since muted-foreground on the node card
                      measures ~4.0:1 in light schemes. */}
                  <span className="max-w-[50%] flex-shrink-0 truncate font-normal text-foreground">{sourceText}</span>
                </button>
                {line.state === 'inactive' ? (
                  <button
                    type="button"
                    onClick={stop(() => rail.onActivateDeclared(line.index))}
                    disabled={!rail.canAddTrigger || unsaved}
                    className={cn(
                      nodeControlClass,
                      'flex-shrink-0 rounded-md border border-border px-1.5 py-0.5 text-2xs font-medium text-primary transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:text-muted-foreground disabled:hover:bg-transparent',
                    )}
                    aria-label={`Activate ${line.declared.name}`}
                  >
                    Activate
                  </button>
                ) : (
                  <Tooltip content={line.health?.detail ?? line.health?.label ?? STATE_TEXT[line.state]} placement="bottom" delay={300} wrapperClassName="inline-flex">
                    <button
                      type="button"
                      onClick={stop(() => (count === 1 ? rail.onEditTrigger(line.activations[0]!) : rail.onActivateDeclared(line.index)))}
                      className={cn(
                        nodeControlClass,
                        'flex flex-shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 text-2xs font-medium transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                        line.state === 'broken' || line.state === 'failing' ? 'text-danger-ink' : line.state === 'active' ? 'text-success-ink' : 'text-muted-foreground',
                      )}
                      aria-label={`${line.declared.name} is ${stateWords}. ${count === 1 ? 'Edit activation' : 'Activate again'}`}
                    >
                      {line.state === 'broken' ? (
                        <AlertOctagon className="h-3 w-3" aria-hidden />
                      ) : (
                        <span
                          aria-hidden
                          className={cn(
                            'h-1.5 w-1.5 rounded-full',
                            line.state === 'active' ? 'bg-success' : line.state === 'failing' ? 'bg-destructive' : 'bg-muted-foreground',
                          )}
                        />
                      )}
                      {STATE_TEXT[line.state]}
                      {count > 1 && <span className="text-muted-foreground">×{count}</span>}
                    </button>
                  </Tooltip>
                )}
              </div>
              {unsaved && line.state === 'inactive' && (
                <p className="px-1.5 pl-7 text-2xs text-muted-foreground">Save the workflow to activate this trigger.</p>
              )}
              {findings.map((finding, i) => (
                <p key={i} role="note" className="flex items-start gap-1 px-1.5 pl-7 text-2xs text-warning-ink">
                  <AlertTriangle className="mt-px h-3 w-3 flex-shrink-0" aria-hidden />
                  <span>
                    {finding.field && <span className="font-medium">{findingFieldLabel(finding.field)}: </span>}
                    {finding.message}
                  </span>
                </p>
              ))}
            </li>
          )
        })}

        {declared.orphans.map((orphan) => (
          <li key={orphan.id}>
            <Tooltip content={orphan.health.lastFailureDetail || 'Its declared trigger is gone'} placement="bottom" delay={300} wrapperClassName="inline-flex w-full">
              <button
                type="button"
                onClick={stop(() => rail?.onEditTrigger(orphan))}
                aria-label={`${orphan.name}, broken: ${orphan.health.lastFailureDetail || `activates "${orphan.workflowTrigger}", which this workflow no longer declares`}. Fix`}
                className={cn(nodeControlClass, lineClass, 'text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring')}
              >
                <AlertOctagon className="h-3.5 w-3.5 flex-shrink-0 text-danger-ink" aria-hidden />
                <span className="min-w-0 flex-1 truncate font-medium">{orphan.name}</span>
                <span className="flex-shrink-0 truncate text-danger-ink">Broken: “{orphan.workflowTrigger}” removed</span>
              </button>
            </Tooltip>
          </li>
        ))}

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
              {trigger.source.kind === 'passthrough' ? (
                <Plug className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
              ) : (
                <Clock className="h-3.5 w-3.5 flex-shrink-0 text-muted-foreground" aria-hidden />
              )}
              <span className="min-w-0 flex-1 truncate font-medium">{trigger.name}</span>
              <span className="max-w-[45%] flex-shrink-0 truncate font-normal text-foreground">{scheduleText}</span>
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
            disabled={!rail.canAddTrigger && !rail.canEditDefinition}
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
          {!rail.canAddTrigger && !rail.canEditDefinition && (
            <p className="px-1.5 text-2xs text-muted-foreground">Save the workflow to add a trigger.</p>
          )}
        </div>
      )}
    </NodeStatusWrapper>
  )
})

TriggerRailNode.displayName = 'TriggerRailNode'

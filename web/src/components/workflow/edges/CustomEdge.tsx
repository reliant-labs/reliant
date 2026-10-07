import { BaseEdge, EdgeLabelRenderer, getBezierPath } from '@xyflow/react'
import type { EdgeProps } from '@xyflow/react'
import { Plus } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import { cn } from '../../../lib/utils'
import { useCanvasInsertApi } from '../canvas/CanvasInsertContext'

export interface CustomEdgeData {
  label?: string
  executionStatus?: NodeExecutionStatus
  layoutDirection?: 'horizontal' | 'vertical'
}

/**
 * Hover state for an edge and the controls at its midpoint. They are separate
 * DOM trees (the path is SVG, the controls an HTML overlay), so leaving one
 * for the other waits a beat before counting as leaving the edge.
 */
function useEdgeHover() {
  const [hovered, setHovered] = useState(false)
  const leaveTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  useEffect(() => () => clearTimeout(leaveTimer.current), [])
  return {
    hovered,
    onMouseEnter: () => {
      clearTimeout(leaveTimer.current)
      setHovered(true)
    },
    onMouseLeave: () => {
      clearTimeout(leaveTimer.current)
      leaveTimer.current = setTimeout(() => setHovered(false), 120)
    },
  }
}

export function CustomEdge({
  id,
  source,
  target,
  sourceX,
  sourceY,
  targetX,
  targetY,
  sourcePosition,
  targetPosition,
  style,
  data,
  selected,
}: EdgeProps) {
  const edgeData = data as CustomEdgeData | undefined
  // Null on a read-only canvas (the viewer), which therefore shows no "+".
  const insert = useCanvasInsertApi()
  const hover = useEdgeHover()

  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })

  const label = edgeData?.label || ''
  const executionStatus = edgeData?.executionStatus
  const isTaken = executionStatus === 'completed' || executionStatus === 'running'
  const isFailed = executionStatus === 'failed'

  const strokeColor = isFailed
    ? 'hsl(var(--destructive))'
    : isTaken
      ? 'hsl(var(--success))'
      : 'hsl(var(--muted-foreground) / 0.42)'

  const strokeWidth = selected ? 3 : isTaken || isFailed ? 2.5 : 2

  const edgeStyle = {
    ...(style || {}),
    stroke: selected ? 'hsl(var(--primary))' : strokeColor,
    strokeWidth,
  }

  const interactionPath = getBezierPath({
    sourceX,
    sourceY,
    sourcePosition,
    targetX,
    targetY,
    targetPosition,
  })[0]

  const markerId = `arrow-${id}`
  const markerColor = selected ? 'hsl(var(--primary))' : strokeColor
  const insertLabel = `Insert a step between ${source} and ${target}`

  return (
    <>
      <svg style={{ position: 'absolute', top: 0, left: 0, zIndex: 25 }}>
        <defs>
          <marker
            id={markerId}
            markerWidth="18"
            markerHeight="18"
            viewBox="-10 -10 20 20"
            orient="auto"
            refX="0"
            refY="0"
          >
            <polyline
              stroke={markerColor}
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth="1.5"
              fill={markerColor}
              points="-6,-5 0,0 -6,5 -6,-5"
            />
          </marker>
        </defs>
      </svg>
      <path
        d={interactionPath}
        fill="none"
        stroke="transparent"
        strokeWidth={20}
        className="react-flow__edge-interaction"
        onMouseEnter={hover.onMouseEnter}
        onMouseLeave={hover.onMouseLeave}
      />
      <BaseEdge path={edgePath} markerEnd={`url(#${markerId})`} style={edgeStyle} />
      {(label || insert) && (
        <EdgeLabelRenderer>
          <div
            data-edge-id={id}
            style={{
              position: 'absolute',
              transform: `translate(-50%, -50%) translate(${labelX}px,${labelY}px)`,
              fontSize: 10,
              pointerEvents: 'all',
              zIndex: 30,
            }}
            className="nodrag nopan flex items-center gap-1"
            onMouseEnter={hover.onMouseEnter}
            onMouseLeave={hover.onMouseLeave}
          >
            {label && (
              <div
                className={`rounded-full border px-2 py-1 text-xs font-medium shadow-sm backdrop-blur-sm transition-colors ${
                  selected
                    ? 'border-primary bg-primary text-primary-foreground shadow-primary/20'
                    : isTaken
                      ? 'border-success/50 bg-success/10 text-success-ink'
                      : isFailed
                        ? 'border-destructive/50 bg-destructive/10 text-destructive-ink'
                        : 'border-border bg-card/95 text-muted-foreground hover:text-foreground'
                }`}
                title={label}
                onClick={(event) => {
                  event.stopPropagation()
                  const edgeElement = document.querySelector(`[data-id="${id}"]`)
                  if (edgeElement) {
                    edgeElement.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
                  }
                }}
              >
                {label}
              </div>
            )}
            {insert && (
              <button
                type="button"
                aria-label={insertLabel}
                title={insertLabel}
                data-testid={`edge-insert-${id}`}
                onClick={(event) => {
                  event.stopPropagation()
                  insert.openPaletteAt({ kind: 'splice', edgeId: id })
                }}
                onMouseDown={(event) => event.stopPropagation()}
                onPointerDown={(event) => event.stopPropagation()}
                // Shown while the edge is hovered or selected, and whenever it
                // has keyboard focus; otherwise the canvas stays quiet.
                className={cn(
                  'flex h-[22px] w-[22px] items-center justify-center rounded-full border border-border bg-card text-muted-foreground shadow-sm transition-opacity',
                  'hover:border-primary hover:bg-primary hover:text-primary-foreground',
                  'focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                  hover.hovered || selected ? 'opacity-100' : 'opacity-0',
                )}
              >
                <Plus className="h-3.5 w-3.5" aria-hidden />
              </button>
            )}
          </div>
        </EdgeLabelRenderer>
      )}
    </>
  )
}

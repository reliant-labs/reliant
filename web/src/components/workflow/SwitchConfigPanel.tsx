import { useState, useCallback } from 'react'
import { useMemo } from 'react'
import type { Edge, Node } from '@xyflow/react'
import { ConfigurationPanel } from './ConfigurationPanel'
import { Trash2, GripVertical, Plus, ArrowRight } from 'lucide-react'
import { cn } from '../../lib/utils'
import type { SwitchCase, SwitchNodeData } from './nodes/SwitchNode'
import { CELExpressionInput } from './CELInput'
import { useWorkflowMutations } from './WorkflowMutationContext'

interface SwitchConfigPanelProps {
  node: Node
  /** All workflow nodes — used to resolve case destinations by label. */
  nodes?: Node[]
  /** All workflow edges — used to look up which node each case routes to. */
  edges?: Edge[]
  onClose: () => void
  bottomOffset?: number
  topOffset?: number
  isReadOnly?: boolean
}

export function SwitchConfigPanel({
  node,
  nodes,
  edges,
  onClose,
  bottomOffset,
  topOffset,
  isReadOnly = false,
}: SwitchConfigPanelProps) {
  // Mutation primitives from <WorkflowMutationProvider>. `updateSwitchNode`
  // also reconciles dangling edges when cases are reordered/removed.
  const mutations = useWorkflowMutations()
  const onUpdate = useCallback(
    (nodeId: string, data: Partial<SwitchNodeData>) => mutations.updateSwitchNode(nodeId, data),
    [mutations],
  )
  const onDelete = useCallback(
    () => mutations.removeNode(node.id),
    [mutations, node.id],
  )
  const data = node.data as unknown as SwitchNodeData
  const cases = useMemo(() => data.cases || [], [data.cases])

  // Map case id → destination node label/id, looked up via the case's
  // sourceHandle. Falls back to undefined when the case isn't wired up yet.
  const destinationByCase = useMemo(() => {
    const map: Record<string, string> = {}
    if (!edges || !nodes) return map
    const nodeLabel = (id: string): string => {
      const target = nodes.find((n) => n.id === id)
      if (!target) return id
      const label = (target.data as { label?: unknown } | undefined)?.label
      return typeof label === 'string' && label ? label : id
    }
    for (const edge of edges) {
      if (edge.source !== node.id || !edge.sourceHandle) continue
      map[edge.sourceHandle] = nodeLabel(edge.target)
    }
    return map
  }, [edges, nodes, node.id])
  
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null)

  const updateCases = useCallback((newCases: SwitchCase[]) => {
    onUpdate(node.id, { cases: newCases })
  }, [node.id, onUpdate])

  const updateCase = useCallback((index: number, updates: Partial<SwitchCase>) => {
    const newCases = cases.map((c, i) => 
      i === index ? { ...c, ...updates } : c
    )
    updateCases(newCases)
  }, [cases, updateCases])

  // A case is the synthetic default iff workflow-flow.ts gave it id='default'
  // (only appended when defaultTargets.length > 0). Indexing off "last case" is
  // wrong for switches with conditional cases only.
  const isDefaultCase = useCallback(
    (c: SwitchCase | undefined) => c?.id === 'default',
    [],
  )
  const defaultIndex = useMemo(
    () => cases.findIndex(isDefaultCase),
    [cases, isDefaultCase],
  )

  const addCase = useCallback(() => {
    // Pick the next free `case-N` id so the React Flow source handle survives
    // a save→reload roundtrip (workflow-flow.ts emits `case-${idx}` on reload).
    const usedIndices = cases
      .map((c) => {
        const match = /^case-(\d+)$/.exec(c.id)
        return match ? Number(match[1]) : -1
      })
      .filter((n) => n >= 0)
    const nextIdx = usedIndices.length > 0 ? Math.max(...usedIndices) + 1 : 0
    const newCase: SwitchCase = {
      id: `case-${nextIdx}`,
      condition: '',
      label: '',
    }
    const newCases = [...cases]
    // Insert before the default case if one exists; otherwise append.
    const insertAt = defaultIndex >= 0 ? defaultIndex : newCases.length
    newCases.splice(insertAt, 0, newCase)
    updateCases(newCases)
  }, [cases, defaultIndex, updateCases])

  const deleteCase = useCallback((index: number) => {
    // Can't delete if only one case, and can't delete the default case.
    if (cases.length <= 1) return
    if (isDefaultCase(cases[index])) return

    const newCases = cases.filter((_, i) => i !== index)
    updateCases(newCases)
  }, [cases, isDefaultCase, updateCases])

  const handleDragStart = (index: number) => {
    // Can't drag the default case
    if (isDefaultCase(cases[index])) return
    setDraggedIndex(index)
  }

  const handleDragOver = (e: React.DragEvent, targetIndex: number) => {
    e.preventDefault()
    if (draggedIndex === null) return
    // Can't drop on the default case position
    if (isDefaultCase(cases[targetIndex])) return
  }

  const handleDrop = (e: React.DragEvent, targetIndex: number) => {
    e.preventDefault()
    if (draggedIndex === null || draggedIndex === targetIndex) {
      setDraggedIndex(null)
      return
    }
    // Can't drop on the default case position
    if (isDefaultCase(cases[targetIndex])) {
      setDraggedIndex(null)
      return
    }

    const newCases = [...cases]
    const [removed] = newCases.splice(draggedIndex, 1)
    newCases.splice(targetIndex, 0, removed)

    updateCases(newCases)
    setDraggedIndex(null)
  }

  const handleDragEnd = () => {
    setDraggedIndex(null)
  }

  return (
    <ConfigurationPanel
      title={isReadOnly ? "Switch (View Only)" : "Switch"}
      subtitle="Conditional routing"
      onClose={onClose}
      onDelete={isReadOnly ? undefined : onDelete}
      deleteLabel="Delete Switch"
      bottomOffset={bottomOffset}
      topOffset={topOffset}
    >
      <div className="cpv2-section">
      <p className="text-xs text-muted-foreground mb-3">
        Cases are evaluated in order. First match wins.
      </p>

      {/* Label */}
      <div className="mb-4">
        <label className="text-xs font-medium text-muted-foreground mb-1 block">
          Label
        </label>
        <input
          type="text"
          value={data.label || ''}
          onChange={(e) => onUpdate(node.id, { label: e.target.value })}
          placeholder="Switch"
          className="w-full px-2 py-1.5 text-sm border border-input rounded bg-background text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-60 disabled:cursor-not-allowed"
          disabled={isReadOnly}
        />
      </div>

      {/* Cases */}
      <div className="space-y-2">
        <label className="text-xs font-medium text-muted-foreground">Cases</label>
        
        {cases.map((caseItem, index) => {
          const isDefault = isDefaultCase(caseItem)
          // Need >1 non-default cases to reorder
          const conditionalCount = cases.length - (defaultIndex >= 0 ? 1 : 0)
          const canDrag = !isDefault && conditionalCount > 1
          const canDelete = !isDefault && cases.length > 1
          
          const destination = destinationByCase[caseItem.id]
          return (
            <div
              key={caseItem.id}
              draggable={canDrag && !isReadOnly}
              onDragStart={() => !isReadOnly && handleDragStart(index)}
              onDragOver={(e) => !isReadOnly && handleDragOver(e, index)}
              onDrop={(e) => !isReadOnly && handleDrop(e, index)}
              onDragEnd={handleDragEnd}
              className={cn(
                'flex items-start gap-2 p-2 rounded-lg border transition-all',
                draggedIndex === index ? 'opacity-50 border-dashed border-primary' : 'border-border',
                isDefault ? 'bg-muted/50' : 'bg-muted/30',
              )}
            >
              {/* Drag handle */}
              <div className={`pt-1.5 ${canDrag && !isReadOnly ? 'cursor-grab active:cursor-grabbing' : 'opacity-30'}`}>
                <GripVertical className="w-3.5 h-3.5 text-muted-foreground" />
              </div>

              {/* Case fields */}
              <div className="flex-1 space-y-1.5 min-w-0">
                {/* Case header + destination */}
                <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <span>{isDefault ? 'default' : `case ${index + 1}`}</span>
                  {destination ? (
                    <span className="flex items-center gap-1 min-w-0 text-foreground/80 font-normal">
                      <ArrowRight className="w-3 h-3 shrink-0 opacity-60" />
                      <span className="truncate font-mono">{destination}</span>
                    </span>
                  ) : (
                    <span className="flex items-center gap-1 text-muted-foreground/60 italic font-normal">
                      <ArrowRight className="w-3 h-3 shrink-0 opacity-40" />
                      <span>unconnected</span>
                    </span>
                  )}
                </div>
                
                {/* Condition input - only for non-default cases */}
                {!isDefault && (
                  <CELExpressionInput
                    value={caseItem.condition}
                    onChange={(val) => updateCase(index, { condition: val })}
                    placeholder="nodes.agent.succeeded == true"
                    celContext="edge_condition"
                    className="text-xs"
                    showCELIndicator={false}
                    disabled={isReadOnly}
                    hideCELHint
                  />
                )}
                
                {/* Label input */}
                <input
                  type="text"
                  value={caseItem.label || ''}
                  onChange={(e) => updateCase(index, { label: e.target.value })}
                  placeholder="label (optional)"
                  className="w-full px-2 py-1 text-xs border border-input rounded bg-background text-foreground focus:outline-none focus:ring-1 focus:ring-ring disabled:opacity-60 disabled:cursor-not-allowed"
                  disabled={isReadOnly}
                />
              </div>

              {/* Delete button */}
              {canDelete && !isReadOnly && (
                <button
                  onClick={() => deleteCase(index)}
                  className="p-1 text-muted-foreground hover:text-destructive transition-colors"
                  title="Delete case"
                >
                  <Trash2 className="w-3.5 h-3.5" />
                </button>
              )}
            </div>
          )
        })}

        {/* Add case button */}
        {!isReadOnly && (
          <button
            onClick={addCase}
            className="w-full flex items-center justify-center gap-1.5 px-2 py-2 text-xs text-muted-foreground hover:text-foreground border border-dashed border-border rounded-lg hover:border-primary transition-colors"
          >
            <Plus className="w-3.5 h-3.5" />
            Add case
          </button>
        )}
      </div>
      </div>
    </ConfigurationPanel>
  )
}

import { useState, useCallback } from 'react'
import { useMemo } from 'react'
import type { Node } from '@xyflow/react'
import { ConfigurationPanel } from './ConfigurationPanel'
import { Trash2, GripVertical, Plus } from 'lucide-react'
import { cn } from '../../lib/utils'
import type { SwitchCase, SwitchNodeData } from './nodes/SwitchNode'
import { CELExpressionInput } from './CELInput'

interface SwitchConfigPanelProps {
  node: Node
  onUpdate: (nodeId: string, data: Partial<SwitchNodeData>) => void
  onClose: () => void
  onDelete: () => void
  bottomOffset?: number
  topOffset?: number
  isReadOnly?: boolean
}

export function SwitchConfigPanel({
  node,
  onUpdate,
  onClose,
  onDelete,
  bottomOffset,
  topOffset,
  isReadOnly = false,
}: SwitchConfigPanelProps) {
  const data = node.data as unknown as SwitchNodeData
  const cases = useMemo(() => data.cases || [], [data.cases])
  
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

  const addCase = useCallback(() => {
    // Insert new case before the last one (default)
    const newCase: SwitchCase = {
      id: `case-${Date.now()}`,
      condition: '',
      label: '',
    }
    const newCases = [...cases]
    // Insert before the last (default) case
    newCases.splice(cases.length - 1, 0, newCase)
    updateCases(newCases)
  }, [cases, updateCases])

  const deleteCase = useCallback((index: number) => {
    // Can't delete if only one case, and can't delete the default (last) case
    if (cases.length <= 1) return
    if (index === cases.length - 1) return // Can't delete default
    
    const newCases = cases.filter((_, i) => i !== index)
    updateCases(newCases)
  }, [cases, updateCases])

  const handleDragStart = (index: number) => {
    // Can't drag the default (last) case
    if (index === cases.length - 1) return
    setDraggedIndex(index)
  }

  const handleDragOver = (e: React.DragEvent, targetIndex: number) => {
    e.preventDefault()
    if (draggedIndex === null) return
    // Can't drop on the default case position
    if (targetIndex === cases.length - 1) return
  }

  const handleDrop = (e: React.DragEvent, targetIndex: number) => {
    e.preventDefault()
    if (draggedIndex === null || draggedIndex === targetIndex) {
      setDraggedIndex(null)
      return
    }
    // Can't drop on the default case position
    if (targetIndex === cases.length - 1) {
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
      <p className="text-xs text-muted-foreground -mt-2 mb-3">
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
          const isDefault = index === cases.length - 1
          const canDrag = !isDefault && cases.length > 2
          const canDelete = !isDefault && cases.length > 1
          
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
              <div className="flex-1 space-y-1.5">
                {/* Case header */}
                <div className="text-xs font-medium text-muted-foreground">
                  {isDefault ? 'default' : `case ${index + 1}`}
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
    </ConfigurationPanel>
  )
}

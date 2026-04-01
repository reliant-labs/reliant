import { useCallback, useRef, useState } from 'react'
import type { Node, Edge } from '@xyflow/react'

interface HistoryState {
  nodes: Node[]
  edges: Edge[]
}

interface UseUndoRedoOptions {
  maxHistorySize?: number
}

export function useUndoRedo(options: UseUndoRedoOptions = {}) {
  const { maxHistorySize = 50 } = options

  const [past, setPast] = useState<HistoryState[]>([])
  const [future, setFuture] = useState<HistoryState[]>([])
  
  // Track if we're currently undoing/redoing to prevent recording these changes
  const isUndoingRef = useRef(false)

  const takeSnapshot = useCallback((nodes: Node[], edges: Edge[]) => {
    // Don't record snapshots during undo/redo operations
    if (isUndoingRef.current) {
      return
    }

    setPast((prev) => {
      const newPast = [...prev, { nodes: JSON.parse(JSON.stringify(nodes)), edges: JSON.parse(JSON.stringify(edges)) }]
      // Limit history size
      if (newPast.length > maxHistorySize) {
        return newPast.slice(newPast.length - maxHistorySize)
      }
      return newPast
    })
    
    // Clear future when a new action is taken
    setFuture([])
  }, [maxHistorySize])

  const undo = useCallback((currentNodes: Node[], currentEdges: Edge[]): { nodes: Node[], edges: Edge[] } | null => {
    if (past.length === 0) return null

    isUndoingRef.current = true

    const previous = past[past.length - 1]
    const newPast = past.slice(0, past.length - 1)

    setPast(newPast)
    setFuture((prev) => [...prev, { nodes: JSON.parse(JSON.stringify(currentNodes)), edges: JSON.parse(JSON.stringify(currentEdges)) }])

    // Allow recording new changes after a short delay
    setTimeout(() => {
      isUndoingRef.current = false
    }, 100)

    return previous
  }, [past])

  const redo = useCallback((): { nodes: Node[], edges: Edge[] } | null => {
    if (future.length === 0) return null

    isUndoingRef.current = true

    const next = future[future.length - 1]
    const newFuture = future.slice(0, future.length - 1)

    setFuture(newFuture)
    setPast((prev) => [...prev, JSON.parse(JSON.stringify(next))])

    // Allow recording new changes after a short delay
    setTimeout(() => {
      isUndoingRef.current = false
    }, 100)

    return next
  }, [future])

  const canUndo = past.length > 0
  const canRedo = future.length > 0

  const clear = useCallback(() => {
    setPast([])
    setFuture([])
  }, [])

  return {
    takeSnapshot,
    undo,
    redo,
    canUndo,
    canRedo,
    clear,
  }
}

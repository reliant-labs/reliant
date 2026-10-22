import { useCallback, useEffect, useRef, useState } from 'react'

export type SaveStatus = 'saved' | 'saving' | 'unsaved' | 'error'

interface UseAutoSaveOptions {
  /** Delay in milliseconds before auto-save triggers (default: 2000ms) */
  debounceMs?: number
  /** Whether auto-save is enabled (default: true) */
  enabled?: boolean
  /** Called when save starts */
  onSaveStart?: () => void
  /** Called when save completes successfully */
  onSaveSuccess?: () => void
  /** Called when save fails */
  onSaveError?: (error: Error) => void
}

interface UseAutoSaveResult {
  /** Current save status */
  status: SaveStatus
  /** Whether there are unsaved changes */
  hasUnsavedChanges: boolean
  /** Call this when content changes */
  markDirty: () => void
  /** Call this when a manual save is triggered */
  markSaved: () => void
  /** Force an immediate save */
  saveNow: () => void
  /** Reset the auto-save state (e.g., when loading new content) */
  reset: () => void
}

/**
 * Hook for managing auto-save functionality with debouncing
 * 
 * @param saveFn - The async function to call when saving
 * @param options - Configuration options
 * @returns Auto-save state and controls
 */
export function useAutoSave(
  saveFn: () => Promise<void>,
  options: UseAutoSaveOptions = {}
): UseAutoSaveResult {
  const {
    debounceMs = 2000,
    enabled = true,
    onSaveStart,
    onSaveSuccess,
    onSaveError,
  } = options

  const [status, setStatus] = useState<SaveStatus>('saved')
  const [hasUnsavedChanges, setHasUnsavedChanges] = useState(false)
  
  // Refs to track timer and save state
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const isSavingRef = useRef(false)
  const pendingSaveRef = useRef(false)
  
  // Store the save function in a ref to avoid re-creating callbacks
  const saveFnRef = useRef(saveFn)
  saveFnRef.current = saveFn

  // Clear any pending timer
  const clearTimer = useCallback(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current)
      timerRef.current = null
    }
  }, [])

  // Perform the actual save
  const performSave = useCallback(async () => {
    // Prevent concurrent saves
    if (isSavingRef.current) {
      pendingSaveRef.current = true
      return
    }

    // Don't save if there are no unsaved changes
    if (!hasUnsavedChanges) {
      return
    }

    isSavingRef.current = true
    setStatus('saving')
    onSaveStart?.()

    try {
      await saveFnRef.current()
      setStatus('saved')
      setHasUnsavedChanges(false)
      onSaveSuccess?.()
    } catch (error) {
      setStatus('error')
      onSaveError?.(error instanceof Error ? error : new Error(String(error)))
    } finally {
      isSavingRef.current = false
      
      // If another save was requested while we were saving, do it now
      if (pendingSaveRef.current) {
        pendingSaveRef.current = false
        performSave()
      }
    }
  }, [hasUnsavedChanges, onSaveStart, onSaveSuccess, onSaveError])

  // Mark content as dirty (changed)
  const markDirty = useCallback(() => {
    setHasUnsavedChanges(true)
    setStatus('unsaved')

    // Clear existing timer
    clearTimer()

    // Set up new debounced save
    if (enabled) {
      timerRef.current = setTimeout(() => {
        performSave()
      }, debounceMs)
    }
  }, [enabled, debounceMs, clearTimer, performSave])

  // Mark as saved (e.g., after manual save)
  const markSaved = useCallback(() => {
    clearTimer()
    setHasUnsavedChanges(false)
    setStatus('saved')
  }, [clearTimer])

  // Force immediate save
  const saveNow = useCallback(() => {
    clearTimer()
    performSave()
  }, [clearTimer, performSave])

  // Reset state (e.g., when loading new content)
  const reset = useCallback(() => {
    clearTimer()
    setHasUnsavedChanges(false)
    setStatus('saved')
    isSavingRef.current = false
    pendingSaveRef.current = false
  }, [clearTimer])

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      clearTimer()
    }
  }, [clearTimer])

  // Handle page unload with unsaved changes
  useEffect(() => {
    const handleBeforeUnload = (e: BeforeUnloadEvent) => {
      if (hasUnsavedChanges) {
        e.preventDefault()
        e.returnValue = ''
        return ''
      }
    }

    window.addEventListener('beforeunload', handleBeforeUnload)
    return () => window.removeEventListener('beforeunload', handleBeforeUnload)
  }, [hasUnsavedChanges])

  return {
    status,
    hasUnsavedChanges,
    markDirty,
    markSaved,
    saveNow,
    reset,
  }
}

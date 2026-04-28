import { Hand, MousePointer2, Undo, Redo, ZoomIn, ZoomOut, Maximize2, Lock, Unlock, Wand2 } from 'lucide-react'
import { cn } from '../../lib/utils'

export type InteractionMode = 'pan' | 'select'

interface FloatingToolbarProps {
  mode: InteractionMode
  onModeChange: (mode: InteractionMode) => void
  onUndo: () => void
  onRedo: () => void
  canUndo: boolean
  canRedo: boolean
  onZoomIn?: () => void
  onZoomOut?: () => void
  onFitView?: () => void
  onOrganizeNodes?: () => void
  isLocked?: boolean
  onLockToggle?: () => void
  /** Hide edit controls (undo/redo, lock) for read-only mode */
  isReadOnly?: boolean
}

export function FloatingToolbar({
  mode,
  onModeChange,
  onUndo,
  onRedo,
  canUndo,
  canRedo,
  onZoomIn,
  onZoomOut,
  onFitView,
  onOrganizeNodes,
  isLocked,
  onLockToggle,
  isReadOnly = false,
}: FloatingToolbarProps) {
  const toolbarButtonClass = (active = false, disabled = false) => cn(
    'inline-flex h-9 w-9 items-center justify-center rounded-lg transition-all',
    active
      ? 'bg-primary text-primary-foreground shadow-sm shadow-primary/20'
      : 'text-muted-foreground hover:bg-muted hover:text-foreground',
    disabled && 'cursor-not-allowed opacity-40 hover:bg-transparent hover:text-muted-foreground',
  )

  return (
    <div className="flex items-center gap-1 rounded-2xl border border-border/80 bg-card/95 p-1.5 shadow-xl shadow-black/10 backdrop-blur-sm">
      <button
        onClick={() => onModeChange('pan')}
        className={toolbarButtonClass(mode === 'pan')}
        title="Pan Mode (Hand Tool)"
        aria-label="Pan Mode"
      >
        <Hand className="w-4 h-4" />
      </button>

      <button
        onClick={() => onModeChange('select')}
        className={toolbarButtonClass(mode === 'select')}
        title="Selection Mode (Box Select)"
        aria-label="Selection Mode"
      >
        <MousePointer2 className="w-4 h-4" />
      </button>

      {!isReadOnly && (
        <>
          {onLockToggle && (
            <button
              onClick={onLockToggle}
              className={toolbarButtonClass(isLocked)}
              title={isLocked ? "Unlock Nodes" : "Lock Nodes"}
              aria-label={isLocked ? "Unlock Nodes" : "Lock Nodes"}
            >
              {isLocked ? <Lock className="w-4 h-4" /> : <Unlock className="w-4 h-4" />}
            </button>
          )}

          <div className="mx-1 h-6 w-px bg-border/80" />

          <button
            onClick={onUndo}
            disabled={!canUndo}
            className={toolbarButtonClass(false, !canUndo)}
            title="Undo (Ctrl+Z)"
            aria-label="Undo"
          >
            <Undo className="w-4 h-4" />
          </button>

          <button
            onClick={onRedo}
            disabled={!canRedo}
            className={toolbarButtonClass(false, !canRedo)}
            title="Redo (Ctrl+Shift+Z)"
            aria-label="Redo"
          >
            <Redo className="w-4 h-4" />
          </button>
        </>
      )}

      {(onZoomIn || onZoomOut || onFitView) && (
        <>
          <div className="mx-1 h-6 w-px bg-border/80" />

          {onZoomOut && (
            <button
              onClick={onZoomOut}
              className={toolbarButtonClass()}
              title="Zoom Out"
              aria-label="Zoom Out"
            >
              <ZoomOut className="w-4 h-4" />
            </button>
          )}

          {onZoomIn && (
            <button
              onClick={onZoomIn}
              className={toolbarButtonClass()}
              title="Zoom In"
              aria-label="Zoom In"
            >
              <ZoomIn className="w-4 h-4" />
            </button>
          )}

          {onFitView && (
            <button
              onClick={onFitView}
              className={toolbarButtonClass()}
              title="Fit to View"
              aria-label="Fit to View"
            >
              <Maximize2 className="w-4 h-4" />
            </button>
          )}

          {!isReadOnly && onOrganizeNodes && (
            <button
              onClick={onOrganizeNodes}
              className={toolbarButtonClass()}
              title="Organize Nodes"
              aria-label="Organize Nodes"
            >
              <Wand2 className="w-4 h-4" />
            </button>
          )}
        </>
      )}
    </div>
  )
}

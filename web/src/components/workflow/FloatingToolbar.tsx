import { Hand, MousePointer2, Undo, Redo, ZoomIn, ZoomOut, Maximize2, Lock, Unlock, Wand2 } from 'lucide-react'
import { Tooltip } from "../ui/Tooltip";
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
      <Tooltip content="Pan Mode (Hand Tool)" placement="top" delay={300} wrapperClassName="inline-flex">
<button
        onClick={() => onModeChange('pan')}
        className={toolbarButtonClass(mode === 'pan')}
        aria-label="Pan Mode"
      >
        <Hand className="w-4 h-4" />
      </button>
</Tooltip>

      <Tooltip content="Selection Mode (Box Select)" placement="top" delay={300} wrapperClassName="inline-flex">
<button
        onClick={() => onModeChange('select')}
        className={toolbarButtonClass(mode === 'select')}
        aria-label="Selection Mode"
      >
        <MousePointer2 className="w-4 h-4" />
      </button>
</Tooltip>

      {!isReadOnly && (
        <>
          {onLockToggle && (
            <Tooltip content={isLocked ? "Unlock Nodes" : "Lock Nodes"} placement="top" delay={300} wrapperClassName="inline-flex">
<button
              onClick={onLockToggle}
              className={toolbarButtonClass(isLocked)}
              aria-label={isLocked ? "Unlock Nodes" : "Lock Nodes"}
            >
              {isLocked ? <Lock className="w-4 h-4" /> : <Unlock className="w-4 h-4" />}
            </button>
</Tooltip>
          )}

          <div className="mx-1 h-6 w-px bg-border/80" />

          <Tooltip content="Undo (Ctrl+Z)" placement="top" delay={300} wrapperClassName="inline-flex">
<button
            onClick={onUndo}
            disabled={!canUndo}
            className={toolbarButtonClass(false, !canUndo)}
            aria-label="Undo"
          >
            <Undo className="w-4 h-4" />
          </button>
</Tooltip>

          <Tooltip content="Redo (Ctrl+Shift+Z)" placement="top" delay={300} wrapperClassName="inline-flex">
<button
            onClick={onRedo}
            disabled={!canRedo}
            className={toolbarButtonClass(false, !canRedo)}
            aria-label="Redo"
          >
            <Redo className="w-4 h-4" />
          </button>
</Tooltip>
        </>
      )}

      {(onZoomIn || onZoomOut || onFitView) && (
        <>
          <div className="mx-1 h-6 w-px bg-border/80" />

          {onZoomOut && (
            <Tooltip content="Zoom Out" placement="top" delay={300} wrapperClassName="inline-flex">
<button
              onClick={onZoomOut}
              className={toolbarButtonClass()}
              aria-label="Zoom Out"
            >
              <ZoomOut className="w-4 h-4" />
            </button>
</Tooltip>
          )}

          {onZoomIn && (
            <Tooltip content="Zoom In" placement="top" delay={300} wrapperClassName="inline-flex">
<button
              onClick={onZoomIn}
              className={toolbarButtonClass()}
              aria-label="Zoom In"
            >
              <ZoomIn className="w-4 h-4" />
            </button>
</Tooltip>
          )}

          {onFitView && (
            <Tooltip content="Fit to View" placement="top" delay={300} wrapperClassName="inline-flex">
<button
              onClick={onFitView}
              className={toolbarButtonClass()}
              aria-label="Fit to View"
            >
              <Maximize2 className="w-4 h-4" />
            </button>
</Tooltip>
          )}

          {!isReadOnly && onOrganizeNodes && (
            <Tooltip content="Organize Nodes" placement="top" delay={300} wrapperClassName="inline-flex">
<button
              onClick={onOrganizeNodes}
              className={toolbarButtonClass()}
              aria-label="Organize Nodes"
            >
              <Wand2 className="w-4 h-4" />
            </button>
</Tooltip>
          )}
        </>
      )}
    </div>
  )
}

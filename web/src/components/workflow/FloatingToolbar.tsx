import { Hand, MousePointer2, Undo, Redo, ZoomIn, ZoomOut, Maximize2, Lock, Unlock, Wand2 } from 'lucide-react'

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
  return (
    <div className="flex items-center gap-2 bg-card border border-border rounded-xl shadow-lg p-2">
      {/* Pan Mode Button */}
      <button
        onClick={() => onModeChange('pan')}
        className={`
          p-2.5 rounded-lg transition-all
          ${mode === 'pan'
            ? 'bg-blue-500 text-white shadow-sm'
            : 'hover:bg-muted text-muted-foreground hover:text-foreground'
          }
        `}
        title="Pan Mode (Hand Tool)"
        aria-label="Pan Mode"
      >
        <Hand className="w-5 h-5" />
      </button>

      {/* Selection Mode Button */}
      <button
        onClick={() => onModeChange('select')}
        className={`
          p-2.5 rounded-lg transition-all
          ${mode === 'select'
            ? 'bg-blue-500 text-white shadow-sm'
            : 'hover:bg-muted text-muted-foreground hover:text-foreground'
          }
        `}
        title="Selection Mode (Box Select)"
        aria-label="Selection Mode"
      >
        <MousePointer2 className="w-5 h-5" />
      </button>

      {/* Edit controls - hidden in read-only mode */}
      {!isReadOnly && (
        <>
          {/* Lock Button */}
          {onLockToggle && (
            <button
              onClick={onLockToggle}
              className={`
                p-2.5 rounded-lg transition-all
                ${isLocked
                  ? 'bg-amber-500 text-white shadow-sm'
                  : 'hover:bg-muted text-muted-foreground hover:text-foreground'
                }
              `}
              title={isLocked ? "Unlock Nodes" : "Lock Nodes"}
              aria-label={isLocked ? "Unlock Nodes" : "Lock Nodes"}
            >
              {isLocked ? <Lock className="w-5 h-5" /> : <Unlock className="w-5 h-5" />}
            </button>
          )}

          {/* Divider */}
          <div className="w-px h-6 bg-border" />

          {/* Undo Button */}
          <button
            onClick={onUndo}
            disabled={!canUndo}
            className={`
              p-2.5 rounded-lg transition-all
              ${canUndo
                ? 'hover:bg-muted text-muted-foreground hover:text-foreground'
                : 'text-muted-foreground/40 cursor-not-allowed'
              }
            `}
            title="Undo (Ctrl+Z)"
            aria-label="Undo"
          >
            <Undo className="w-5 h-5" />
          </button>

          {/* Redo Button */}
          <button
            onClick={onRedo}
            disabled={!canRedo}
            className={`
              p-2.5 rounded-lg transition-all
              ${canRedo
                ? 'hover:bg-muted text-muted-foreground hover:text-foreground'
                : 'text-muted-foreground/40 cursor-not-allowed'
              }
            `}
            title="Redo (Ctrl+Shift+Z)"
            aria-label="Redo"
          >
            <Redo className="w-5 h-5" />
          </button>
        </>
      )}

      {/* Zoom/View controls - only show if handlers provided */}
      {(onZoomIn || onZoomOut || onFitView) && (
        <>
          {/* Divider */}
          <div className="w-px h-6 bg-border" />

          {/* Zoom Out Button */}
          {onZoomOut && (
            <button
              onClick={onZoomOut}
              className="p-2.5 rounded-lg transition-all hover:bg-muted text-muted-foreground hover:text-foreground"
              title="Zoom Out"
              aria-label="Zoom Out"
            >
              <ZoomOut className="w-5 h-5" />
            </button>
          )}

          {/* Zoom In Button */}
          {onZoomIn && (
            <button
              onClick={onZoomIn}
              className="p-2.5 rounded-lg transition-all hover:bg-muted text-muted-foreground hover:text-foreground"
              title="Zoom In"
              aria-label="Zoom In"
            >
              <ZoomIn className="w-5 h-5" />
            </button>
          )}

          {/* Fit View Button */}
          {onFitView && (
            <button
              onClick={onFitView}
              className="p-2.5 rounded-lg transition-all hover:bg-muted text-muted-foreground hover:text-foreground"
              title="Fit to View"
              aria-label="Fit to View"
            >
              <Maximize2 className="w-5 h-5" />
            </button>
          )}

          {/* Organize Layout Button */}
          {!isReadOnly && onOrganizeNodes && (
            <button
              onClick={onOrganizeNodes}
              className="p-2.5 rounded-lg transition-all hover:bg-muted text-muted-foreground hover:text-foreground"
              title="Organize Nodes"
              aria-label="Organize Nodes"
            >
              <Wand2 className="w-5 h-5" />
            </button>
          )}
        </>
      )}
    </div>
  )
}

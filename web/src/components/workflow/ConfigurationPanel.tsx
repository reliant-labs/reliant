import type { ReactNode } from 'react'
import { ExternalLink, Pencil, Trash2, X } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'

interface ConfigurationPanelProps {
  title: string
  subtitle?: string
  subtitleMono?: boolean // Whether subtitle uses monospace font (default: true for node IDs)
  onSubtitleClick?: () => void
  onClose: () => void
  onDelete?: () => void  // Optional - if not provided, shows X to close instead of trash
  deleteLabel?: string // Custom label for delete button tooltip (e.g., "Ungroup")
  children: ReactNode
  /** Optional element rendered between header and scrollable content (e.g., tab bar) */
  tabBar?: ReactNode
  bottomOffset?: number // Space to reserve at bottom (e.g., for chat panel)
  topOffset?: number // Distance from top (to align with left sidebar)
  isExpanded?: boolean // Whether the panel should use expanded positioning
}

export function ConfigurationPanel({
  title,
  subtitle,
  subtitleMono = true,
  onSubtitleClick,
  onClose,
  onDelete,
  deleteLabel = 'Delete',
  children,
  tabBar,
  bottomOffset = 0,
  topOffset = 64, // Default to top-16 (64px) to match left sidebar base position
  isExpanded = true, // Default to expanded to prevent dropdown clipping
}: ConfigurationPanelProps) {
  const handleDelete = () => {
    onDelete?.()
    onClose()
  }

  return (
    <div
      className="config-panel-v2 absolute right-3 w-[420px] bg-card border border-border rounded-2xl shadow-xl flex flex-col z-40 transition-all duration-300"
      style={{
        top: `${topOffset}px`,
        maxHeight: isExpanded
          ? `calc(100vh - ${topOffset}px - 1rem - ${bottomOffset}px)`
          : '550px',
        bottom: isExpanded ? `${Math.max(16, bottomOffset + 16)}px` : 'auto'
      }}
    >
      {/* Header */}
      <div className="px-4 py-3 border-b border-border flex items-start justify-between flex-shrink-0">
        <div className="flex-1">
          <h3 className="font-semibold text-foreground text-base">{title}</h3>
          {subtitle && (
            <div className="flex items-center gap-1.5 mt-0.5">
              <p
                className={`text-xs ${subtitleMono ? 'font-mono ' : ''}text-muted-foreground truncate${onSubtitleClick ? ' cursor-pointer hover:text-foreground transition-colors' : ''}`}
                onClick={onSubtitleClick}
                title={onSubtitleClick ? 'Click to rename' : undefined}
              >{subtitle}</p>
              {onSubtitleClick && (
                <Pencil className="w-3 h-3 text-muted-foreground flex-shrink-0" />
              )}
              <a
                href="https://docs.reliantlabs.io/reference/nodes/"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center text-muted-foreground/50 hover:text-muted-foreground transition-colors flex-shrink-0 ml-auto"
                title="Node reference docs"
              >
                <ExternalLink className="w-3 h-3" />
              </a>
            </div>
          )}
          {!subtitle && (
            <div className="mt-0.5">
              <a
                href="https://docs.reliantlabs.io/reference/nodes/"
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center text-muted-foreground/50 hover:text-muted-foreground transition-colors"
                title="Node reference docs"
              >
                <ExternalLink className="w-3 h-3" />
              </a>
            </div>
          )}
        </div>
        {onDelete ? (
          <Tooltip content={deleteLabel} placement="left" delay={300}>
            <button
              onClick={handleDelete}
              className="text-muted-foreground hover:text-destructive transition-colors flex-shrink-0 ml-4"
              aria-label="Delete"
            >
              <Trash2 className="w-4 h-4" />
            </button>
          </Tooltip>
        ) : (
          <Tooltip content="Close" placement="left" delay={300}>
            <button
              onClick={onClose}
              className="text-muted-foreground hover:text-foreground transition-colors flex-shrink-0 ml-4"
              aria-label="Close"
            >
              <X className="w-4 h-4" />
            </button>
          </Tooltip>
        )}
      </div>

      {/* Tab bar (optional, between header and scrollable content) */}
      {tabBar}

      {/* Content */}
      <div className="flex-1 p-3 pb-3 space-y-3 overflow-y-auto overflow-x-hidden">
        {children}
      </div>
    </div>
  )
}
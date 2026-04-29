import type { ReactNode } from 'react'
import { ExternalLink, Trash2, X } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import './config/config-panel.css'

export type ConfigurationPanelAccent =
  | 'default'
  | 'purple'
  | 'violet'
  | 'indigo'
  | 'blue'
  | 'sky'
  | 'cyan'
  | 'teal'
  | 'emerald'
  | 'amber'
  | 'orange'
  | 'pink'
  | 'rose'

interface ConfigurationPanelProps {
  title: string
  icon?: ReactNode
  subtitle?: string
  subtitleMono?: boolean
  accent?: ConfigurationPanelAccent
  onSubtitleClick?: () => void
  onClose: () => void
  onDelete?: () => void
  deleteLabel?: string
  children: ReactNode
  tabBar?: ReactNode
  bottomOffset?: number
  topOffset?: number
  isExpanded?: boolean
}

export function ConfigurationPanel({
  title,
  icon,
  subtitle,
  subtitleMono = true,
  accent = 'default',
  onSubtitleClick,
  onClose,
  onDelete,
  deleteLabel = 'Delete',
  children,
  tabBar,
  bottomOffset = 0,
  topOffset = 64,
  isExpanded = true,
}: ConfigurationPanelProps) {
  const handleDelete = () => {
    onDelete?.()
    onClose()
  }

  const accentClassName = accent === 'default' ? '' : ` config-panel-v2--${accent}`

  return (
    <div
      className={`config-panel-v2${accentClassName}`}
      style={{
        top: `${topOffset}px`,
        maxHeight: isExpanded
          ? `calc(100vh - ${topOffset}px - 1rem - ${bottomOffset}px)`
          : '550px',
        bottom: isExpanded ? `${Math.max(16, bottomOffset + 16)}px` : 'auto',
      }}
    >
      <div className="cpv2-panel-header">
        {icon && <div className="cpv2-panel-icon" aria-hidden>{icon}</div>}
        <div className="min-w-0 flex-1">
          <h3 className="cpv2-panel-title">{title}</h3>
          <div className="flex items-center gap-1.5 mt-0.5 min-w-0">
            {subtitle && (
              <button
                type="button"
                className={`cpv2-panel-subtitle ${subtitleMono ? 'font-mono ' : ''}${onSubtitleClick ? 'clickable' : ''}`}
                onClick={onSubtitleClick}
                title={onSubtitleClick ? 'Click to rename' : undefined}
                disabled={!onSubtitleClick}
              >
                {subtitle}
              </button>
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
        </div>

        <div className="cpv2-header-actions">
          {onDelete && (
            <Tooltip content={deleteLabel} placement="left" delay={300}>
              <button
                type="button"
                onClick={handleDelete}
                className="cpv2-header-btn delete"
                aria-label="Delete"
              >
                <Trash2 />
              </button>
            </Tooltip>
          )}
          <Tooltip content="Close" placement="left" delay={300}>
            <button
              type="button"
              onClick={onClose}
              className="cpv2-header-btn"
              aria-label="Close"
            >
              <X />
            </button>
          </Tooltip>
        </div>
      </div>

      {tabBar}

      <div className="cpv2-panel-content">
        {children}
      </div>
    </div>
  )
}
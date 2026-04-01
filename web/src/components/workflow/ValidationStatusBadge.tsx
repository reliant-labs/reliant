import { useState, useRef, useEffect } from 'react'
import { CheckCircle, AlertTriangle, XCircle, ChevronDown } from 'lucide-react'
import type { ValidationError } from '../../api/workflow-grpc'
import { cn } from '../../lib/utils'

export type ValidationStatus = 'valid' | 'invalid' | 'validating' | 'unknown'

interface ValidationStatusBadgeProps {
  status: ValidationStatus
  errors: ValidationError[]
  onNodeClick?: (nodeId: string) => void
  className?: string
}

export function ValidationStatusBadge({
  status,
  errors,
  onNodeClick,
  className = '',
}: ValidationStatusBadgeProps) {
  const [showPopover, setShowPopover] = useState(false)
  const popoverRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)

  // Close popover when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (
        popoverRef.current &&
        !popoverRef.current.contains(event.target as Node) &&
        buttonRef.current &&
        !buttonRef.current.contains(event.target as Node)
      ) {
        setShowPopover(false)
      }
    }

    if (showPopover) {
      document.addEventListener('mousedown', handleClickOutside)
      return () => document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [showPopover])

  const getStatusDisplay = () => {
    switch (status) {
      case 'valid':
        return {
          icon: <CheckCircle className="w-3.5 h-3.5" />,
          text: 'Valid',
          color: 'text-green-500',
          bgColor: 'bg-green-500/10',
          borderColor: 'border-green-500/20',
        }
      case 'invalid':
        return {
          icon: <AlertTriangle className="w-3.5 h-3.5" />,
          text: `${errors.length} error${errors.length !== 1 ? 's' : ''}`,
          color: 'text-amber-500',
          bgColor: 'bg-amber-500/10',
          borderColor: 'border-amber-500/20',
        }
      case 'validating':
        return {
          icon: <div className="w-3.5 h-3.5 border-2 border-blue-500 border-t-transparent rounded-full animate-spin" />,
          text: 'Validating...',
          color: 'text-blue-500',
          bgColor: 'bg-blue-500/10',
          borderColor: 'border-blue-500/20',
        }
      case 'unknown':
      default:
        return {
          icon: <XCircle className="w-3.5 h-3.5" />,
          text: 'Unknown',
          color: 'text-muted-foreground',
          bgColor: 'bg-muted',
          borderColor: 'border-border',
        }
    }
  }

  const { icon, text, color, bgColor, borderColor } = getStatusDisplay()

  const hasErrors = status === 'invalid' && errors.length > 0

  return (
    <div className={cn('relative', className)}>
      <button
        ref={buttonRef}
        onClick={() => hasErrors && setShowPopover(!showPopover)}
        className={cn(
          'flex items-center gap-1.5 px-2.5 py-1 rounded-full border text-sm font-medium transition-all',
          bgColor,
          borderColor,
          color,
          hasErrors && 'cursor-pointer hover:opacity-80',
          !hasErrors && 'cursor-default'
        )}
        title={hasErrors ? 'Click to view validation errors' : undefined}
      >
        {icon}
        <span>{text}</span>
        {hasErrors && (
          <ChevronDown
            className={cn(
              'w-3.5 h-3.5 transition-transform',
              showPopover && 'rotate-180'
            )}
          />
        )}
      </button>

      {/* Error Popover */}
      {showPopover && hasErrors && (
        <div
          ref={popoverRef}
          className="absolute top-full left-0 mt-2 w-80 max-h-64 overflow-y-auto bg-popover border border-border rounded-lg shadow-lg z-50"
        >
          <div className="p-3 border-b border-border">
            <h4 className="font-semibold text-sm text-foreground">
              Validation Errors ({errors.length})
            </h4>
            <p className="text-xs text-muted-foreground mt-0.5">
              Fix these errors to make your workflow usable
            </p>
          </div>
          <ul className="p-2 space-y-1">
            {errors.map((error, index) => (
              <li
                key={index}
                className={cn(
                  'p-2 rounded-md bg-muted/50 text-sm',
                  error.edgeFrom && onNodeClick && 'cursor-pointer hover:bg-muted'
                )}
                onClick={() => {
                  if (error.edgeFrom && onNodeClick) {
                    onNodeClick(error.edgeFrom)
                    setShowPopover(false)
                  }
                }}
              >
                <div className="font-medium text-foreground">{error.message}</div>
                {error.suggestion && (
                  <div className="text-xs text-muted-foreground mt-1">
                    {error.suggestion}
                  </div>
                )}
                {error.edgeFrom && (
                  <div className="text-xs text-primary mt-1 flex items-center gap-1">
                    <span className="text-muted-foreground">Node:</span>
                    <code className="bg-muted px-1 rounded">{error.edgeFrom}</code>
                  </div>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

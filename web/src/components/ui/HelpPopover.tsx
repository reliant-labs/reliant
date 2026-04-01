import { useState, useRef, useEffect, useLayoutEffect, useCallback } from 'react'
import { HelpCircle, X } from 'lucide-react'
import { createPortal } from 'react-dom'

interface HelpPopoverProps {
  /** Help content - can be string or JSX */
  content: React.ReactNode
  /** Optional title for the popover */
  title?: string
  /** Size of the trigger icon */
  iconSize?: 'sm' | 'md'
}

interface PopoverPosition {
  top: number
  left: number
}

const POPOVER_WIDTH = 280
const FALLBACK_POPOVER_HEIGHT = 180
const VIEWPORT_PADDING = 16
const POPOVER_GAP = 8

function calculatePopoverPosition(
  triggerRect: DOMRect,
  popoverWidth: number,
  popoverHeight: number
): PopoverPosition {
  // Default: below trigger, left-aligned with trigger
  let top = triggerRect.bottom + POPOVER_GAP
  let left = triggerRect.left

  // Prefer above if below would overflow viewport
  if (top + popoverHeight > window.innerHeight - VIEWPORT_PADDING) {
    top = triggerRect.top - popoverHeight - POPOVER_GAP
  }

  // Clamp to viewport bounds with padding
  const maxTop = Math.max(VIEWPORT_PADDING, window.innerHeight - popoverHeight - VIEWPORT_PADDING)
  const maxLeft = Math.max(VIEWPORT_PADDING, window.innerWidth - popoverWidth - VIEWPORT_PADDING)

  top = Math.min(Math.max(top, VIEWPORT_PADDING), maxTop)
  left = Math.min(Math.max(left, VIEWPORT_PADDING), maxLeft)

  return { top, left }
}

/**
 * HelpPopover - A clickable help icon that shows a popover with help content
 * Better UX than hover tooltips for complex help information
 */
export function HelpPopover({
  content,
  title,
  iconSize = 'sm'
}: HelpPopoverProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [position, setPosition] = useState<PopoverPosition | null>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const popoverRef = useRef<HTMLDivElement>(null)

  const setInitialPosition = useCallback(() => {
    if (!triggerRef.current) return

    const triggerRect = triggerRef.current.getBoundingClientRect()
    setPosition(
      calculatePopoverPosition(triggerRect, POPOVER_WIDTH, FALLBACK_POPOVER_HEIGHT)
    )
  }, [])

  const updatePosition = useCallback(() => {
    if (!triggerRef.current) return

    const triggerRect = triggerRef.current.getBoundingClientRect()
    const popoverWidth = popoverRef.current?.offsetWidth ?? POPOVER_WIDTH
    const popoverHeight = popoverRef.current?.offsetHeight ?? FALLBACK_POPOVER_HEIGHT

    setPosition(calculatePopoverPosition(triggerRect, popoverWidth, popoverHeight))
  }, [])

  // Recalculate position immediately on open (before paint) to avoid first-frame drift
  useLayoutEffect(() => {
    if (!isOpen) return
    updatePosition()
  }, [isOpen, updatePosition])

  // Keep popover anchored while viewport/layout changes
  useEffect(() => {
    if (!isOpen) return

    const handleReposition = () => updatePosition()

    window.addEventListener('resize', handleReposition)
    // Capture scroll on any ancestor (including nested scroll containers)
    window.addEventListener('scroll', handleReposition, true)

    return () => {
      window.removeEventListener('resize', handleReposition)
      window.removeEventListener('scroll', handleReposition, true)
    }
  }, [isOpen, updatePosition])

  // Close on click outside
  useEffect(() => {
    if (!isOpen) return

    const handleClickOutside = (e: MouseEvent) => {
      if (
        popoverRef.current &&
        !popoverRef.current.contains(e.target as Node) &&
        triggerRef.current &&
        !triggerRef.current.contains(e.target as Node)
      ) {
        setIsOpen(false)
      }
    }

    const handleEscape = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setIsOpen(false)
      }
    }

    document.addEventListener('mousedown', handleClickOutside)
    document.addEventListener('keydown', handleEscape)

    return () => {
      document.removeEventListener('mousedown', handleClickOutside)
      document.removeEventListener('keydown', handleEscape)
    }
  }, [isOpen])

  const handleToggle = () => {
    setIsOpen((prev) => {
      const next = !prev
      if (next) {
        // Set a best-effort anchored position synchronously so first render is near trigger
        setInitialPosition()
      }
      return next
    })
  }

  const iconClass = iconSize === 'sm' ? 'w-3.5 h-3.5' : 'w-4 h-4'

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        onClick={handleToggle}
        className={`
          inline-flex items-center justify-center rounded-full
          text-muted-foreground hover:text-foreground hover:bg-muted
          transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-1
          ${iconSize === 'sm' ? 'p-0.5' : 'p-1'}
        `}
        aria-label="Help"
        aria-expanded={isOpen}
      >
        <HelpCircle className={iconClass} />
      </button>

      {isOpen && position && createPortal(
        <div
          ref={popoverRef}
          className="fixed z-50"
          style={{ top: position.top, left: position.left }}
        >
          <div className="w-[280px] rounded-lg border border-border bg-popover shadow-lg">
            {/* Header */}
            <div className="flex items-center justify-between px-3 py-2 border-b border-border">
              <span className="text-sm font-medium text-foreground">
                {title || 'Help'}
              </span>
              <button
                type="button"
                onClick={() => setIsOpen(false)}
                className="p-0.5 rounded text-muted-foreground hover:text-foreground hover:bg-muted transition-colors"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>

            {/* Content */}
            <div className="px-3 py-2 text-sm text-muted-foreground">
              {typeof content === 'string' ? (
                <p className="whitespace-pre-line">{content}</p>
              ) : (
                content
              )}
            </div>
          </div>
        </div>,
        document.body
      )}
    </>
  )
}

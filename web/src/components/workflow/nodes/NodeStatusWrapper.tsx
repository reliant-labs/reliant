/**
 * NodeStatusWrapper - Unified container for workflow nodes with execution status styling
 * 
 * This wrapper handles all execution status visual feedback (border color, background, animation)
 * so individual node components only need to focus on their content.
 * 
 * Usage:
 * ```tsx
 * <NodeStatusWrapper
 *   status={executionStatus}
 *   selected={selected}
 *   theme="green" // or "indigo", "blue", "teal", etc.
 *   minWidth={200}
 *   maxWidth={300}
 * >
 *   <Handle ... />
 *   <YourNodeContent />
 * </NodeStatusWrapper>
 * ```
 */

import type { ReactNode } from 'react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'

// Theme color definitions - maps theme name to Tailwind border classes + CSS variable backgrounds
// Background colors use CSS variables defined in workflow-theme.css that switch with .dark class
const THEME_COLORS = {
  green: {
    defaultBorder: 'border-green-300',
    selectedBorder: 'border-green-500 shadow-lg ring-2 ring-green-200',
    bgVar: '--node-green-bg',
  },
  indigo: {
    defaultBorder: 'border-indigo-300',
    selectedBorder: 'border-indigo-500 shadow-lg ring-2 ring-indigo-200',
    bgVar: '--node-indigo-bg',
  },
  blue: {
    defaultBorder: 'border-blue-300',
    selectedBorder: 'border-blue-500 shadow-lg ring-2 ring-blue-200',
    bgVar: '--node-blue-bg',
  },
  teal: {
    defaultBorder: 'border-teal-300',
    selectedBorder: 'border-teal-600 shadow-lg ring-2 ring-teal-200',
    bgVar: '--node-teal-bg',
  },
  amber: {
    defaultBorder: 'border-amber-300',
    selectedBorder: 'border-amber-600 shadow-lg ring-2 ring-amber-200',
    bgVar: '--node-amber-bg',
  },
  pink: {
    defaultBorder: 'border-pink-300',
    selectedBorder: 'border-pink-600 shadow-lg ring-2 ring-pink-200',
    bgVar: '--node-pink-bg',
  },
  rose: {
    defaultBorder: 'border-rose-300',
    selectedBorder: 'border-rose-500 shadow-lg ring-2 ring-rose-200',
    bgVar: '--node-rose-bg',
  },
  fuchsia: {
    defaultBorder: 'border-fuchsia-300',
    selectedBorder: 'border-fuchsia-500 shadow-lg ring-2 ring-fuchsia-200',
    bgVar: '--node-fuchsia-bg',
  },
  purple: {
    defaultBorder: 'border-purple-300',
    selectedBorder: 'border-purple-600 shadow-lg ring-2 ring-purple-200',
    bgVar: '--node-purple-bg',
  },
  orange: {
    defaultBorder: 'border-orange-400',
    selectedBorder: 'border-orange-500 shadow-lg ring-2 ring-orange-200',
    bgVar: '--node-orange-bg',
  },
  emerald: {
    defaultBorder: 'border-emerald-400',
    selectedBorder: 'border-emerald-500 shadow-lg ring-2 ring-emerald-200',
    bgVar: '--node-emerald-bg',
  },
  red: {
    defaultBorder: 'border-red-300',
    selectedBorder: 'border-red-500 shadow-lg ring-2 ring-red-200',
    bgVar: '--node-red-bg',
  },
  yellow: {
    defaultBorder: 'border-yellow-300',
    selectedBorder: 'border-yellow-500 shadow-lg ring-2 ring-yellow-200',
    bgVar: '--node-yellow-bg',
  },
  sky: {
    defaultBorder: 'border-sky-300',
    selectedBorder: 'border-sky-500 shadow-lg ring-2 ring-sky-200',
    bgVar: '--node-sky-bg',
  },
  cyan: {
    defaultBorder: 'border-cyan-300',
    selectedBorder: 'border-cyan-500 shadow-lg ring-2 ring-cyan-200',
    bgVar: '--node-cyan-bg',
  },
  violet: {
    defaultBorder: 'border-violet-300',
    selectedBorder: 'border-violet-500 shadow-lg ring-2 ring-violet-200',
    bgVar: '--node-violet-bg',
  },
  slate: {
    defaultBorder: 'border-slate-400',
    selectedBorder: 'border-slate-500 shadow-lg ring-2 ring-slate-200',
    bgVar: '--node-slate-bg',
  },
  neutral: {
    defaultBorder: 'border-border',
    selectedBorder: 'border-gray-500 shadow-lg ring-2 ring-gray-200',
    bgVar: null, // Uses bg-background class
  },
  primary: {
    defaultBorder: 'border-primary/30',
    selectedBorder: 'border-primary shadow-lg ring-2 ring-primary/20',
    bgVar: '--node-primary-bg',
  },
} as const

export type NodeTheme = keyof typeof THEME_COLORS

// Execution status styling - overrides theme colors when status is set
// Running and failed are prominent; completed is subtle to reduce visual noise
const STATUS_STYLES = {
  running: {
    border: 'border-sky-500 border-[3px]',
    bgVar: '--node-status-running-bg',
    animate: 'animate-pulse-border',
    ring: 'ring-2 ring-sky-300',
  },
  completed: {
    // Subtle completed state - don't compete with active nodes
    border: 'border-slate-300',
    bgVar: null, // Use default background, just slightly muted
    animate: '',
    ring: '',
  },
  failed: {
    border: 'border-red-600 border-[3px]',
    bgVar: '--node-status-failed-bg',
    animate: '',
    ring: 'ring-2 ring-red-300',
  },
  pending: null, // Use theme defaults
} as const

interface NodeStatusWrapperProps {
  children: ReactNode
  /** Execution status for visual feedback */
  status?: NodeExecutionStatus
  /** Whether the node is selected in the editor */
  selected?: boolean
  /** Color theme for the node (default styling when no status) */
  theme?: NodeTheme
  /** Minimum width in pixels (default: 200) */
  minWidth?: number
  /** Maximum width in pixels (default: 300) */
  maxWidth?: number
  /** Additional CSS classes */
  className?: string
}

export function NodeStatusWrapper({
  children,
  status,
  selected = false,
  theme = 'neutral',
  minWidth = 200,
  maxWidth = 300,
  className = '',
}: NodeStatusWrapperProps) {
  const themeColors = THEME_COLORS[theme]
  const statusStyle = status && status !== 'pending' ? STATUS_STYLES[status] : null

  // Determine final classes: status > selected > default
  let borderClass: string
  let bgVar: string | null
  let animateClass = ''
  let ringClass = ''

  if (statusStyle) {
    // Status overrides everything - use thick border + ring for visibility
    borderClass = statusStyle.border
    // Use status background if defined, otherwise fall back to theme background
    bgVar = statusStyle.bgVar || themeColors.bgVar
    animateClass = statusStyle.animate
    ringClass = statusStyle.ring
  } else if (selected) {
    // Selected state
    borderClass = themeColors.selectedBorder
    bgVar = themeColors.bgVar
  } else {
    // Default theme - thin border (border-2 is base)
    borderClass = themeColors.defaultBorder
    bgVar = themeColors.bgVar
  }

  // Build style object - use CSS variable for background if available
  const style: React.CSSProperties = {
    minWidth: `${minWidth}px`,
    maxWidth: `${maxWidth}px`,
  }
  
  // Set background via CSS variable if available
  if (bgVar) {
    style.backgroundColor = `var(${bgVar})`
  }

  // Use bg-background for neutral theme when no status
  const bgClass = theme === 'neutral' && !statusStyle 
      ? 'bg-background' 
      : ''

  return (
    <div
      className={`px-4 py-3 rounded-lg border-2 transition-all ${borderClass} ${animateClass} ${ringClass} ${bgClass} ${className}`}
      style={style}
    >
      {children}
    </div>
  )
}

/**
 * Get handle colors for a theme
 * Used to style the ReactFlow handles consistently with the node theme
 */
export function getHandleColors(theme: NodeTheme): {
  borderColor: string
  connectedBg: string
  disconnectedBg: string
} {
  const colorMap: Record<NodeTheme, { border: string; bg: string }> = {
    green: { border: '!border-green-600', bg: '!bg-green-600' },
    indigo: { border: '!border-indigo-600', bg: '!bg-indigo-600' },
    blue: { border: '!border-blue-600', bg: '!bg-blue-600' },
    teal: { border: '!border-teal-600', bg: '!bg-teal-600' },
    amber: { border: '!border-amber-600', bg: '!bg-amber-600' },
    pink: { border: '!border-pink-600', bg: '!bg-pink-600' },
    rose: { border: '!border-rose-600', bg: '!bg-rose-600' },
    fuchsia: { border: '!border-fuchsia-600', bg: '!bg-fuchsia-600' },
    purple: { border: '!border-purple-600', bg: '!bg-purple-600' },
    orange: { border: '!border-orange-600', bg: '!bg-orange-600' },
    emerald: { border: '!border-emerald-600', bg: '!bg-emerald-600' },
    red: { border: '!border-red-600', bg: '!bg-red-600' },
    yellow: { border: '!border-yellow-600', bg: '!bg-yellow-600' },
    sky: { border: '!border-sky-600', bg: '!bg-sky-600' },
    cyan: { border: '!border-cyan-600', bg: '!bg-cyan-600' },
    violet: { border: '!border-violet-600', bg: '!bg-violet-600' },
    slate: { border: '!border-slate-600', bg: '!bg-slate-600' },
    neutral: { border: '!border-gray-600', bg: '!bg-gray-600' },
    primary: { border: '!border-primary', bg: '!bg-primary' },
  }

  const colors = colorMap[theme]
  return {
    borderColor: colors.border,
    connectedBg: colors.bg,
    disconnectedBg: colors.bg,
  }
}

/**
 * Build handle className for ReactFlow handles
 * 
 * @param theme - Color theme for the node
 * @param isConnected - Whether the handle has connections
 * @param executionStatus - Optional execution status (running/completed/failed) - when set, handles are black
 */
export function buildHandleClassName(
  theme: NodeTheme,
  isConnected: boolean,
  _executionStatus?: NodeExecutionStatus
): string {
  const colors = getHandleColors(theme)
  
  // Default behavior: theme color when connected, white when not
  return `!w-3 !h-3 !border-2 ${colors.borderColor} ${isConnected ? colors.connectedBg : colors.disconnectedBg}`
}

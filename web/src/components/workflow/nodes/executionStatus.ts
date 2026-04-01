/**
 * Shared execution status styling for workflow nodes
 */

import type { NodeExecutionStatus } from '../../../lib/workflow-flow'

/**
 * Get CSS classes for a node based on its execution status
 * Returns classes for border color, background, and optional animation
 * 
 * Running nodes get a pulsing border effect (defined in workflow-theme.css)
 */
export function getExecutionStatusClasses(
  status: NodeExecutionStatus | undefined,
  _baseColor: string = 'gray'
): {
  border: string
  bg: string
  animate: string
} {
  switch (status) {
    case 'running':
      return {
        border: 'border-blue-500 border-[3px]', // Thicker border for running
        bg: 'bg-blue-50', // Light blue background
        animate: 'animate-pulse-border', // Custom pulsing border animation
      }
    case 'completed':
      return {
        border: 'border-emerald-500',
        bg: 'bg-emerald-50',
        animate: '',
      }
    case 'failed':
      return {
        border: 'border-red-500',
        bg: 'bg-red-50',
        animate: '',
      }
    case 'pending':
    default:
      // Return empty strings - let the node use its default colors
      return {
        border: '',
        bg: '',
        animate: '',
      }
  }
}

/**
 * Get a status indicator icon/badge classes
 */
export function getStatusIndicatorClasses(status: NodeExecutionStatus | undefined): string {
  switch (status) {
    case 'running':
      return 'bg-blue-500 animate-pulse'
    case 'completed':
      return 'bg-emerald-500'
    case 'failed':
      return 'bg-red-500'
    case 'pending':
    default:
      return 'bg-gray-300'
  }
}

/**
 * Get border and background classes for a node based on execution status
 * Returns the status classes if available, otherwise returns defaults
 */
export function getNodeStatusStyles(
  status: NodeExecutionStatus | undefined,
  defaultBorder: string,
  selectedBorder: string,
  defaultBg: string,
  selected: boolean = false
): { borderClass: string; bgClass: string; animateClass: string } {
  const statusClasses = getExecutionStatusClasses(status)
  
  // Border: status overrides selection overrides default
  let borderClass = defaultBorder
  if (statusClasses.border) {
    borderClass = statusClasses.border
  } else if (selected) {
    borderClass = selectedBorder
  }
  
  // Background: status overrides default (keep node's default bg unless status specifies one)
  const bgClass = statusClasses.bg || defaultBg
  
  // Animation: always from status (empty if no status)
  const animateClass = statusClasses.animate
  
  return { borderClass, bgClass, animateClass }
}

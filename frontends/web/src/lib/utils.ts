import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

/**
 * Formats error messages to be more human-readable
 */
export function formatErrorMessage(error: string | Error): string {
  const message = typeof error === 'string' ? error : error.message;

  // Handle common API error patterns
  if (message.includes('400 Bad Request')) {
    if (message.includes('all messages must have non-empty content')) {
      return 'The AI response was incomplete. Please try again.';
    }
    if (message.includes('invalid_request_error')) {
      return 'The request was invalid. Please try again.';
    }
    return 'The request could not be processed. Please try again.';
  }

  if (message.includes('401 Unauthorized')) {
    return 'Authentication failed. Please check your API key.';
  }

  if (message.includes('403 Forbidden')) {
    return 'Access denied. You may not have permission for this action.';
  }

  if (message.includes('429 Too Many Requests')) {
    return 'Too many requests. Please wait a moment and try again.';
  }

  if (message.includes('500 Internal Server Error')) {
    return 'Server error occurred. Please try again later.';
  }

  if (message.includes('502 Bad Gateway') || message.includes('503 Service Unavailable')) {
    return 'Service temporarily unavailable. Please try again later.';
  }

  if (message.includes('timeout') || message.includes('timed out')) {
    return 'Request timed out. Please try again.';
  }

  if (message.includes('network') || message.includes('connection')) {
    return 'Network connection issue. Please check your internet connection.';
  }

  if (message.includes('Failed to') || message.includes('failed to')) {
    // Clean up common failure messages
    return message
      .replace(/Failed to /i, 'Unable to ')
      .replace(/failed to /i, 'unable to ')
      .replace(/\.$/, '') + '. Please try again.';
  }

  // For other errors, try to make them more readable
  return message
    .replace(/Error: /i, '')
    .replace(/ERROR: /i, '')
    .replace(/\[.*?\]/g, '') // Remove brackets and content
    .replace(/\{.*?\}/g, '') // Remove JSON objects
    .replace(/\s+/g, ' ') // Normalize whitespace
    .trim();
}

export function toTitleCase(str: string): string {
  return str
    .split('_')
    .map(word => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
    .join(' ')
}
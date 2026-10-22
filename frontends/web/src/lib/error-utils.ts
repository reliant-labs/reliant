import { HTTPError } from './http-error';

export interface APIErrorResponse {
  error: string;
  message: string;
  code: number;
}

/**
 * Parse error response from API
 */
export async function parseAPIError(error: unknown): Promise<string> {
  if (error instanceof HTTPError) {
    try {
      // Try to parse the JSON error response
      const errorData = await error.response.json() as APIErrorResponse;
      // Return the message field if it exists
      if (errorData.message) {
        return errorData.message;
      }
      // Fallback to error field
      if (errorData.error) {
        return errorData.error;
      }
    } catch {
      // If JSON parsing fails, try to get text
      try {
        const text = await error.response.text();
        if (text) {
          return text;
        }
      } catch {
        // Ignore text parsing errors
      }
    }
    // Fallback to status text
    return `Request failed: ${error.response.statusText || error.response.status}`;
  }

  if (error instanceof Error) {
    return error.message;
  }

  if (typeof error === 'string') {
    return error;
  }

  return 'An unexpected error occurred';
}

/**
 * Format error for display in UI
 */
export function formatErrorForDisplay(error: string): string {
  // Remove technical details like URLs and status codes if they're redundant
  const cleanedError = error
    .replace(/Request failed with status code \d+.*?: .*/, '')
    .replace(/^Error: /, '')
    .replace(/failed to create pull request:\s*/i, '')
    .replace(/gh pr create failed:\s*/i, '')
    .trim();

  // Check for GitHub CLI authentication issues with helpful instructions
  if (cleanedError.includes('gh auth login') || cleanedError.includes('GH_TOKEN')) {
    // Preserve the helpful auth instructions from gh CLI
    return cleanedError
      .replace(/To get started with GitHub CLI, please run:/i, 'GitHub CLI is not authenticated. Please run:')
      .replace(/Alternatively,/, 'Or')
      .trim();
  }

  // Ensure the error message is user-friendly
  if (cleanedError.includes('GitHub CLI (gh) not found') || cleanedError.includes('GitHub CLI (gh) is not installed')) {
    return 'GitHub CLI is not installed. Please install it from https://cli.github.com/';
  }

  if (cleanedError.includes('not a GitHub repository') || cleanedError.includes('only supported for GitHub')) {
    return 'This repository is not hosted on GitHub. Pull requests are only supported for GitHub repositories.';
  }

  if (cleanedError.includes('authentication') || cleanedError.includes('401')) {
    return 'Authentication failed. Please check your GitHub credentials.';
  }

  if (cleanedError.includes('permission') || cleanedError.includes('403')) {
    return 'You do not have permission to perform this action.';
  }

  if (cleanedError.includes('network') || cleanedError.includes('ECONNREFUSED')) {
    return 'Network error. Please check your internet connection.';
  }

  // For other gh CLI errors, try to clean them up but preserve the message
  if (cleanedError.includes('gh ')) {
    return cleanedError;
  }

  return cleanedError || 'An unexpected error occurred';
}
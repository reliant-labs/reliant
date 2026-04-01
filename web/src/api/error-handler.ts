import { HTTPError } from '../lib/http-error';
import { toast } from '../lib/toast-manager';
import { logger } from '../lib/logger';
import * as Sentry from '@sentry/react';

interface ErrorContext {
  url?: string;
  method?: string;
  body?: unknown;
  operation?: string;
}

export class APIErrorHandler {
  private static instance: APIErrorHandler;
  private suppressedErrors = new Set<string>();
  private errorCount = new Map<string, number>();
  private readonly MAX_ERROR_COUNT = 3;
  private readonly ERROR_RESET_TIME = 60000; // 1 minute
  private resetIntervalId: ReturnType<typeof setInterval> | null = null;

  private constructor() {
    // Reset error counts periodically
    this.resetIntervalId = setInterval(() => {
      this.errorCount.clear();
    }, this.ERROR_RESET_TIME);
  }

  dispose(): void {
    if (this.resetIntervalId !== null) {
      clearInterval(this.resetIntervalId);
      this.resetIntervalId = null;
    }
  }

  static getInstance(): APIErrorHandler {
    if (!APIErrorHandler.instance) {
      APIErrorHandler.instance = new APIErrorHandler();
    }
    return APIErrorHandler.instance;
  }

  // Ky hook for handling errors
  async handleError(error: HTTPError, context?: ErrorContext): Promise<void> {
    const errorKey = this.getErrorKey(error, context);

    // Check if error should be suppressed
    if (this.suppressedErrors.has(errorKey)) {
      return;
    }

    // Track error frequency
    const count = (this.errorCount.get(errorKey) || 0) + 1;
    this.errorCount.set(errorKey, count);

    // Suppress repeated errors
    if (count > this.MAX_ERROR_COUNT) {
      this.suppressedErrors.add(errorKey);
      setTimeout(() => {
        this.suppressedErrors.delete(errorKey);
      }, this.ERROR_RESET_TIME);
      return;
    }

    // Log error details
    logger.error('API Error:', {
      url: context?.url || error.request.url,
      method: context?.method || error.request.method,
      status: error.response.status,
      statusText: error.response.statusText,
      body: context?.body,
    });

    // Report server errors (5xx) to Sentry
    const status = error.response.status;
    if (status >= 500) {
      Sentry.captureException(error, {
        tags: {
          http_status: String(status),
          http_method: context?.method || error.request.method,
          operation: context?.operation,
        },
        extra: {
          url: context?.url || error.request.url,
          statusText: error.response.statusText,
        },
      });
    }

    // Handle specific error types
    await this.handleSpecificError(error, context);
  }

  private async handleSpecificError(error: HTTPError, context?: ErrorContext): Promise<void> {
    const status = error.response.status;
    const operation = context?.operation || 'Operation';

    switch (status) {
      case 400: {
        // Bad request - show specific error message
        const badRequestMsg = await this.extractErrorMessage(error);
        toast.error(badRequestMsg || `${operation} failed: Invalid request`, { duration: 8000 });
        break;
      }

      case 401:
        // Unauthorized - handled by the apiClient auth hook
        // Only show toast if not already redirecting
        if (!window.location.pathname.includes('/login')) {
          toast.error('Session expired. Please log in again.', { duration: 8000 });
        }
        break;

      case 403:
        // Forbidden
        toast.error(`${operation} failed: You don't have permission to perform this action`, { duration: 8000 });
        break;

      case 404:
        // Not found - only show for important operations
        if (context?.operation && !context.operation.includes('get')) {
          toast.error(`${operation} failed: Resource not found`, { duration: 8000 });
        }
        break;

      case 409: {
        // Conflict
        const conflictMsg = await this.extractErrorMessage(error);
        toast.warning(conflictMsg || `${operation} failed: Resource conflict`, { duration: 6000 });
        break;
      }

      case 422: {
        // Validation error
        const validationMsg = await this.extractErrorMessage(error);
        toast.error(validationMsg || `${operation} failed: Validation error`, { duration: 8000 });
        break;
      }

      case 429:
        // Rate limiting
        toast.warning('Too many requests. Please slow down.', { duration: 6000 });
        break;

      case 500:
      case 502:
      case 503:
        // Server errors
        toast.error(`Server error. Please try again later.`, {
          duration: 10000,
        });
        break;

      case 504:
        // Gateway timeout
        toast.error(`${operation} timed out. Please try again.`, { duration: 8000 });
        break;

      default: {
        // Generic error
        const genericMsg = await this.extractErrorMessage(error);
        toast.error(genericMsg || `${operation} failed`, { duration: 8000 });
      }
    }
  }

  private async extractErrorMessage(error: HTTPError): Promise<string | null> {
    try {
      const contentType = error.response.headers.get('content-type');

      if (contentType?.includes('application/json')) {
        const errorData = await error.response.clone().json();
        return errorData.message || errorData.error || null;
      }

      if (contentType?.includes('text/')) {
        return await error.response.clone().text();
      }
    } catch {
      // Failed to parse error response
    }

    return null;
  }

  private getErrorKey(error: HTTPError, context?: ErrorContext): string {
    const url = context?.url || error.request.url;
    const method = context?.method || error.request.method;
    const status = error.response.status;
    return `${method}:${url}:${status}`;
  }

  // Method to suppress specific errors
  suppressError(pattern: string): void {
    this.suppressedErrors.add(pattern);
  }

  // Method to clear suppressed errors
  clearSuppressedErrors(): void {
    this.suppressedErrors.clear();
  }
}

// Create and export a singleton instance
export const apiErrorHandler = APIErrorHandler.getInstance();

/**
 * Handle an HTTP error from a fetch response.
 * Call this after checking response.ok === false.
 */
export async function handleFetchError(
  response: Response,
  request: Request
): Promise<HTTPError> {
  const error = new HTTPError(response, request);
  const url = request.url;
  const method = request.method;
  const operation = extractOperationName(url, method);

  await apiErrorHandler.handleError(error, {
    url,
    method,
    operation,
  });

  return error;
}

function extractOperationName(url: string, method: string): string {
  // Extract meaningful operation name from URL
  const urlParts = url.split('/').filter(Boolean);
  const lastPart = urlParts[urlParts.length - 1];

  // Map HTTP methods to operations
  const methodMap: Record<string, string> = {
    GET: 'Fetch',
    POST: 'Create',
    PUT: 'Update',
    PATCH: 'Update',
    DELETE: 'Delete',
  };

  const action = methodMap[method] || method;

  // Handle specific endpoints
  if (url.includes('/workflows')) {
    if (method === 'POST') return 'Create workflow';
    if (method === 'PUT') return 'Update workflow';
    if (method === 'DELETE') return 'Delete workflow';
    return 'Workflow operation';
  }

  if (url.includes('/projects')) {
    if (lastPart === 'projects') return `${action} project`;
    if (url.includes('/worktrees')) return `${action} worktree`;
    if (url.includes('/chats')) return `${action} chat`;
    if (url.includes('/messages')) return `${action} message`;
    if (url.includes('/sessions')) return `${action} session`;
    if (url.includes('/approvals')) return `${action} approval`;
  }

  if (url.includes('/auth')) return 'Authentication';
  if (url.includes('/health')) return 'Health check';

  return action;
}
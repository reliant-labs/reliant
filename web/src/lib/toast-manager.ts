import { toast as sonnerToast } from 'sonner';
import { parseAPIError } from './error-utils';
import { HTTPError } from './http-error';

export interface ToastOptions {
  duration?: number;
  dismissible?: boolean;
  position?:
    | 'top-left'
    | 'top-center'
    | 'top-right'
    | 'bottom-left'
    | 'bottom-center'
    | 'bottom-right';
}

const defaultOptions: ToastOptions = {
  duration: 4000,
  dismissible: true,
  position: 'top-center',
};

class ToastManager {
  private static instance: ToastManager;

  private constructor() {}

  static getInstance(): ToastManager {
    if (!ToastManager.instance) {
      ToastManager.instance = new ToastManager();
    }
    return ToastManager.instance;
  }

  success(message: string, options?: ToastOptions) {
    sonnerToast.success(message, {
      duration: options?.duration || defaultOptions.duration,
      dismissible: true,
    });
  }

  async error(error: unknown, options?: ToastOptions) {
    const message = await this.formatError(error);
    const description = this.getErrorDescription(error);

    sonnerToast.error(message, {
      duration: options?.duration || 6000,
      dismissible: true,
      description,
      action: {
        label: 'Copy',
        onClick: () => {
          const fullMessage = description ? `${message}\n${description}` : message;
          navigator.clipboard.writeText(fullMessage);
          sonnerToast.success('Copied to clipboard', { duration: 2000 });
        },
      },
    });
  }

  warning(message: string, options?: ToastOptions) {
    sonnerToast.warning(message, {
      duration: options?.duration || 5000,
      dismissible: true,
    });
  }

  info(message: string, options?: ToastOptions) {
    sonnerToast.info(message, {
      duration: options?.duration || defaultOptions.duration,
      dismissible: true,
    });
  }

  loading(message: string, _options?: ToastOptions) {
    return sonnerToast.loading(message, {
      dismissible: true,
    });
  }

  promise<T>(
    promise: Promise<T>,
    {
      loading,
      success,
      error,
    }: {
      loading: string;
      success: string | ((data: T) => string);
      error: string | ((error: unknown) => string);
    }
  ) {
    return sonnerToast.promise(promise, {
      loading,
      success,
      error: async (err: unknown) => {
        const errorMessage = typeof error === 'function' ? error(err) : error;
        const formatted = await this.formatError(err);
        return `${errorMessage}: ${formatted}`;
      },
    });
  }

  dismiss(toastId?: string | number) {
    if (toastId) {
      sonnerToast.dismiss(toastId);
    } else {
      sonnerToast.dismiss();
    }
  }

  // Generic clickable/info toast
  notify(
    message: string,
    options?: ToastOptions & {
      description?: string;
      action?: { label: string; onClick: () => void };
    }
  ) {
    sonnerToast(message, {
      duration: options?.duration || defaultOptions.duration,
      dismissible: true,
      description: options?.description,
      action: options?.action,
    });
  }

  private async formatError(error: unknown): Promise<string> {
    if (error instanceof HTTPError) {
      return await parseAPIError(error);
    }

    if (error instanceof Error) {
      // Check for network errors
      if (error.message.includes('Failed to fetch') || error.message.includes('NetworkError')) {
        return 'Network error: Unable to connect to the server';
      }

      // Check for timeout errors
      if (error.message.includes('timeout') || error.message.includes('Timeout')) {
        return 'Request timed out. Please try again.';
      }

      return error.message;
    }

    if (typeof error === 'string') {
      return error;
    }

    return 'An unexpected error occurred';
  }

  private getErrorDescription(error: unknown): string | undefined {
    if (error instanceof HTTPError) {
      const status = error.response.status;
      const statusText = error.response.statusText;

      switch (status) {
        case 400:
          return 'The request was invalid. Please check your input.';
        case 401:
          return 'You are not authenticated. Please log in.';
        case 403:
          return 'You do not have permission to perform this action.';
        case 404:
          return 'The requested resource was not found.';
        case 429:
          return 'Too many requests. Please try again later.';
        case 500:
          return 'Server error. Please try again later.';
        case 502:
          return 'Bad gateway. The server is temporarily unavailable.';
        case 503:
          return 'Service unavailable. Please try again later.';
        default:
          return `HTTP ${status}: ${statusText}`;
      }
    }

    return undefined;
  }
}

export const toast = ToastManager.getInstance();

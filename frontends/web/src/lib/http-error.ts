// Copyright (c) 2025 Reliant Labs

/**
 * Custom HTTP error class to replace ky's HTTPError.
 * Provides similar functionality for error handling without the ky dependency.
 */
export class HTTPError extends Error {
  readonly response: Response;
  readonly request: Request;

  constructor(response: Response, request: Request, message?: string) {
    const defaultMessage = `Request failed with status ${response.status}: ${response.statusText}`;
    super(message || defaultMessage);
    this.name = "HTTPError";
    this.response = response;
    this.request = request;
  }

  /**
   * Check if an error is an HTTPError
   */
  static isHTTPError(error: unknown): error is HTTPError {
    return error instanceof HTTPError;
  }
}

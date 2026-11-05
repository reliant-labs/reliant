/** Where the SDK writes diagnostics. Silent by default. */
export interface Logger {
  debug(message: string, meta?: Record<string, unknown>): void;
  info(message: string, meta?: Record<string, unknown>): void;
  warn(message: string, meta?: Record<string, unknown>): void;
  error(message: string, meta?: Record<string, unknown>): void;
}

const noop = (): void => {};

export const noopLogger: Logger = { debug: noop, info: noop, warn: noop, error: noop };

/** A logger that starts every message with `prefix`, for filtering one component's lines. */
export function withPrefix(base: Logger, prefix: string): Logger {
  return {
    debug: (message, meta) => base.debug(`${prefix} ${message}`, meta),
    info: (message, meta) => base.info(`${prefix} ${message}`, meta),
    warn: (message, meta) => base.warn(`${prefix} ${message}`, meta),
    error: (message, meta) => base.error(`${prefix} ${message}`, meta),
  };
}

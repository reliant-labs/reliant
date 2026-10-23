"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// A React error boundary with a designed fallback. Next.js file-convention
// boundaries (app/error.tsx, app/global-error.tsx) catch route render
// crashes; this component boundary lets you ALSO isolate a subtree — a
// dashboard widget, a <Resource> table — so one crashing panel degrades to a
// small fallback instead of taking the whole route (or the app) white.
import {
  Component,
  type ErrorInfo,
  type ReactElement,
  type ReactNode,
} from "react";

import { reportException } from "./error-reporter.js";
import { userMessage } from "./errors.js";

interface FallbackProps {
  error: Error;
  reset: () => void;
}

interface Props {
  children: ReactNode;
  /** Custom fallback UI. Defaults to DefaultErrorFallback. */
  fallback?: (props: FallbackProps) => ReactNode;
  /** Extra reporter hook, called after the error is recorded with the browser telemetry SDK. */
  onError?: (error: Error, info: ErrorInfo) => void;
  /** When any value in this array changes, the boundary resets. */
  resetKeys?: unknown[];
  /** Compact single-line fallback (for small subtrees). */
  compact?: boolean;
}

interface State {
  error: Error | null;
}

export class RuntimeErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Always log, and record with the browser telemetry SDK (a no-op when
    // none is running); the app can additionally forward to a sink via onError.
    console.error("[runtime] error boundary caught:", error, info);
    reportException(error, {
      "react.component_stack": info.componentStack ?? "",
      "error.handled_by": "RuntimeErrorBoundary",
    });
    this.props.onError?.(error, info);
  }

  componentDidUpdate(prev: Props): void {
    if (
      this.state.error &&
      !shallowEqual(prev.resetKeys, this.props.resetKeys)
    ) {
      this.reset();
    }
  }

  reset = (): void => {
    this.setState({ error: null });
  };

  render(): ReactNode {
    const { error } = this.state;
    if (!error) {
      return this.props.children;
    }
    if (this.props.fallback) {
      return this.props.fallback({ error, reset: this.reset });
    }
    return (
      <DefaultErrorFallback
        error={error}
        reset={this.reset}
        compact={this.props.compact}
      />
    );
  }
}

function shallowEqual(a?: unknown[], b?: unknown[]): boolean {
  if (a === b) {
    return true;
  }
  if (!a || !b || a.length !== b.length) {
    return false;
  }
  return a.every((v, i) => Object.is(v, b[i]));
}

// The `: ReactElement` here and on every other exported component is
// load-bearing, not decoration — see README, "One .d.ts for React 18 and 19".
// Inferring it makes the published declaration name a type whose SPELLING
// differs between the two @types/react majors this package supports.
export function DefaultErrorFallback({
  error,
  reset,
  compact,
}: FallbackProps & { compact?: boolean }): ReactElement {
  if (compact) {
    return (
      <div
        role="alert"
        className="flex items-center justify-between gap-3 rounded-md border border-danger-border bg-danger-surface px-3 py-2 text-sm text-danger-ink"
      >
        <span className="truncate">Couldn&apos;t load this section.</span>
        <button
          type="button"
          onClick={reset}
          className="shrink-0 font-medium text-danger underline hover:text-danger-hover"
        >
          Retry
        </button>
      </div>
    );
  }
  return (
    <div
      role="alert"
      className="flex min-h-[12rem] flex-col items-center justify-center gap-3 rounded-lg border border-danger-border bg-danger-surface p-8 text-center"
    >
      <h2 className="text-lg font-semibold text-danger-ink">
        Something went wrong
      </h2>
      <p className="max-w-md text-sm text-danger-ink">
        This section ran into an unexpected error. You can try again — the rest
        of the app is still working.
      </p>
      {/* userMessage, never error.message — the same display rule as
          <Resource>'s error rung. */}
      <p className="max-w-md text-sm text-danger-ink">{userMessage(error)}</p>
      <button
        type="button"
        onClick={reset}
        className="mt-1 rounded-md bg-danger px-4 py-2 text-sm font-medium text-on-danger hover:bg-danger-hover"
      >
        Try again
      </button>
    </div>
  );
}

"use client";

import { reportException } from "@reliantlabs/forge-web-runtime";
import { useEffect } from "react";

export default function Error({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    // Record with the browser telemetry SDK (src/lib/otel_gen.ts). When it is
    // not running — telemetry off, or still loading — fall back to the
    // console, so the error is never silent. The SDK already records an
    // error React logged to console.error, and reportException knows that, so
    // this never counts one failure twice.
    if (!reportException(error, { "error.digest": error.digest ?? "" })) {
      console.error(error);
    }
  }, [error]);

  // A section, not a second main landmark: the app shell already renders the
  // page's one main, and a nested one breaks landmark navigation.
  return (
    <section className="flex flex-1 flex-col items-center justify-center p-8">
      <div className="max-w-md text-center">
        <h1 className="mb-4 text-xl font-semibold tracking-tight">Something went wrong</h1>
        <p className="mb-6 text-ink-muted">
          An unexpected error occurred. You can try again, or return later.
        </p>
        {error.digest ? (
          <p className="mb-6 font-mono text-xs text-ink-muted">Error ID: {error.digest}</p>
        ) : null}
        <button
          type="button"
          onClick={reset}
          className="rounded-md bg-accent px-4 py-2 text-sm font-medium text-on-accent hover:bg-accent-hover"
        >
          Try again
        </button>
      </div>
    </section>
  );
}

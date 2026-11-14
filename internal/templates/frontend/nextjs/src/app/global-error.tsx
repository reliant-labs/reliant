"use client";

import { reportException } from "@reliantlabs/forge-web-runtime";
import { useEffect } from "react";

export default function GlobalError({
  error,
  reset,
}: {
  error: Error & { digest?: string };
  reset: () => void;
}) {
  useEffect(() => {
    // global-error replaces the root layout, so the telemetry SDK started by
    // providers.tsx may never have mounted. reportException is a no-op then,
    // and the console fallback keeps the error visible.
    if (!reportException(error, { "error.digest": error.digest ?? "" })) {
      console.error(error);
    }
  }, [error]);

  return (
    <html lang="en">
      <body className="bg-surface font-sans text-ink antialiased">
        <main className="flex flex-1 flex-col items-center justify-center p-8">
          <div className="max-w-md text-center">
            <h1 className="mb-4 text-3xl font-bold tracking-tight">Application error</h1>
            <p className="mb-6 text-ink-muted">
              A critical error occurred and the app could not recover.
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
        </main>
      </body>
    </html>
  );
}

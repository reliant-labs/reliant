"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// RuntimeShell is the provider set the app shell mounts INSIDE its existing
// providers (QueryClient + event bus + auth). It adds, in one place:
//
//   - SessionProvider     — the signed-in user derived from the JWT
//   - RuntimeErrorBoundary — a designed crash fallback (not a white screen)
//   - RuntimeToastHost     — the toast queue behind "toast:show" events
//                            (mutation errors, success toasts) that were
//                            previously emitted into the void
//
// Everything app-shaped arrives as a PROP: the auth state, the event
// subscription, the toast markup. The runtime never reaches into src/lib or
// src/components, so renaming or deleting those files can never break it.
//
// Keep the shell thin: compose here, don't grow app logic into it.
import { RuntimeErrorBoundary } from "./error-boundary.js";
import { SessionProvider } from "./session.js";
import { RuntimeToastHost } from "./toast-host.js";

import type { RuntimeToastHostProps } from "./toast-host.js";
import type { SessionAuth } from "./session.js";
import type { ErrorInfo, ReactElement, ReactNode } from "react";

export interface RuntimeShellProps {
  children: ReactNode;
  /**
   * Current auth state — pass your own `useAuth()` value. The runtime derives
   * the session from it rather than importing your auth module.
   */
  auth: SessionAuth;
  /**
   * Toast wiring: how to subscribe to your event bus and how to render your
   * toast component. Omit it and no toast host is mounted.
   */
  toast?: RuntimeToastHostProps;
  /** Forwarded to the top-level error boundary (wire to telemetry). */
  onError?: (error: Error, info: ErrorInfo) => void;
}

export function RuntimeShell({
  children,
  auth,
  toast,
  onError,
}: RuntimeShellProps): ReactElement {
  return (
    <SessionProvider auth={auth}>
      <RuntimeErrorBoundary onError={onError}>{children}</RuntimeErrorBoundary>
      {toast ? (
        <RuntimeToastHost subscribe={toast.subscribe} render={toast.render} />
      ) : null}
    </SessionProvider>
  );
}

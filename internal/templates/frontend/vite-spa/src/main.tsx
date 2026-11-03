import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { installDevLogging } from "@reliantlabs/forge-web-runtime";

import "./index.css";
import { initTelemetry } from "@/lib/otel_gen";

import { Providers } from "./providers";

// The entry point mounts the app and defines no component of its own: the
// provider stack is ./providers.tsx, so React Fast Refresh can hot-swap it.

// Start browser telemetry (the HyperDX SDK: errors, console, fetch/XHR spans,
// web vitals, to the same-origin /_otel route). A no-op while telemetry is off
// (see src/lib/otel_gen.ts), idempotent, and never throws. Delete this call
// and src/lib/otel_gen.ts to opt out.
initTelemetry();

// Mirror console output and uncaught errors to the dev server, which writes
// them to .forge/logs/<env>/frontend_<name>.log — so a browser-side failure is
// visible on disk instead of only in devtools. Delete this block and the
// devLogPlugin() entry in vite.config.ts to opt out.
//
// The `if` is what keeps this out of a production bundle, and it has to be a
// statement-level guard rather than an argument. `import.meta.env.DEV` folds
// to `false` at build time; wrapping the CALL lets the minifier drop the whole
// statement (and then tree-shake the import), whereas passing it as an
// argument only folded the argument — leaving a live `installDevLogging({})`
// in the production bundle. Verified against a real `vite build`.
if (import.meta.env.DEV) {
  installDevLogging({ dev: true });
}

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("Root element #root not found");
}

createRoot(rootEl).render(
  <StrictMode>
    <Providers />
  </StrictMode>,
);

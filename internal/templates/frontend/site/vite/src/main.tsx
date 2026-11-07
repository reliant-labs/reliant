import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { installDevLogging } from "@reliantlabs/forge-web-runtime";

import { App } from "./App";
import "./index.css";

// Mirror console output and uncaught errors to the dev server, which writes
// them to .forge/logs/<env>/frontend_<name>.log — so a browser-side failure is
// visible on disk instead of only in devtools. Delete this block and the
// devLogPlugin() entry in vite.config.ts to opt out.
//
// The statement-level `if` is what keeps this out of a production bundle:
// `import.meta.env.DEV` folds to `false` at build time, so the minifier drops
// the call and tree-shakes the import.
if (import.meta.env.DEV) {
  installDevLogging({ dev: true });
}

const rootEl = document.getElementById("root");
if (!rootEl) {
  throw new Error("Root element #root not found");
}

createRoot(rootEl).render(
  <StrictMode>
    <App />
  </StrictMode>,
);

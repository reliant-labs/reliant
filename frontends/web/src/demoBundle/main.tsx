// The forge demo's own entry (demo.html): what demo.reliantlabs.io serves, a
// separate build of this package (`npm run build:demo`, vite.config.ts's demo
// target). It mounts the demo shell's route tree and nothing else: no Reliant
// app, chat, onboarding, sign-in, daemon or settings code is reachable from
// here, and vite-plugin-demo-bundle.ts fails the build if any arrives.

// FIRST and SECOND for the same reasons as in main.tsx: console capture before
// anything can log, and the runtime config (config.js) checked before any
// module reads it.
import "../lib/browser-log-boot";
import "../lib/configGuard";

import { createRoot } from "react-dom/client";
import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "../index.css";

import { DemoRoot } from "./DemoRoot";

createRoot(document.getElementById("root")!).render(<DemoRoot />);

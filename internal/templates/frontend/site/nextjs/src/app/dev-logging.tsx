"use client";

import { useEffect } from "react";

import { installDevLogging } from "@reliantlabs/forge-web-runtime";
import { initTelemetry } from "../lib/otel_gen";

// Mirror console output and uncaught errors to the dev server, which writes
// them to .forge/logs/<env>/frontend_<name>.log — so a browser-side failure is
// visible on disk instead of only in devtools. Remove installDevLogging()
// and src/app/%5F_forge/log/route.ts to opt out of dev log mirroring. The
// telemetry initializer also runs here, client-side only, in every env.
//
// Guarded on the literal: the bundler folds process.env.NODE_ENV at build
// time, so the statement-level `if` drops the call from a production bundle.
export function DevLogging() {
  useEffect(() => {
    initTelemetry();
    if (process.env.NODE_ENV !== "production") {
      installDevLogging({ dev: true });
    }
  }, []);
  return null;
}

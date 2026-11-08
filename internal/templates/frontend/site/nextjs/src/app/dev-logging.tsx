"use client";

import { useEffect } from "react";

import { installDevLogging } from "@reliantlabs/forge-web-runtime";

// Mirror console output and uncaught errors to the dev server, which writes
// them to .forge/logs/<env>/frontend_<name>.log — so a browser-side failure is
// visible on disk instead of only in devtools. Delete this file, its use in
// layout.tsx and src/app/%5F_forge/log/route.ts (which serves /__forge/log) to
// opt out.
//
// Guarded on the literal: the bundler folds process.env.NODE_ENV at build
// time, so the statement-level `if` drops the call from a production bundle.
export function DevLogging() {
  useEffect(() => {
    if (process.env.NODE_ENV !== "production") {
      installDevLogging({ dev: true });
    }
  }, []);
  return null;
}

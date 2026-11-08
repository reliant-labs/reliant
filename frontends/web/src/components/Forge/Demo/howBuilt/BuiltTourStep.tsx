// Copyright (c) 2025 Reliant Labs

/**
 * The tour's "How this app was built" step (journeys/forge.tsx, fg-built):
 * the recorded forge run as the step's own view. The tour draws it as a wide
 * modal card (no spotlight, the page behind it scroll-locked), so the player
 * cannot be scrolled out from under the step. Playback starts only on request,
 * and it states the run's totals; the full
 * breakdown is the page's `?section=built`.
 *
 * Lazy-loaded by the journey, so the fixtures stay out of the app's main chunk.
 */

import { useMemo } from "react";

import { scaffoldFixtures } from "./fixtures";
import { summarize } from "./ScaffoldPanel";
import { TerminalReplay } from "./TerminalReplay";
import { BuildSummary } from "./BuildSummary";

const n = (value: number) => value.toLocaleString("en-US");

export function BuiltTourStep() {
  const { cast, manifest } = scaffoldFixtures();
  const s = useMemo(() => summarize(manifest, cast), [manifest, cast]);
  return (
    <div className="space-y-3" data-testid="built-tour-step">
      <BuildSummary />
      <TerminalReplay cast={cast} autoPlay={false} />
      <p data-testid="built-tour-totals">
        {n(s.youLines)} lines of API definitions. Forge generated {n(s.forgeLines)} lines across {n(s.files)} files: database setup, server and browser code, tests, dashboards, alerts and deployment settings. Every file is counted under <strong>How this app was built</strong>.
      </p>
      <p>The recording shows code generation, code checks and a Go build. The console uses sample data; it is not a deployment from this recording.</p>
      <p>On your running app, you and your coding agent can inspect the same health information. Forge also writes an <code>AGENTS.md</code> file with instructions for your agent.</p>
    </div>
  );
}

export default BuiltTourStep;

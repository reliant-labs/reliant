// Copyright (c) 2025 Reliant Labs

/**
 * The tour's "How this app was built" step (journeys/forge.tsx, fg-built):
 * the recorded forge run as the step's own view. The tour draws it as a wide
 * modal card (no spotlight, the page behind it scroll-locked), so the player
 * cannot be scrolled out from under the step. It plays by itself when the
 * card opens (TerminalReplay), and states the run's totals; the full
 * breakdown is the page's `?section=built`.
 *
 * Lazy-loaded by the journey, so the fixtures stay out of the app's main chunk.
 */

import { useMemo } from "react";

import { scaffoldFixtures } from "./fixtures";
import { summarize } from "./ScaffoldPanel";
import { TerminalReplay } from "./TerminalReplay";

const n = (value: number) => value.toLocaleString("en-US");

export function BuiltTourStep() {
  const { cast, manifest } = scaffoldFixtures();
  const s = useMemo(() => summarize(manifest, cast), [manifest, cast]);
  return (
    <div className="space-y-3" data-testid="built-tour-step">
      <p>
        One more thing: the traces you just walked are sample data, but this app&apos;s code is real. This is the run that made it. One forge
        command made the project, a person added an entity and an rpc to each service&apos;s proto, and <code>forge scaffold</code> did the
        rest.
      </p>
      <TerminalReplay cast={cast} />
      <p data-testid="built-tour-totals">
        You write {n(s.youLines)} lines of proto. forge writes {n(s.forgeLines)} lines across {n(s.files)} files: migrations, ORM, handlers,
        React hooks, tests, dashboards, alerts and deploy config, with OpenTelemetry already wired in. Every file is counted under{" "}
        <strong>How this app was built</strong>, in the banner.
      </p>
      <p>
        Your coding agent works from the same project: forge wrote it an <code>AGENTS.md</code> that points it at forge&apos;s skills, and{" "}
        <code>forge obs</code> gives it this console&apos;s telemetry in a terminal.
      </p>
    </div>
  );
}

export default BuiltTourStep;

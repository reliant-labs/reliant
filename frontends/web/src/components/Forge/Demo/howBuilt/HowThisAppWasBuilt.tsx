// Copyright (c) 2025 Reliant Labs

/**
 * "How this app was built": the recorded forge run that made shop's code (a
 * terminal replay) and what it wrote (the panel), from one pair of fixtures.
 *
 * The demo console shows it at `/forge/demo?section=built` in every shell
 * (DemoConsole), and the demo host's tour opens it (journeys/forge.tsx,
 * fg-built). It is also standalone: no router, query client or console
 * around it, so a page elsewhere can render it as is, or pass its own
 * cast and manifest (see howBuilt/index.ts).
 */

import { ArrowLeft } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";

import { scaffoldFixtures, type ScaffoldCast, type ScaffoldManifest } from "./fixtures";
import { ScaffoldPanel } from "./ScaffoldPanel";
import { TerminalReplay } from "./TerminalReplay";

export interface HowThisAppWasBuiltProps {
  /** Default: the recorded run in data/. */
  cast?: ScaffoldCast;
  manifest?: ScaffoldManifest;
  /** Shows a "Back to the console" button that calls it (the demo console). */
  onBack?: () => void;
  className?: string;
  /** Contextual help never starts playback without a request. */
  autoPlay?: boolean;
  /** Public entry puts the real build before its provenance paragraph. */
  leadWithRecording?: boolean;
}

export function HowThisAppWasBuilt({ cast, manifest, onBack, className, autoPlay, leadWithRecording = false }: HowThisAppWasBuiltProps) {
  const fixtures = cast && manifest ? { cast, manifest } : scaffoldFixtures();
  const recorded = new Date(fixtures.manifest.captured_at).toLocaleDateString("en-US", { year: "numeric", month: "long", day: "numeric" });
  return (
    <div className={cn("space-y-6", className)} data-testid="how-built">
      {leadWithRecording && <TerminalReplay cast={fixtures.cast} autoPlay={false} />}
      <div className="flex flex-wrap items-start gap-3">
        <div className="min-w-0 flex-1 space-y-1">
          <p className="max-w-3xl text-sm text-ink-muted">
            The traces and errors in this console are sample data. This part is not: it is the real run that made shop&apos;s code. One{" "}
            <code>forge</code> command made the project, a person added one entity and one rpc to each service&apos;s proto, and{" "}
            <code>forge scaffold</code> did the rest.
            Then <code>forge lint</code> and <code>go build</code> checked it. Recorded with forge {fixtures.manifest.forge_version} on {recorded}.
          </p>
        </div>
        {onBack && (
          <Button size="sm" variant="outline" leftIcon={<ArrowLeft />} onClick={onBack} data-testid="how-built-back">
            Back to the console
          </Button>
        )}
      </div>
      {!leadWithRecording && <TerminalReplay cast={fixtures.cast} autoPlay={autoPlay} />}
      <ScaffoldPanel manifest={fixtures.manifest} cast={fixtures.cast} />
    </div>
  );
}

export default HowThisAppWasBuilt;

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

import { shortForgeVersion } from "./forgeVersion";
import { scaffoldFixtures, type ScaffoldCast, type ScaffoldManifest } from "./fixtures";
import { ScaffoldPanel } from "./ScaffoldPanel";
import { TerminalReplay } from "./TerminalReplay";
import { BuildSummary } from "./BuildSummary";

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
    <div className={cn("space-y-10", className)} data-testid="how-built">
      <section className="space-y-5 rounded-xl border border-border bg-surface p-4 sm:p-6" aria-label="The recorded build">
        {/* The public entry's Home (sections/DemoHome) already says what Forge does: no second pitch. */}
        {(!leadWithRecording || onBack) && (
          <div className="flex flex-wrap items-start justify-between gap-4">
            {!leadWithRecording && <BuildSummary />}
            {onBack && <Button className="min-h-[44px]" variant="outline" leftIcon={<ArrowLeft />} onClick={onBack} data-testid="how-built-back">Back to the console</Button>}
          </div>
        )}
        <TerminalReplay cast={fixtures.cast} autoPlay={leadWithRecording ? false : autoPlay} />
        <details className="border-t border-border pt-3 text-sm text-ink-muted" data-testid="how-built-about">
          <summary className="min-h-[44px] cursor-pointer py-3 font-medium text-ink">About this recording</summary>
          <div className="space-y-2">
            <p>API definitions were added, then Forge generated the supporting code. The recording shows code generation, code checks and a successful Go build, not a deployment or a test-suite run; the console&apos;s monitoring data is a separate sample.</p>
            <p>
              Recorded with <span title={`forge ${fixtures.manifest.forge_version}`}>Forge {shortForgeVersion(fixtures.manifest.forge_version)}</span> on {recorded}. The commands and code are unchanged from that run; long waits play shorter.
            </p>
          </div>
        </details>
      </section>
      <ScaffoldPanel manifest={fixtures.manifest} cast={fixtures.cast} />
    </div>
  );
}

export default HowThisAppWasBuilt;

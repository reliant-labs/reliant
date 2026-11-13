// Copyright (c) 2026 Reliant Labs

/**
 * "How releases work", folded at the top of Releases (the "75" of the demo
 * density scale): one line when closed, and when open the path one build
 * takes and why it is safer, in four short points. Grounded in what forge
 * does (docs: /forge/deploy/releases-and-promotion):
 *
 *   - a release is built once and promoted by digest: the bytes that passed
 *     staging are the bytes prod runs;
 *   - `forge obs check` compares the window after a promotion with an equal
 *     window before it;
 *   - going back is an ordinary deploy of an older recorded release, no
 *     rebuild; a release that changed data in a new shape rolls forward;
 *   - every promotion is a row in an append-only ledger.
 */

import { useState } from "react";
import { Check, ChevronDown, ExternalLink, History, Package, RotateCcw, ShieldCheck } from "lucide-react";

import { cn } from "@/lib/utils";

const DOCS_RELEASES = "https://docs.reliantlabs.io/forge/deploy/releases-and-promotion";

const STAGES = [
  { name: "dev", about: "Your team tries it" },
  { name: "staging", about: "A full copy, no real customers" },
  { name: "prod", about: "What customers use" },
];

const POINTS = [
  {
    icon: Package,
    title: "Built once, promoted as-is",
    body: "One build per release. staging and prod run the same image digest, so what passed staging is byte for byte what prod runs.",
  },
  {
    icon: ShieldCheck,
    title: "Checked after every promotion",
    body: "forge obs check compares the window after a promotion with an equal window before it: error rate, latency, crashes. A regression stops the release there.",
  },
  {
    icon: RotateCcw,
    title: "Rollback is a deploy of a known build",
    body: "Going back promotes the last good release, the exact build that ran before, not a rebuild. A release that already changed data's shape rolls forward instead, and the plan says so.",
  },
  {
    icon: History,
    title: "One ledger",
    body: "Every promotion is recorded: who, which release, which environment, and how its check came out. Nothing is overwritten.",
  },
];

export function HowReleasesWork() {
  const [open, setOpen] = useState(false);
  return (
    <section className="rounded-lg border border-border bg-surface" data-testid="how-releases-work" aria-label="How releases work">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="how-releases-work-body"
        onClick={() => setOpen(!open)}
        className="group flex w-full items-center gap-3 rounded-lg px-4 py-3 text-left hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
        data-testid="how-releases-work-toggle"
      >
        <span className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary-ink">
          <Package className="h-3.5 w-3.5" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 text-sm">
          <span className="font-medium text-ink">How releases work</span>
          <span className="text-ink-muted"> · built once, checked after every promotion, one step back to a known-good build</span>
        </span>
        <ChevronDown className={cn("h-4 w-4 shrink-0 text-ink-muted transition-transform", open && "rotate-180")} aria-hidden="true" />
      </button>
      {open && (
        <div id="how-releases-work-body" className="space-y-5 border-t border-border px-4 pb-5 pt-4">
          <ol className="flex flex-col gap-2 md:flex-row md:items-stretch" aria-label="The path of one release">
            {STAGES.map((stage, index) => (
              <li key={stage.name} className="contents">
                {index > 0 && (
                  <span className="flex items-center justify-center gap-1.5 text-xs font-medium text-success-ink md:flex-col md:px-1" aria-label="checked">
                    <span className="inline-flex h-6 w-6 items-center justify-center rounded-full border border-success-border bg-success-surface">
                      <Check className="h-3.5 w-3.5" aria-hidden="true" />
                    </span>
                    checked
                  </span>
                )}
                <div className="flex-1 rounded-lg border border-border bg-surface-sunken px-3 py-2">
                  <p className="font-mono text-sm font-medium text-ink">{stage.name}</p>
                  <p className="text-xs text-ink-muted">{stage.about}</p>
                </div>
              </li>
            ))}
          </ol>
          <ul className="grid gap-4 sm:grid-cols-2">
            {POINTS.map(({ icon: Icon, title, body }) => (
              <li key={title} className="flex gap-3">
                <Icon className="mt-0.5 h-4 w-4 shrink-0 text-primary-ink" aria-hidden="true" />
                <span className="space-y-0.5">
                  <span className="block text-sm font-medium text-ink">{title}</span>
                  <span className="block text-sm leading-relaxed text-ink-muted">{body}</span>
                </span>
              </li>
            ))}
          </ul>
          <p className="text-sm text-ink-muted">
            In this sample shop, v1.8.3 failed one of its checks in prod: checkouts started failing on payment-provider timeouts. The fix, v1.8.4, is in dev,
            and takes the same path.{" "}
            <a href={DOCS_RELEASES} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 font-medium text-primary-ink underline-offset-2 hover:underline">
              Releases and promotion
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          </p>
        </div>
      )}
    </section>
  );
}

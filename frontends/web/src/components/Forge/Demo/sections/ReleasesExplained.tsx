// Copyright (c) 2026 Reliant Labs

/**
 * "How releases work, and why it's safer", in everyday words. Grounded in
 * what forge does (docs: /forge/deploy/releases-and-promotion and the
 * observability agents page):
 *
 *   - a release is built once and promoted by digest: the bytes that passed
 *     staging are the bytes prod runs;
 *   - `forge obs check` compares the window after a promotion with an equal
 *     window before it (the "hour before");
 *   - going back is an ordinary deploy of an older, recorded release (its
 *     plan says BEHIND): the exact build, no rebuild. A release that changed
 *     data in a new shape is recovered by rolling forward, and we say so;
 *   - every promotion is a row in an append-only ledger: who, what, when.
 */

import type { ReactNode } from "react";
import { ArrowRight, Check, History, Package, RotateCcw, ShieldCheck, X } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS } from "./demoLinks";
import { Panel } from "./Panel";
import { ago, INCIDENT_CHECK, runningNow } from "./shopFacts";

const POINTS = [
  {
    icon: Package,
    title: "Built once, shipped as-is",
    body: "Forge builds your app one time. The copy your customers get is the exact copy you tested, down to the last byte. Nothing is rebuilt along the way, so “it worked in testing” means it works.",
  },
  {
    icon: ShieldCheck,
    title: "Checked automatically at every step",
    body: "After each move, Forge compares the next stretch of real traffic with the hour before: errors, speed, crashes. If anything got worse, it says what, in numbers, before you move on.",
  },
  {
    icon: RotateCcw,
    title: "One step back to a known-good build",
    body: "Every release is kept. Going back means pointing customers at the last good one, the exact build that ran before, not a rushed rebuild. If a release already changed your data, Forge tells you, and you ship a fix forward instead.",
  },
  {
    icon: History,
    title: "A full history",
    body: "Who shipped what, when, where it went, and how its check came out, for every environment. Nothing is overwritten, so “what changed?” always has an answer.",
  },
];

const COMPARE: { topic: string; usual: string; forge: string }[] = [
  { topic: "Building", usual: "Rebuilt separately for testing and for customers, so what you tested isn't quite what ships.", forge: "Built once. Testing and customers run the very same copy." },
  { topic: "Checking", usual: "Someone watches charts after a release, if they remember to.", forge: "Every release is compared with the hour before, automatically." },
  { topic: "When it goes wrong", usual: "A scramble to rebuild the old version, often late at night.", forge: "Point customers back at the last good build. Minutes, not hours." },
  { topic: "Fridays", usual: "“Nobody ships on Friday.”", forge: "Ship when it's ready. A bad release is caught and reversed." },
  { topic: "Records", usual: "Spread across chat threads and people's memories.", forge: "One history: who, what, when, and every check's result." },
];

export function ReleasesExplained() {
  const stops = runningNow();
  const prod = stops.find((s) => s.env === "env-prod")!;
  const dev = stops.find((s) => s.env === "env-dev")!;
  return (
    <div className="mx-auto w-full max-w-5xl space-y-16 pb-20 pt-4 sm:pt-8" data-testid="releases-explained">
      <header className="space-y-5">
        <h1 className="max-w-3xl text-4xl font-semibold leading-tight tracking-tight text-ink sm:text-5xl">How releases work, and why it's safer</h1>
        <p className="max-w-2xl text-xl leading-relaxed text-ink-muted">
          A release is one exact copy of your app. Forge builds it once, then moves that same copy from development, to testing, to your customers, and checks it at every step.
        </p>
      </header>

      <PathDiagram />

      <section className="grid gap-5 sm:grid-cols-2" aria-label="Why it's safer">
        {POINTS.map(({ icon: Icon, title, body }) => (
          <Panel key={title} className="space-y-3 p-6">
            <span className="inline-flex h-11 w-11 items-center justify-center rounded-xl bg-primary/10 text-primary-ink">
              <Icon className="h-5 w-5" aria-hidden="true" />
            </span>
            <h2 className="text-xl font-semibold text-ink">{title}</h2>
            <p className="text-base leading-relaxed text-ink-muted">{body}</p>
          </Panel>
        ))}
      </section>

      <section className="space-y-5" aria-labelledby="compare-title">
        <h2 id="compare-title" className="text-3xl font-semibold tracking-tight text-ink">
          The common way, and the Forge way
        </h2>
        <div className="overflow-hidden rounded-2xl border border-border">
          <div className="grid grid-cols-1 border-b border-border bg-surface-sunken text-base font-semibold text-ink sm:grid-cols-[10rem_1fr_1fr]">
            <span className="hidden px-5 py-3 sm:block" />
            <span className="hidden px-5 py-3 sm:block">The common way</span>
            <span className="hidden px-5 py-3 sm:block">With Forge</span>
          </div>
          {COMPARE.map((row) => (
            <div key={row.topic} className="grid grid-cols-1 gap-2 border-b border-border bg-surface px-5 py-4 last:border-b-0 sm:grid-cols-[10rem_1fr_1fr] sm:gap-0 sm:px-0 sm:py-0">
              <span className="text-base font-semibold text-ink sm:px-5 sm:py-4">{row.topic}</span>
              <span className="flex gap-2 text-base leading-relaxed text-ink-muted sm:px-5 sm:py-4">
                <X className="mt-1 h-4 w-4 shrink-0 text-ink-subtle" aria-label="The common way:" />
                {row.usual}
              </span>
              <span className="flex gap-2 text-base leading-relaxed text-ink sm:px-5 sm:py-4">
                <Check className="mt-1 h-4 w-4 shrink-0 text-success-ink" aria-label="With Forge:" />
                {row.forge}
              </span>
            </div>
          ))}
        </div>
      </section>

      <section className="space-y-5" aria-labelledby="today-title">
        <h2 id="today-title" className="text-3xl font-semibold tracking-tight text-ink">
          What happened in the sample shop today
        </h2>
        <Panel className="space-y-4 p-6 text-lg leading-relaxed text-ink">
          <p>
            Release {prod.version} passed development and testing. Testing talks to the payment company's practice system, which never runs slow, so nothing looked wrong.
          </p>
          <p>
            It reached customers {ago(prod.ageMs)}. Within minutes the automatic check compared it with the hour before and failed it: failed checkouts went from {INCIDENT_CHECK.errorsBefore} to {INCIDENT_CHECK.errorsAfter}.
          </p>
          <p>
            Maya's fix, {dev.version}, is already in development. It will take the same path, and be checked the same way, before any customer sees it.
          </p>
        </Panel>
        <div className="flex flex-wrap gap-3">
          <SeeIt href={DEMO_LINKS.badRelease} primary>
            See {prod.version} and its check
          </SeeIt>
          <SeeIt href={DEMO_LINKS.releases}>See every release</SeeIt>
          <SeeIt href={DEMO_LINKS.releasesDashboard}>Releases dashboard</SeeIt>
        </div>
      </section>

      <PageAgentStrip id="release-guard" lead="Put an agent on every release: it waits for real traffic, checks, and moves customers back to the last good build if it's bad." />
    </div>
  );
}

function SeeIt({ href, children, primary }: { href: string; children: ReactNode; primary?: boolean }) {
  return (
    <Link
      href={href}
      className={cn(
        "inline-flex min-h-11 items-center gap-2 rounded-lg px-4 text-base font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        primary ? "bg-primary font-semibold text-primary-foreground hover:bg-primary/90" : "border border-border bg-surface text-ink hover:bg-ink/5",
      )}
    >
      {children}
      <ArrowRight className="h-4 w-4" aria-hidden="true" />
    </Link>
  );
}

/** The one build, three stages, a checkmark gate between each. */
function PathDiagram() {
  const stages = [
    { name: "Development", about: "Your team tries it out" },
    { name: "Testing", about: "A full copy of the app, no real customers" },
    { name: "Customers", about: "Production: the app people use" },
  ];
  return (
    <figure className="rounded-3xl border border-border bg-surface p-6 sm:p-8" data-testid="release-path">
      <div className="mb-6 flex items-center gap-3">
        <span className="inline-flex h-11 w-11 items-center justify-center rounded-xl bg-primary/10 text-primary-ink">
          <Package className="h-5 w-5" aria-hidden="true" />
        </span>
        <div>
          <p className="text-lg font-semibold text-ink">One build: v1.8.2</p>
          <p className="text-base text-ink-muted">The same copy, moved from stage to stage</p>
        </div>
      </div>
      <ol className="flex flex-col items-stretch gap-3 lg:flex-row lg:items-center">
        {stages.map((stage, index) => (
          <li key={stage.name} className="contents">
            {index > 0 && (
              <span className="flex items-center justify-center gap-2 text-sm font-medium text-success-ink lg:flex-col lg:px-1" aria-label="checked automatically">
                <span className="inline-flex h-8 w-8 items-center justify-center rounded-full border border-success-border bg-success-surface">
                  <Check className="h-4 w-4" aria-hidden="true" />
                </span>
                <span>Checked</span>
              </span>
            )}
            <div className="flex-1 rounded-2xl border border-border bg-surface-sunken p-4">
              <p className="text-sm font-medium text-ink-muted">Stage {index + 1}</p>
              <p className="text-lg font-semibold text-ink">{stage.name}</p>
              <p className="text-base text-ink-muted">{stage.about}</p>
            </div>
          </li>
        ))}
      </ol>
      <figcaption className="mt-5 text-base text-ink-muted">Between each stage, Forge compares the new release with the hour before it. A failed check stops the release where it is.</figcaption>
    </figure>
  );
}

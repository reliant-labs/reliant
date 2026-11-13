// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's home, for buyers who are not engineers: one short hero in
 * everyday words, then four small before/after stories, each with one visual
 * from the sample shop and one way to see it for real, then "Put an agent
 * on it".
 *
 * The first story is our own (we spent months on slow chat loading; a trace
 * handed to an agent found it in an afternoon). The others are told as "teams
 * like yours" and use the sample shop's own story (shopStory.ts): the v1.8.3
 * payment retry, its failed check, maya's fix.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight, Check, X } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { trackEvent } from "@/lib/analytics";
import { cn } from "@/lib/utils";

import { SignUpButton } from "../DemoCta";
import { demoSectionHref, DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_RELEASES } from "../demoPath";
import { AgentCardGrid, ChatWithAgentCard } from "./AgentCards";
import { DEMO_LINKS } from "./demoLinks";
import { INCIDENT_CHECK, peopleThisWeek } from "./simpleData";
import { count } from "./ui";

export function DemoHome() {
  return (
    <div className="mx-auto w-full max-w-5xl space-y-20 pb-20 pt-4 sm:space-y-28 sm:pt-10" data-testid="demo-home">
      <Hero />
      <div className="space-y-20 sm:space-y-28">
        <Story
          id="slow"
          eyebrow="Slow pages"
          title="Three months of guessing. One afternoon with the agent."
          before="We spent three months trying to make our chat pages load faster. New caches, bigger servers, rewritten screens. Nothing moved much."
          after="The day we handed the agent a trace, a step-by-step timeline of one slow page load, it pointed at a single database query and fixed it the same afternoon."
          see={{ href: DEMO_LINKS.traces, label: "See slow requests in the sample shop" }}
          visual={<SlowVisual />}
        />
        <Story
          id="checkout"
          eyebrow="Things that break"
          title="Checkout broke at 2am. The fix was waiting by breakfast."
          before="Usually an alert wakes someone up, who spends an hour half-asleep working out what changed."
          after="Here the alert wakes an agent instead. It reads the failed orders, sees that the newest release retries slow payments past checkout's time limit, and opens a fix. In the morning, you review one change."
          see={{ href: DEMO_LINKS.incidentIssue, label: "See the broken checkout" }}
          visual={<CheckoutVisual />}
          flip
        />
        <Story
          id="release"
          eyebrow="Releases"
          title="“Did yesterday's release break anything?” You'll know in minutes."
          before="Someone ships on a Friday, then everyone watches charts and hopes. Or nobody ships on Fridays at all."
          after={`Every release is compared, automatically, with the hour before it went out. When v1.8.3 reached the shop's customers, the check flagged it within minutes: failed checkouts went from ${INCIDENT_CHECK.errorsBefore} to ${INCIDENT_CHECK.errorsAfter}.`}
          see={{ href: DEMO_LINKS.badRelease, label: "See that release's check" }}
          more={{ href: demoSectionHref(DEMO_SECTION_RELEASES), label: "How releases work, and why it's safer" }}
          visual={<ReleaseVisual />}
        />
        <Story
          id="people"
          eyebrow="People"
          title="“Who actually uses this?”"
          before="Most teams bolt on a separate analytics tool, then argue about whose numbers are right."
          after="Forge counts people from the same app it runs: who is new, who came back, and where they found you. No extra tool, and the numbers match the rest of the app."
          see={{ href: DEMO_LINKS.people, label: "See who uses the sample shop" }}
          visual={<PeopleVisual />}
          flip
        />
      </div>

      <section id="agents" className="scroll-mt-24 space-y-6" aria-labelledby="home-agents-title">
        <div className="max-w-2xl space-y-2">
          <h2 id="home-agents-title" className="text-3xl font-semibold tracking-tight text-ink">
            Put an agent on it
          </h2>
          <p className="text-lg leading-relaxed text-ink-muted">Create an agentic workflow that…</p>
        </div>
        <AgentCardGrid placement="landing" />
        <ChatWithAgentCard placement="landing" />
      </section>

      <section className="rounded-3xl border border-border bg-surface px-6 py-10 text-center sm:px-12 sm:py-14">
        <h2 className="text-3xl font-semibold tracking-tight text-ink">Try it on your own app</h2>
        <p className="mx-auto mt-3 max-w-xl text-lg leading-relaxed text-ink-muted">Everything here works the same way for an app you build with Forge. Free to start.</p>
        <div className="mt-6 flex flex-wrap items-center justify-center gap-4">
          <SignUpButton placement="landing" />
          <Link href={demoSectionHref(DEMO_SECTION_BUILT)} className="inline-flex min-h-11 items-center gap-1.5 rounded-lg px-3 text-base font-medium text-primary-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            See how this shop was built
            <ArrowRight className="h-4 w-4" aria-hidden="true" />
          </Link>
        </div>
      </section>
    </div>
  );
}

function Hero() {
  return (
    <section className="space-y-6" aria-labelledby="home-title">
      <p className="text-base font-medium text-brand-violet">A demo you can click around in</p>
      <h1 id="home-title" className="max-w-4xl text-4xl font-semibold leading-[1.1] tracking-tight text-ink sm:text-6xl">
        Know when your app breaks, and let an AI agent fix it.
      </h1>
      <p className="max-w-2xl text-xl leading-relaxed text-ink-muted">
        Forge builds your app, ships it safely and keeps an eye on it. When something goes wrong you see it in plain words, and an agent can find the cause and fix it.
      </p>
      <div className="flex flex-wrap items-center gap-4 pt-2">
        <Link
          href={DEMO_LINKS.happening}
          onClick={() => trackEvent("demo_welcome_choice", { choice: "happening" })}
          className="inline-flex min-h-12 items-center gap-2 rounded-xl bg-primary px-6 text-lg font-semibold text-primary-foreground shadow-sm hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          data-testid="home-primary"
        >
          See what's happening in the shop
          <ArrowRight className="h-5 w-5" aria-hidden="true" />
        </Link>
        <Link
          href={demoSectionHref(DEMO_SECTION_AGENTS)}
          className="inline-flex min-h-12 items-center rounded-xl border border-border bg-surface px-5 text-lg font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          Put an agent on it
        </Link>
      </div>
      <p className="max-w-2xl text-base text-ink-muted">
        This is a sample online shop with made-up customers and real-looking data. Look at anything; nothing you click changes it.
      </p>
    </section>
  );
}

function Story({
  id,
  eyebrow,
  title,
  before,
  after,
  see,
  more,
  visual,
  flip,
}: {
  id: string;
  eyebrow: string;
  title: string;
  before: string;
  after: string;
  see: { href: string; label: string };
  more?: { href: string; label: string };
  visual: ReactNode;
  flip?: boolean;
}) {
  return (
    <section className="grid items-center gap-8 lg:grid-cols-2 lg:gap-14" aria-labelledby={`story-${id}`} data-testid={`home-story-${id}`}>
      <div className={cn("space-y-5", flip && "lg:order-2")}>
        <p className="text-base font-medium text-ink-muted">{eyebrow}</p>
        <h2 id={`story-${id}`} className="text-3xl font-semibold leading-tight tracking-tight text-ink">
          {title}
        </h2>
        <div className="space-y-4 text-lg leading-relaxed">
          <p className="text-ink-muted">
            <span className="font-semibold text-ink">Before. </span>
            {before}
          </p>
          <p className="text-ink">
            <span className="font-semibold">After. </span>
            {after}
          </p>
        </div>
        <div className="flex flex-wrap gap-3 pt-1">
          <Link
            href={see.href}
            onClick={() => trackEvent("demo_story_opened", { story: id })}
            className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:border-primary/50 hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {see.label}
            <ArrowRight className="h-4 w-4" aria-hidden="true" />
          </Link>
          {more && (
            <Link href={more.href} className="inline-flex min-h-11 items-center gap-1.5 rounded-lg px-3 text-base font-medium text-primary-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              {more.label}
            </Link>
          )}
        </div>
      </div>
      <div className={cn(flip && "lg:order-1")}>{visual}</div>
    </section>
  );
}

function VisualFrame({ caption, children }: { caption: string; children: ReactNode }) {
  return (
    <figure className="rounded-3xl border border-border bg-surface p-5 shadow-sm sm:p-7">
      {children}
      <figcaption className="mt-5 text-sm text-ink-muted">{caption}</figcaption>
    </figure>
  );
}

/** One request, step by step: the slow step stands out. */
function SlowVisual() {
  const steps = [
    { label: "Check who is signed in", ms: 12, at: 0 },
    { label: "Find this customer's orders", ms: 1720, at: 12, slow: true },
    { label: "Build the page", ms: 60, at: 1732 },
  ];
  const total = 1792;
  return (
    <VisualFrame caption="From the sample shop: one visit to “Your orders”, step by step.">
      <div className="flex items-baseline justify-between">
        <p className="text-lg font-semibold text-ink">Opening “Your orders”</p>
        <p className="text-lg font-semibold tabular-nums text-ink">1.8s</p>
      </div>
      <div className="mt-5 space-y-4">
        {steps.map((step) => (
          <div key={step.label} className="space-y-1.5">
            <div className="flex items-baseline justify-between gap-3 text-base">
              <span className={step.slow ? "font-semibold text-ink" : "text-ink-muted"}>{step.label}</span>
              <span className={cn("tabular-nums", step.slow ? "font-semibold text-warning-ink" : "text-ink-muted")}>{step.ms >= 1000 ? `${(step.ms / 1000).toFixed(1)}s` : `${step.ms}ms`}</span>
            </div>
            <div className="relative h-4 rounded-full bg-surface-sunken">
              <div className={cn("absolute inset-y-0 rounded-full", step.slow ? "bg-warning" : "bg-primary/50")} style={{ left: `${(step.at / total) * 100}%`, width: `${Math.max(1.5, (step.ms / total) * 100)}%` }} />
            </div>
          </div>
        ))}
      </div>
      <p className="mt-5 rounded-xl border border-warning-border bg-warning-surface px-4 py-3 text-base text-warning-ink">The agent: “One query reads every order, then filters. An index makes it about 20 times faster.”</p>
    </VisualFrame>
  );
}

/** The night, as four moments. */
function CheckoutVisual() {
  const moments = [
    { at: "2:07 am", text: "Alert: orders are failing", tone: "bad" },
    { at: "2:09 am", text: "Agent finds the cause: the new payment retry in v1.8.3 runs past checkout's 3-second limit", tone: "neutral" },
    { at: "2:31 am", text: "Fix ready for review: give the payment call its own time limit", tone: "neutral" },
    { at: "8:40 am", text: "You read it, approve it, and it ships through testing first", tone: "good" },
  ] as const;
  return (
    <VisualFrame caption="The sample shop's broken checkout, as it would play out overnight.">
      <ol className="space-y-5">
        {moments.map((moment, index) => (
          <li key={moment.at} className="flex gap-4">
            <div className="flex flex-col items-center">
              <span
                className={cn(
                  "mt-1 inline-flex h-3.5 w-3.5 shrink-0 rounded-full",
                  moment.tone === "bad" ? "bg-danger" : moment.tone === "good" ? "bg-success" : "bg-primary/70",
                )}
                aria-hidden="true"
              />
              {index < moments.length - 1 && <span className="mt-1 w-px flex-1 bg-border" aria-hidden="true" />}
            </div>
            <div className="pb-1">
              <p className="text-sm font-medium tabular-nums text-ink-muted">{moment.at}</p>
              <p className="text-base leading-relaxed text-ink">{moment.text}</p>
            </div>
          </li>
        ))}
      </ol>
    </VisualFrame>
  );
}

/** Three stops, a checkmark gate between each; prod's check fails. */
function ReleaseVisual() {
  const stops = [
    { name: "Development", ok: true },
    { name: "Testing", ok: true },
    { name: "Customers", ok: false },
  ];
  return (
    <VisualFrame caption="Release v1.8.3 in the sample shop: passed two checks, failed the third.">
      <p className="text-lg font-semibold text-ink">v1.8.3: retry slow payments once</p>
      <ol className="mt-5 flex flex-col gap-3 sm:flex-row sm:items-stretch">
        {stops.map((stop) => (
          <li key={stop.name} className={cn("flex flex-1 items-center gap-3 rounded-2xl border p-4 sm:flex-col sm:items-start", stop.ok ? "border-border" : "border-danger-border bg-danger-surface")}>
            <span className={cn("inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-full", stop.ok ? "bg-success-surface text-success-ink" : "border border-danger-border bg-surface text-danger-ink")}>
              {stop.ok ? <Check className="h-5 w-5" aria-hidden="true" /> : <X className="h-5 w-5" aria-hidden="true" />}
            </span>
            <span>
              <span className="block text-base font-semibold text-ink">{stop.name}</span>
              <span className={cn("block text-sm", stop.ok ? "text-ink-muted" : "text-danger-ink")}>{stop.ok ? "Check passed" : "Check failed"}</span>
            </span>
          </li>
        ))}
      </ol>
      <dl className="mt-5 grid grid-cols-2 gap-3 text-base">
        <div className="rounded-xl border border-border bg-surface-sunken p-3">
          <dt className="text-ink-muted">Failed checkouts</dt>
          <dd className="mt-1 font-semibold tabular-nums text-ink">
            {INCIDENT_CHECK.errorsBefore} → <span className="text-danger-ink">{INCIDENT_CHECK.errorsAfter}</span>
          </dd>
        </div>
        <div className="rounded-xl border border-border bg-surface-sunken p-3">
          <dt className="text-ink-muted">Slowest checkouts</dt>
          <dd className="mt-1 font-semibold tabular-nums text-ink">
            {INCIDENT_CHECK.slowBefore} → <span className="text-danger-ink">{INCIDENT_CHECK.slowAfter}</span>
          </dd>
        </div>
      </dl>
    </VisualFrame>
  );
}

/** New vs returning, and the top three ways people arrived. */
function PeopleVisual() {
  const week = useMemo(() => peopleThisWeek(), []);
  const total = week.fresh + week.returning || 1;
  const top = week.sources.slice(0, 3);
  const max = Math.max(...top.map((s) => s.people), 1);
  return (
    <VisualFrame caption="The sample shop's last 7 days, from its People page.">
      <p className="text-4xl font-semibold tabular-nums text-ink">{count(week.active)}</p>
      <p className="text-base text-ink-muted">people used the shop this week</p>
      <div className="mt-5 flex h-4 overflow-hidden rounded-full" aria-hidden="true">
        <div className="bg-primary" style={{ width: `${(week.fresh / total) * 100}%` }} />
        <div className="bg-primary/30" style={{ width: `${(week.returning / total) * 100}%` }} />
      </div>
      <div className="mt-2 flex justify-between text-base">
        <span className="text-ink">
          <span className="font-semibold tabular-nums">{count(week.fresh)}</span> new
        </span>
        <span className="text-ink">
          <span className="font-semibold tabular-nums">{count(week.returning)}</span> came back
        </span>
      </div>
      <p className="mt-6 text-base font-medium text-ink-muted">New people found the shop through</p>
      <div className="mt-3 space-y-3">
        {top.map((source) => (
          <div key={source.channel} className="grid grid-cols-[9rem_1fr_2.5rem] items-center gap-3 text-base">
            <span className="text-ink">{source.channel}</span>
            <span className="h-3 overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
              <span className="block h-full rounded-full bg-primary/70" style={{ width: `${(source.people / max) * 100}%` }} />
            </span>
            <span className="text-right tabular-nums text-ink">{source.people}</span>
          </div>
        ))}
      </div>
    </VisualFrame>
  );
}

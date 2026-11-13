// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's landing at the "62" density: a short hero, the four
 * stories as an editorial two-column list (a label, a short title, one line,
 * a link, and a small real piece of the console beside each: the slowest
 * request's timeline, the failing issue's row, the release's check, a replay
 * frame), the agent workflows as a plain list with their calls to action,
 * the chat, and the way to "How this shop was built".
 *
 * The first story is our own and true (we spent three months on slow chat
 * loading; the day we handed an agent a trace it fixed it). The others are
 * the sample shop's own story (shopStory.ts): the v1.8.3 payment retry, its
 * failed check, maya's fix.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ArrowRight, Play } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useListTraces, useObservabilityOrg, useTrace, type TraceFilters } from "@/hooks/observability-queries";
import { trackEvent } from "@/lib/analytics";

import { FIXTURE_PROJECT } from "../../Observability/__fixtures__/fixtureApi";
import { MiniWaterfall } from "../../Observability/Leads/MiniWaterfall";
import { DeployYoursButton } from "../DemoCta";
import { DEMO_INCIDENT, RUNNING } from "../demoData/shopStory";
import { demoSectionHref, DEMO_SECTION_BUILT, DEMO_SECTION_RELEASES } from "../demoPath";
import { TOUR_START } from "../DemoScreenActions";
import { AgentWorkflowList, ChatWithAgentCard } from "./AgentCards";
import { DEMO_LINKS } from "./demoLinks";
import { INCIDENT_CHECK } from "./shopFacts";

interface Story {
  id: string;
  label: string;
  title: string;
  line: string;
  link: { href: string; label: string };
  more?: { href: string; label: string };
  visual: ReactNode;
}

const STORIES: Story[] = [
  {
    id: "slow",
    label: "Traces",
    title: "Three months of guessing, one afternoon with a trace",
    line: "We spent three months making our chat pages load faster. Handed one trace, an agent found the slow query that day.",
    link: { href: DEMO_LINKS.traces, label: "See the slowest requests" },
    visual: <SlowestRequestPreview />,
  },
  {
    id: "checkout",
    label: "Errors and alerts",
    title: "Checkout broke at 2am; the fix was waiting by breakfast",
    line: "The alert woke an agent, not a person. It traced the failed orders to the new payment retry and opened a fix.",
    link: { href: DEMO_LINKS.incidentIssue, label: "Open the checkout issue" },
    visual: <IssueRow />,
  },
  {
    id: "release",
    label: "Releases",
    title: "Know in minutes if a release broke something",
    line: `Every release is checked against the hour before it. ${DEMO_INCIDENT.release} took failed checkouts from ${INCIDENT_CHECK.errorsBefore} to ${INCIDENT_CHECK.errorsAfter}, and the check said so.`,
    link: { href: DEMO_LINKS.badRelease, label: "See that release's check" },
    more: { href: demoSectionHref(DEMO_SECTION_RELEASES), label: "How releases work" },
    visual: <ReleaseRow />,
  },
  {
    id: "people",
    label: "Product analytics and sessions",
    title: "See who uses it, and watch what they did",
    line: "Who is new, who came back and where they found you. Open any visit and replay it, errors marked.",
    link: { href: DEMO_LINKS.people, label: "Open product analytics" },
    visual: <ReplayFrame />,
  },
];

export function DemoHome() {
  return (
    <div className="mx-auto w-full max-w-6xl space-y-10 pb-12 pt-2" data-testid="demo-home">
      <Hero />

      <section className="space-y-3" aria-labelledby="home-stories">
        <h2 id="home-stories" className="text-sm font-medium text-ink-muted">
          Why it matters
        </h2>
        <ol className="grid grid-cols-[minmax(0,1fr)] gap-x-10 border-t border-border md:grid-cols-2">
          {STORIES.map((story, index) => (
            <StoryItem key={story.id} story={story} number={index + 1} />
          ))}
        </ol>
      </section>

      <section id="agents" className="scroll-mt-24 space-y-3" aria-labelledby="home-agents-title">
        <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
          <h2 id="home-agents-title" className="text-lg font-semibold text-ink">
            Put an agent on it
          </h2>
          <p className="text-sm text-ink-muted">Create an agentic workflow that…</p>
        </div>
        <AgentWorkflowList placement="landing" />
        <ChatWithAgentCard placement="landing" bare />
      </section>

      <section className="flex flex-col items-start justify-between gap-3 border-t border-border pt-6 sm:flex-row sm:items-center">
        <div>
          <h2 className="text-base font-semibold text-ink">Try it on your own app</h2>
          <p className="text-sm text-ink-muted">Everything here works the same for an app you build with Forge. Free to start.</p>
        </div>
        <DeployYoursButton placement="landing" />
      </section>
    </div>
  );
}

function Hero() {
  const navigate = useNavigate();
  return (
    <section className="space-y-3" aria-labelledby="home-title">
      <p className="text-sm font-medium text-brand-violet">Forge demo · a sample shop with made-up data</p>
      <h1 id="home-title" className="max-w-3xl text-2xl font-semibold leading-tight tracking-tight text-ink sm:text-3xl">
        Ship your app safely, see what breaks, and let an agent fix it.
      </h1>
      <p className="max-w-2xl text-base leading-relaxed text-ink-muted">
        Forge builds, releases and watches your app: errors, traces, session replays and releases in one console. Click anything; nothing here changes.
      </p>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 pt-1">
        <Link
          href={DEMO_LINKS.happening}
          onClick={() => trackEvent("demo_welcome_choice", { choice: "overview" })}
          className="inline-flex min-h-10 items-center gap-2 rounded-lg bg-primary px-4 text-sm font-semibold text-primary-foreground shadow-sm hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          data-testid="home-primary"
        >
          Open the console
          <ArrowRight className="h-4 w-4" aria-hidden="true" />
        </Link>
        <button
          type="button"
          onClick={() => {
            trackEvent("demo_welcome_choice", { choice: "tour" });
            void navigate(TOUR_START as never);
          }}
          className="inline-flex min-h-10 items-center rounded-lg border border-border bg-surface px-4 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="home-take-tour"
        >
          Take the tour
        </button>
        <a
          href="#agents"
          className="inline-flex min-h-10 items-center rounded-lg border border-border bg-surface px-4 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          Put an agent on it
        </a>
        <Link
          href={demoSectionHref(DEMO_SECTION_BUILT)}
          className="group inline-flex min-h-10 items-center gap-1 px-1 text-sm font-medium text-primary-ink hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="home-how-built"
        >
          How this shop was built
          <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </Link>
      </div>
    </section>
  );
}

const PROD_TRACES: TraceFilters = { range: "1h", rootSpanName: "", services: [], errorsOnly: false, minDurationMs: 0 };

/** The shop's slowest request in prod, as the first steps of its timeline: the trace story's real piece. */
function SlowestRequestPreview() {
  const org = useObservabilityOrg();
  const scope = useMemo(() => (org.orgId ? { orgId: org.orgId, projectId: FIXTURE_PROJECT, envIds: ["env-prod"], filters: [] } : null), [org.orgId]);
  const traces = useListTraces(scope, PROD_TRACES);
  const slowest = useMemo(() => (traces.data?.pages.flatMap((page) => page.traces) ?? []).reduce<{ traceId: string; durationMs: number } | null>((a, b) => (!a || b.durationMs > a.durationMs ? b : a), null), [traces.data]);
  const detail = useTrace(scope, slowest?.traceId ?? null, "1h");
  const spans = detail.data?.spans ?? [];
  return spans.length ? <MiniWaterfall spans={spans} max={5} /> : <div className="h-24" />;
}

/** One story: number and label, a short title, one line, its link, and a real piece of the console. */
function StoryItem({ story, number }: { story: Story; number: number }) {
  return (
    <li className="group relative grid min-w-0 grid-cols-[minmax(0,1fr)] gap-3 border-b border-border py-5" data-testid={`home-story-${story.id}`}>
      <div className="space-y-1">
        <p className="flex items-baseline gap-2 text-xs font-medium text-ink-muted">
          <span className="tabular-nums text-ink">{String(number).padStart(2, "0")}</span>
          {story.label}
        </p>
        <h3 className="text-base font-semibold leading-snug text-ink">{story.title}</h3>
        <p className="text-sm leading-relaxed text-ink-muted">{story.line}</p>
      </div>
      <div className="rounded-lg border border-border/60 bg-surface-sunken p-3" aria-hidden="true">
        {story.visual}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
        <Link
          href={story.link.href}
          onClick={() => trackEvent("demo_story_opened", { story: story.id })}
          className="inline-flex items-center gap-1 text-sm font-semibold text-primary-ink after:absolute after:inset-0 after:content-[''] focus-visible:outline-none group-focus-within:underline"
        >
          {story.link.label}
          <ArrowRight className="h-3.5 w-3.5 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
        </Link>
        {story.more && (
          // Above the item's stretched link, so it is a link of its own.
          <Link href={story.more.href} className="relative z-10 text-sm font-medium text-ink-muted underline-offset-2 hover:text-ink hover:underline">
            {story.more.label}
          </Link>
        )}
      </div>
    </li>
  );
}

/** The incident's issue, as its row in the error inbox reads. */
function IssueRow() {
  return (
    <div className="space-y-1.5 text-xs">
      <div className="flex items-center gap-2">
        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-danger" />
        <span className="min-w-0 truncate text-sm font-medium text-ink">{DEMO_INCIDENT.error}</span>
        <span className="ml-auto shrink-0 rounded-full border border-danger-border bg-danger-surface px-1.5 text-2xs font-medium leading-4 text-danger-ink">New</span>
      </div>
      <p className="flex flex-wrap gap-x-3 text-ink-muted">
        <span className="font-mono text-ink">{DEMO_INCIDENT.service}</span>
        <span>first seen in {DEMO_INCIDENT.release}</span>
        <span>alert {DEMO_INCIDENT.alert} firing</span>
      </p>
      <p className="flex items-center gap-2 text-ink-muted">
        <span className="rounded border border-border bg-surface px-1.5 font-medium text-ink">Fix ready</span>
        maya&apos;s {RUNNING["env-dev"].version} gives the charge its own deadline
      </p>
    </div>
  );
}

/** The bad release, as its row on Releases reads, with its check. */
function ReleaseRow() {
  const prod = RUNNING["env-prod"];
  return (
    <div className="space-y-1.5 text-xs">
      <div className="flex items-center gap-2">
        <span className="min-w-0 truncate text-sm font-medium text-ink">feat(orders): retry the payment charge once on a timeout</span>
      </div>
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-ink-muted">
        <span className="font-mono text-ink">{prod.version}</span>
        <span>live in prod, staging</span>
        <span>{prod.by}</span>
      </p>
      <p className="flex items-center gap-2">
        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-danger" />
        <span className="text-ink">Health check failed:</span>
        <span className="tabular-nums text-ink-muted">
          failed checkouts {INCIDENT_CHECK.errorsBefore} → <span className="font-semibold text-danger-ink">{INCIDENT_CHECK.errorsAfter}</span>
        </span>
      </p>
    </div>
  );
}

/** One recorded visit's first frame, with its play button and its error. */
function ReplayFrame() {
  return (
    <div className="flex items-center gap-3">
      <div className="relative w-36 shrink-0 overflow-hidden rounded border border-border bg-surface">
        <div className="flex items-center gap-1 border-b border-border px-1.5 py-1">
          <span className="h-1 w-1 rounded-full bg-ink/20" />
          <span className="h-1 w-1 rounded-full bg-ink/20" />
          <span className="h-1 w-1 rounded-full bg-ink/20" />
          <span className="ml-1 truncate font-mono text-3xs leading-3 text-ink-muted">/checkout</span>
        </div>
        <div className="space-y-1 p-2">
          <div className="h-1.5 w-2/3 rounded bg-ink/10" />
          <div className="h-1 w-5/6 rounded bg-ink/[0.07]" />
          <div className="h-3 w-full rounded bg-ink/[0.06]" />
        </div>
        <span className="absolute inset-0 top-3 flex items-center justify-center">
          <span className="flex h-6 w-6 items-center justify-center rounded-full bg-primary text-primary-foreground">
            <Play className="ml-px h-3 w-3" fill="currentColor" />
          </span>
        </span>
      </div>
      <div className="min-w-0 space-y-1 text-xs text-ink-muted">
        <p className="text-sm font-medium text-ink">/checkout</p>
        <p>4 pages · 4m 17s · Madrid</p>
        <p>
          <span className="rounded-full border border-danger-border bg-danger-surface px-1.5 text-2xs font-semibold leading-4 text-danger-ink">1 error</span> at 0:08
        </p>
      </div>
    </div>
  );
}

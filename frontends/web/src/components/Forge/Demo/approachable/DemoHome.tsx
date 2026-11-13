// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's landing at the "50" density: a short hero, the four
 * stories condensed to a card each (two or three lines and a link into the
 * sample shop), the agent workflow cards, and the chat.
 *
 * The first story is our own and true (we spent three months on slow chat
 * loading; the day we handed an agent a trace it fixed it). The others are
 * the sample shop's own story (shopStory.ts): the v1.8.3 payment retry, its
 * failed check, maya's fix.
 */

import { ArrowRight, BarChart3, CircleAlert, Tag as TagIcon, Timer } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { trackEvent } from "@/lib/analytics";

import { SignUpButton } from "../DemoCta";
import { demoSectionHref, DEMO_SECTION_BUILT, DEMO_SECTION_RELEASES } from "../demoPath";
import { AgentCardGrid, ChatWithAgentCard } from "./AgentCards";
import { DEMO_LINKS } from "./demoLinks";
import { INCIDENT_CHECK } from "./simpleData";

const STORIES = [
  {
    id: "slow",
    icon: Timer,
    eyebrow: "Traces",
    title: "Three months of guessing, one afternoon with a trace",
    body: "We spent three months trying to make our chat pages load faster. The day we handed an agent a trace, the step-by-step timeline of one slow load, it found the query and fixed it.",
    link: { href: DEMO_LINKS.traces, label: "See the slowest requests" },
  },
  {
    id: "checkout",
    icon: CircleAlert,
    eyebrow: "Errors and alerts",
    title: "Checkout broke at 2am; the fix was waiting by breakfast",
    body: "The alert woke an agent, not a person. It read the failed orders, traced them to the new payment retry in v1.8.3, and opened a fix for review.",
    link: { href: DEMO_LINKS.incidentIssue, label: "Open the checkout issue" },
  },
  {
    id: "release",
    icon: TagIcon,
    eyebrow: "Releases",
    title: "Know in minutes if a release broke something",
    body: `Every release is built once and checked against the hour before it. When v1.8.3 reached prod, failed checkouts went from ${INCIDENT_CHECK.errorsBefore} to ${INCIDENT_CHECK.errorsAfter} and the check flagged it.`,
    link: { href: DEMO_LINKS.badRelease, label: "See that release's check" },
    more: { href: demoSectionHref(DEMO_SECTION_RELEASES), label: "How releases work" },
  },
  {
    id: "people",
    icon: BarChart3,
    eyebrow: "Product analytics and sessions",
    title: "See who uses it, and watch what they did",
    body: "Who is new, who came back and where they found you, from the same app Forge runs. Open any visit and replay it, errors marked.",
    link: { href: DEMO_LINKS.people, label: "Open product analytics" },
  },
] as const;

export function DemoHome() {
  return (
    <div className="mx-auto w-full max-w-6xl space-y-14 pb-16 pt-2 sm:pt-6" data-testid="demo-home">
      <Hero />

      <section className="space-y-4" aria-labelledby="home-stories">
        <h2 id="home-stories" className="text-xl font-semibold text-ink">
          Why it matters
        </h2>
        <div className="grid gap-4 md:grid-cols-2">
          {STORIES.map((story) => (
            <article key={story.id} className="group relative flex flex-col rounded-2xl border border-border bg-surface p-5 transition-colors hover:border-primary/50 focus-within:ring-2 focus-within:ring-ring" data-testid={`home-story-${story.id}`}>
              <p className="flex items-center gap-2 text-sm font-medium text-ink-muted">
                <story.icon className="h-4 w-4" aria-hidden="true" />
                {story.eyebrow}
              </p>
              <h3 className="mt-2 text-lg font-semibold leading-snug text-ink">{story.title}</h3>
              <p className="mt-2 flex-1 text-base leading-relaxed text-ink-muted">{story.body}</p>
              <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2">
                <Link
                  href={story.link.href}
                  onClick={() => trackEvent("demo_story_opened", { story: story.id })}
                  className="inline-flex items-center gap-1.5 text-sm font-semibold text-primary-ink after:absolute after:inset-0 after:rounded-2xl after:content-[''] focus-visible:outline-none"
                >
                  {story.link.label}
                  <ArrowRight className="h-4 w-4 transition-transform group-hover:translate-x-0.5" aria-hidden="true" />
                </Link>
                {"more" in story && (
                  // Above the card's stretched link, so it is a link of its own.
                  <Link href={story.more.href} className="relative z-10 text-sm font-medium text-ink-muted underline-offset-2 hover:text-ink hover:underline">
                    {story.more.label}
                  </Link>
                )}
              </div>
            </article>
          ))}
        </div>
      </section>

      <section id="agents" className="scroll-mt-24 space-y-4" aria-labelledby="home-agents-title">
        <div className="space-y-1">
          <h2 id="home-agents-title" className="text-xl font-semibold text-ink">
            Put an agent on it
          </h2>
          <p className="text-base text-ink-muted">Create an agentic workflow that…</p>
        </div>
        <AgentCardGrid placement="landing" />
        <ChatWithAgentCard placement="landing" />
      </section>

      <section className="flex flex-col items-start justify-between gap-4 rounded-2xl border border-border bg-surface px-6 py-6 sm:flex-row sm:items-center">
        <div>
          <h2 className="text-lg font-semibold text-ink">Try it on your own app</h2>
          <p className="text-base text-ink-muted">Everything here works the same for an app you build with Forge. Free to start.</p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Link href={demoSectionHref(DEMO_SECTION_BUILT)} className="inline-flex min-h-10 items-center gap-1.5 rounded-lg px-3 text-sm font-medium text-primary-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            How this shop was built
          </Link>
          <SignUpButton placement="landing" />
        </div>
      </section>
    </div>
  );
}

function Hero() {
  return (
    <section className="space-y-4" aria-labelledby="home-title">
      <p className="text-sm font-medium text-brand-violet">Forge demo · a sample shop with made-up data</p>
      <h1 id="home-title" className="max-w-3xl text-3xl font-semibold leading-tight tracking-tight text-ink sm:text-4xl">
        Ship your app safely, see what breaks, and let an agent fix it.
      </h1>
      <p className="max-w-2xl text-lg leading-relaxed text-ink-muted">
        Forge builds, releases and watches your app: errors, traces, session replays and releases in one console. Click anything; nothing here changes.
      </p>
      <div className="flex flex-wrap items-center gap-3 pt-1">
        <Link
          href={DEMO_LINKS.happening}
          onClick={() => trackEvent("demo_welcome_choice", { choice: "overview" })}
          className="inline-flex min-h-11 items-center gap-2 rounded-lg bg-primary px-5 text-base font-semibold text-primary-foreground shadow-sm hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          data-testid="home-primary"
        >
          Open the console
          <ArrowRight className="h-4 w-4" aria-hidden="true" />
        </Link>
        <a
          href="#agents"
          className="inline-flex min-h-11 items-center rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          Put an agent on it
        </a>
      </div>
    </section>
  );
}

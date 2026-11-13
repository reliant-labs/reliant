// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's versions of the console's busiest pages, for readers who
 * are not engineers. Each one leads with ONE plain sentence, shows the few
 * things that matter as big cards that look clickable, keeps IDs and raw
 * names behind "Details", and ends with the way into the detailed page.
 *
 *   What's happening   (Overview)          SimpleHappening
 *   Things that broke  (Errors)            SimpleBroke
 *   Slow requests      (Traces)            SimpleSlow
 *   Releases           (Releases)          SimpleReleases
 *   People             (Product analytics) SimplePeople
 *   Checkout           (one dashboard)     SimpleCheckout
 *
 * Their figures come from simpleData.ts, which reads the shop's one story.
 */

import { useMemo } from "react";
import { ArrowRight, CheckCircle2, CircleAlert, Rocket, Timer, Users } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { DEMO_INCIDENT, demoNow, HOUR, RUNNING } from "../demoData/shopStory";
import { demoSectionHref, DEMO_SECTION_RELEASES } from "../demoPath";
import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS } from "./demoLinks";
import {
  checkoutByHour,
  ENV_SHORT,
  INCIDENT_CHECK,
  overallSpeed,
  peopleThisWeek,
  plainIssue,
  plainSubject,
  releaseRows,
  routeSpeeds,
  runningNow,
  useShopIssues,
} from "./simpleData";
import { ago, CardLink, count, Details, Panel, Section, seconds, ShortList, ShowDetailedView, SimplePage, StatusDot } from "./ui";

const issueHref = (id: string) => `${DEMO_LINKS.errors}?issue=${encodeURIComponent(id)}`;

// ── What's happening ────────────────────────────────────────────────────────

export function SimpleHappening() {
  const { open } = useShopIssues();
  const speed = useMemo(() => overallSpeed(), []);
  const checkout = useMemo(() => routeSpeeds().find((r) => r.op === "checkout.v1.CheckoutService/Checkout")!, []);
  const people = useMemo(() => peopleThisWeek(), []);
  const prod = RUNNING["env-prod"];
  const broken = open?.filter((i) => i.envName === "prod").length ?? 1;
  return (
    <SimplePage
      testId="simple-happening"
      title="What's happening"
      tone={broken ? "bad" : "good"}
      summary={
        broken ? (
          <>
            <strong className="font-semibold">One thing needs attention:</strong> some checkouts have been failing since release {prod.version} reached customers {ago(prod.age)}. Everything else is running normally.
          </>
        ) : (
          <>All good: nothing is broken for customers right now.</>
        )
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <CardLink href={issueHref(DEMO_INCIDENT.issueId)} testId="happening-broke">
          <StatusLine icon={<CircleAlert className="h-5 w-5 text-danger-ink" aria-hidden="true" />} label="Things that broke" />
          <p className="mt-2 text-lg font-semibold text-ink">1 problem, and it's new</p>
          <p className="mt-1 text-base text-ink-muted">Some checkouts fail. Maya is on it.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.traces} testId="happening-slow">
          <StatusLine icon={<Timer className="h-5 w-5 text-warning-ink" aria-hidden="true" />} label="Speed" />
          <p className="mt-2 text-lg font-semibold text-ink">Most pages answer in {seconds(speed.typicalMs)}</p>
          <p className="mt-1 text-base text-ink-muted">Checkout is the exception: 1 in 20 takes {seconds(checkout.slowMs)} today.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.badRelease} testId="happening-release">
          <StatusLine icon={<Rocket className="h-5 w-5 text-ink-muted" aria-hidden="true" />} label="Latest release" />
          <p className="mt-2 text-lg font-semibold text-ink">
            {prod.version} went out {ago(prod.age)}
          </p>
          <p className="mt-1 text-base text-ink-muted">Its automatic check failed. The fix is already in development.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.people} testId="happening-people">
          <StatusLine icon={<Users className="h-5 w-5 text-ink-muted" aria-hidden="true" />} label="People" />
          <p className="mt-2 text-lg font-semibold text-ink">{count(people.active)} people this week</p>
          <p className="mt-1 text-base text-ink-muted">{count(people.fresh)} of them are new.</p>
        </CardLink>
      </div>

      <Section title="The parts of the shop" about="Each one is running, and here is how it is doing.">
        <Panel className="divide-y divide-border p-0">
          {[
            { name: "Orders and checkout", tone: "bad" as const, note: "Some checkouts fail since v1.8.3" },
            { name: "Order processing (in the background)", tone: "good" as const, note: "Normal" },
            { name: "Stock and inventory", tone: "good" as const, note: "Normal" },
            { name: "The storefront website", tone: "good" as const, note: "Normal" },
          ].map((part) => (
            <div key={part.name} className="flex min-h-14 items-center gap-3 px-5 py-3">
              <StatusDot tone={part.tone} />
              <span className="flex-1 text-base text-ink">{part.name}</span>
              <span className={cn("text-base", part.tone === "bad" ? "text-danger-ink" : "text-ink-muted")}>{part.note}</span>
            </div>
          ))}
        </Panel>
      </Section>

      <PageAgentStrip id="watch-alerts" lead="When something like this happens at 2am, an agent can look first and have a fix waiting." />
      <ShowDetailedView what="overview" />
    </SimplePage>
  );
}

function StatusLine({ icon, label }: { icon: React.ReactNode; label: string }) {
  return (
    <span className="flex items-center gap-2 text-base font-medium text-ink-muted">
      {icon}
      {label}
    </span>
  );
}

// ── Things that broke ───────────────────────────────────────────────────────

export function SimpleBroke() {
  const { open, fixed } = useShopIssues();
  const now = demoNow();
  const customers = open?.filter((i) => i.envName === "prod") ?? [];
  const fresh = customers.filter((i) => i.status === "new").length;
  return (
    <SimplePage
      testId="simple-broke"
      title="Things that broke"
      tone={customers.length ? "bad" : "good"}
      summary={
        !open ? (
          "Checking…"
        ) : customers.length ? (
          <>
            {customers.length === 1 ? "1 thing is broken for customers right now" : `${customers.length} things are broken for customers right now`}
            {fresh ? (fresh === customers.length ? ", and it's new." : `; ${fresh} ${fresh === 1 ? "is" : "are"} new.`) : "."} {fixed?.length ? `${fixed.length} other problems were fixed in the last two weeks.` : ""}
          </>
        ) : (
          "All good: nothing broke for customers in the last 24 hours."
        )
      }
    >
      <Section title="Broken now">
        {open && customers.length === 0 && <Panel className="text-base text-ink">Nothing is broken. When something does break, it shows up here within a minute.</Panel>}
        <ShortList
          items={customers}
          noun="problems"
          render={(issue) => {
            const plain = plainIssue(issue);
            return (
              <div key={issue.id} className="space-y-1">
                <CardLink href={issueHref(issue.id)} testId={`simple-issue-${issue.id}`}>
                  <span className="flex flex-wrap items-center gap-2">
                    {issue.status === "new" && <span className="rounded-full border border-danger-border bg-danger-surface px-2 py-0.5 text-sm font-medium text-danger-ink">New</span>}
                    <span className="text-lg font-semibold text-ink">{plain.title}</span>
                  </span>
                  {plain.why && <span className="mt-1 block text-base text-ink-muted">{plain.why}</span>}
                  <span className="mt-2 block text-base text-ink">
                    Hit {count(issue.affected)} orders · started {ago(now - issue.firstSeenMs)}, right after release {issue.firstVersion}
                    {issue.assignee ? ` · ${issue.assignee.name} is on it` : ""}
                  </span>
                </CardLink>
                <Details>
                  <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
                    <dt>Error</dt>
                    <dd className="font-mono [overflow-wrap:anywhere]">{issue.title}</dd>
                    <dt>Where</dt>
                    <dd className="font-mono [overflow-wrap:anywhere]">{issue.culprit}</dd>
                    <dt>Service</dt>
                    <dd>{issue.service}</dd>
                    <dt>ID</dt>
                    <dd className="font-mono">{issue.id}</dd>
                  </dl>
                </Details>
              </div>
            );
          }}
        />
      </Section>

      <PageAgentStrip id="fix-errors" lead="An agent can read this problem, find the change that caused it, and draft the fix." />

      {fixed && fixed.length > 0 && (
        <Section title="Fixed recently" about="Problems that were found, fixed in a release, and have not come back.">
          <ShortList
            items={fixed}
            limit={3}
            noun="fixed problems"
            render={(issue) => (
              <CardLink key={issue.id} href={issueHref(issue.id)} className="py-4">
                <span className="flex items-center gap-3">
                  <CheckCircle2 className="h-5 w-5 shrink-0 text-success-ink" aria-hidden="true" />
                  <span className="text-base text-ink">{plainIssue(issue).title}</span>
                </span>
                <span className="mt-1 block pl-8 text-base text-ink-muted">
                  Fixed in {issue.resolvedVersion} · {ago(now - issue.resolvedAtMs)}
                </span>
              </CardLink>
            )}
          />
        </Section>
      )}
      <ShowDetailedView what="error list" />
    </SimplePage>
  );
}

// ── Slow requests ───────────────────────────────────────────────────────────

export function SimpleSlow() {
  const routes = useMemo(() => routeSpeeds().filter((r) => r.requests > 0), []);
  const overall = useMemo(() => overallSpeed(), []);
  const slowest = routes[0];
  const max = Math.max(...routes.map((r) => r.slowMs), 1);
  return (
    <SimplePage
      testId="simple-slow"
      title="Slow requests"
      tone={slowest && slowest.slowMs > 1000 ? "warn" : "good"}
      summary={
        slowest && slowest.slowMs > 1000 ? (
          <>
            Most things in the shop answer in {seconds(overall.typicalMs)}. <strong className="font-semibold">{slowest.name} is the slow one:</strong> 1 in 20 takes {seconds(slowest.slowMs)} today, up from {seconds(slowest.slowYesterdayMs)} yesterday.
          </>
        ) : (
          <>All good: everything answers in {seconds(overall.typicalMs)}, the same as yesterday.</>
        )
      }
    >
      <Section title="Slowest first" about="How long customers wait, over the last hour. The bar is the slowest 1 in 20 visits.">
        <ShortList
          items={routes}
          noun="kinds of request"
          render={(route) => (
            <div key={route.op} className="space-y-1">
              <CardLink href={DEMO_LINKS.traces} cue="See examples" testId={`simple-route-${route.op}`}>
                <span className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
                  <span className="text-lg font-semibold text-ink">{route.name}</span>
                  <span className="text-base text-ink">
                    usually {seconds(route.typicalMs)} · slowest {seconds(route.slowMs)}
                  </span>
                </span>
                <span className="mt-3 block h-3 w-full overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
                  <span className={cn("block h-full rounded-full", route.slowMs > 1000 ? "bg-warning" : "bg-primary/60")} style={{ width: `${Math.max(3, (route.slowMs / max) * 100)}%` }} />
                </span>
                {route.slowMs > route.slowYesterdayMs * 1.5 && (
                  <span className="mt-2 block text-base text-warning-ink">Slower than yesterday ({seconds(route.slowYesterdayMs)})</span>
                )}
              </CardLink>
              <Details>
                <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
                  <dt>Operation</dt>
                  <dd className="font-mono [overflow-wrap:anywhere]">{route.op}</dd>
                  <dt>Service</dt>
                  <dd>{route.service}</dd>
                  <dt>Requests (1h)</dt>
                  <dd>{count(route.requests)}</dd>
                  <dt>p50 / p95</dt>
                  <dd>
                    {Math.round(route.typicalMs)}ms / {Math.round(route.slowMs)}ms
                  </dd>
                </dl>
              </Details>
            </div>
          )}
        />
      </Section>
      <PageAgentStrip id="slow-pages" lead="We spent months guessing at slow pages. Handing an agent one slow request found the cause in an afternoon." />
      <ShowDetailedView what="request timelines" />
    </SimplePage>
  );
}

// ── Releases ────────────────────────────────────────────────────────────────

export function SimpleReleases() {
  const stops = useMemo(() => runningNow(), []);
  const rows = useMemo(() => releaseRows(), []);
  const prod = stops.find((s) => s.env === "env-prod")!;
  const dev = stops.find((s) => s.env === "env-dev")!;
  return (
    <SimplePage
      testId="simple-releases"
      title="Releases"
      tone={prod.check === "failed" ? "bad" : "good"}
      summary={
        prod.check === "failed" ? (
          <>
            Customers are on {prod.version}, shipped {ago(prod.ageMs)} by {prod.by}. <strong className="font-semibold">Its automatic check failed</strong> (failed checkouts went from {INCIDENT_CHECK.errorsBefore} to {INCIDENT_CHECK.errorsAfter}). The fix, {dev.version}, is in development and will follow the same path.
          </>
        ) : (
          <>All good: customers are on {prod.version}, and it passed its check.</>
        )
      }
    >
      <Section title="Where each version is right now" about="Every release moves through the same three stops, and is checked at each one.">
        <ol className="grid gap-3 sm:grid-cols-3">
          {stops.map((stop, index) => (
            <li key={stop.env} className="relative">
              <Panel className={cn("h-full", stop.check === "failed" && "border-danger-border")}>
                <p className="text-base font-medium text-ink-muted">
                  {index + 1}. {ENV_SHORT[stop.env]}
                </p>
                <p className="mt-1 text-2xl font-semibold text-ink">{stop.version}</p>
                <p className="mt-1 text-base text-ink-muted">since {ago(stop.ageMs)}</p>
                <p className={cn("mt-3 flex items-center gap-2 text-base", stop.check === "failed" ? "text-danger-ink" : stop.check === "passed" ? "text-success-ink" : "text-ink-muted")}>
                  <StatusDot tone={stop.check === "failed" ? "bad" : stop.check === "passed" ? "good" : "neutral"} />
                  {stop.check === "failed" ? "Check failed" : stop.check === "passed" ? "Check passed" : "Being tried out"}
                </p>
              </Panel>
            </li>
          ))}
        </ol>
      </Section>

      <Section
        title="Recent releases"
        action={
          <Link href={demoSectionHref(DEMO_SECTION_RELEASES)} className="inline-flex min-h-11 items-center gap-2 rounded-lg border border-border bg-surface px-4 text-base font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            How releases work
            <ArrowRight className="h-4 w-4" aria-hidden="true" />
          </Link>
        }
      >
        <ShortList
          items={rows}
          limit={4}
          noun="releases"
          render={({ release, runningIn }) => (
            <div key={release.version} className="space-y-1">
              <CardLink href={`${DEMO_LINKS.releases}/${release.version}`} testId={`simple-release-${release.version}`}>
                <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                  <span className="text-lg font-semibold text-ink">{release.version}</span>
                  <span className="text-base text-ink">{plainSubject(release.subject)}</span>
                </span>
                <span className="mt-1 block text-base text-ink-muted">
                  by {release.author} · {ago(release.age)}
                  {runningIn.length ? ` · running in ${runningIn.map((env) => ENV_SHORT[env]).join(" and ")}` : " · replaced by a newer release"}
                </span>
              </CardLink>
              <Details label="What changed">
                <ul className="space-y-1">
                  {release.commits.map((commit) => (
                    <li key={commit.sha}>
                      {plainSubject(commit.subject)} <span className="text-ink-subtle">({commit.author}, {commit.sha.slice(0, 7)})</span>
                    </li>
                  ))}
                </ul>
              </Details>
            </div>
          )}
        />
      </Section>
      <PageAgentStrip id="release-guard" lead="An agent can check every release like this one, and move customers back to the last good build if it's bad." />
      <ShowDetailedView what="release table" />
    </SimplePage>
  );
}

// ── People ──────────────────────────────────────────────────────────────────

export function SimplePeople() {
  const week = useMemo(() => peopleThisWeek(), []);
  const change = week.activeLastWeek ? Math.round(((week.active - week.activeLastWeek) / week.activeLastWeek) * 100) : 0;
  const top = week.sources.slice(0, 5);
  const max = Math.max(...top.map((s) => s.people), 1);
  return (
    <SimplePage
      testId="simple-people"
      title="People"
      summary={
        <>
          {count(week.active)} people used the shop this week, {change >= 0 ? `${change}% more` : `${-change}% fewer`} than last week. {count(week.fresh)} of them were new, and most new people found it through {top[0]?.channel.toLowerCase() ?? "search"}.
        </>
      }
    >
      <div className="grid grid-cols-3 gap-3 sm:gap-4">
        {[
          { label: "This week", value: week.active },
          { label: "New", value: week.fresh },
          { label: "Came back", value: week.returning },
        ].map((stat) => (
          <Panel key={stat.label} className="p-4 sm:p-5">
            <p className="text-sm font-medium text-ink-muted sm:text-base">{stat.label}</p>
            <p className="mt-1 text-2xl font-semibold tabular-nums text-ink sm:text-4xl">{count(stat.value)}</p>
          </Panel>
        ))}
      </div>

      <Section title="Where new people came from" about="This week's sign-ups, by how they first arrived.">
        <Panel className="space-y-4">
          {top.map((source) => (
            <div key={source.channel} className="grid grid-cols-[minmax(8rem,12rem)_1fr_3rem] items-center gap-4">
              <span className="text-base text-ink">{source.channel}</span>
              <span className="block h-4 overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
                <span className="block h-full rounded-full bg-primary/70" style={{ width: `${(source.people / max) * 100}%` }} />
              </span>
              <span className="text-right text-base tabular-nums text-ink">{source.people}</span>
            </div>
          ))}
        </Panel>
      </Section>

      <div className="grid gap-4 sm:grid-cols-2">
        <CardLink href={DEMO_LINKS.peopleAcquisition}>
          <p className="text-lg font-semibold text-ink">Every source and campaign</p>
          <p className="mt-1 text-base text-ink-muted">Ads, newsletters, search terms and referrals, in full.</p>
        </CardLink>
        <CardLink href={`${DEMO_LINKS.happening.replace(/\/$/, "")}/users`}>
          <p className="text-lg font-semibold text-ink">Who can sign in</p>
          <p className="mt-1 text-base text-ink-muted">Customer and team accounts, and when each last visited.</p>
        </CardLink>
      </div>
      <PageAgentStrip id="weekly-digest" lead="Want this in your inbox? An agent can write it up every Monday, in plain words." />
      <ShowDetailedView what="analytics dashboards" />
    </SimplePage>
  );
}

// ── One dashboard: Checkout ─────────────────────────────────────────────────

export function SimpleCheckout() {
  const now = demoNow();
  const hours = useMemo(() => checkoutByHour(now), [now]);
  const since = now - RUNNING["env-prod"].age;
  const before = hours.filter((h) => h.startMs + HOUR <= since);
  const after = hours.filter((h) => h.startMs >= since - HOUR);
  const rate = (hs: typeof hours) => {
    const orders = hs.reduce((a, h) => a + h.orders, 0);
    return orders ? 100 - (hs.reduce((a, h) => a + h.failed, 0) / orders) * 100 : 100;
  };
  const max = Math.max(...hours.map((h) => h.orders), 1);
  const label = (ms: number) => new Date(ms).toLocaleTimeString("en-US", { hour: "numeric" });
  return (
    <SimplePage
      testId="simple-checkout"
      title="Checkout"
      tone="bad"
      summary={
        <>
          Right now about <strong className="font-semibold">{Math.round(rate(after))}% of orders go through</strong>, down from {Math.round(rate(before))}% before this morning's release. The rest fail at payment.
        </>
      }
    >
      <Section title="Orders per hour, last 24 hours" about="Each bar is one hour. The red part is orders that failed.">
        <Panel>
          <div className="flex h-56 items-end gap-1" role="img" aria-label={`Orders per hour over the last 24 hours; failures appear in the last ${Math.ceil(RUNNING["env-prod"].age / HOUR)} hour(s).`}>
            {hours.map((hour) => (
              <div key={hour.startMs} className="flex h-full flex-1 flex-col justify-end" title={`${label(hour.startMs)}: ${count(hour.orders)} orders, ${count(hour.failed)} failed`}>
                <div className="w-full rounded-t-sm bg-danger" style={{ height: `${(hour.failed / max) * 100}%` }} />
                <div className={cn("w-full bg-primary/60", hour.failed ? "" : "rounded-t-sm")} style={{ height: `${((hour.orders - hour.failed) / max) * 100}%` }} />
              </div>
            ))}
          </div>
          <div className="mt-2 flex justify-between text-sm text-ink-muted">
            <span>{label(hours[0].startMs)}</span>
            <span>{label(hours[12].startMs)}</span>
            <span>Now</span>
          </div>
          <div className="mt-4 flex flex-wrap gap-5 text-base text-ink">
            <span className="flex items-center gap-2">
              <span className="h-3 w-3 rounded-sm bg-primary/60" aria-hidden="true" /> Went through
            </span>
            <span className="flex items-center gap-2">
              <span className="h-3 w-3 rounded-sm bg-danger" aria-hidden="true" /> Failed
            </span>
          </div>
        </Panel>
      </Section>
      <CardLink href={issueHref(DEMO_INCIDENT.issueId)}>
        <p className="text-lg font-semibold text-ink">See why orders fail</p>
        <p className="mt-1 text-base text-ink-muted">The problem, the release that caused it, and who is fixing it.</p>
      </CardLink>
      <PageAgentStrip id="watch-alerts" lead="An agent can watch this chart for you, and start investigating the moment it dips." />
      <ShowDetailedView what="dashboard" />
    </SimplePage>
  );
}

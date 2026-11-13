// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's versions of the console's busiest pages, for readers who
 * are not engineers. Each one leads with ONE plain sentence, shows the few
 * things that matter as big cards that look clickable, and keeps IDs and raw
 * names visible as a quieter second line under each card (TechLine), then
 * ends with the way into the detailed page.
 *
 *   What's happening   (Overview)          SimpleHappening
 *   Things that broke  (Errors)            SimpleBroke
 *   Releases           (Releases)          SimpleReleases
 *   People             (Product analytics) SimplePeople
 *   Checkout           (one dashboard)     SimpleCheckout
 *   One service        (its dashboard)     ServiceLead, above the real charts
 *
 * Slow requests, Recordings and Alerts are in SimpleTelemetry.tsx. Their
 * figures come from simpleData.ts, which reads the shop's one story.
 */

import { useMemo } from "react";
import { ArrowRight, BellRing, CheckCircle2, CircleAlert, MonitorPlay, Rocket, Timer, Users } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { cn } from "@/lib/utils";

import { DEMO_INCIDENT, demoNow, demoStats, HOUR, RUNNING } from "../demoData/shopStory";
import { demoSectionHref, DEMO_SECTION_RELEASES } from "../demoPath";
import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS, issueHref } from "./demoLinks";
import {
  checkoutByHour,
  ENV_SHORT,
  INCIDENT_CHECK,
  overallSpeed,
  peopleThisWeek,
  plainIssue,
  plainSubject,
  PROD,
  releaseRows,
  routeSpeeds,
  runningNow,
  useShopIssues,
} from "./simpleData";
import { useRecordings } from "./SimpleTelemetry";
import { ago, CardLink, Code, count, Panel, Section, seconds, ShortList, ShowDetailedView, SimplePage, StatusDot, TechLine, Term, capital } from "./ui";

// ── What's happening ────────────────────────────────────────────────────────

/** Three tiles to a row: the "Open ›" cue sits under the text, not beside it. */
const TILE = "sm:flex-col sm:items-start sm:justify-between sm:gap-3";

export function SimpleHappening() {
  const { open } = useShopIssues();
  const speed = useMemo(() => overallSpeed(), []);
  const checkout = useMemo(() => routeSpeeds().find((r) => r.op === "checkout.v1.CheckoutService/Checkout")!, []);
  const people = useMemo(() => peopleThisWeek(), []);
  const recordings = useRecordings();
  const visits = recordings.data ?? [];
  const hitError = visits.filter((s) => s.errors > 0).length;
  const prod = RUNNING["env-prod"];
  const broken = open?.filter((i) => i.envName === "prod").length ?? 1;
  return (
    <SimplePage
      testId="simple-happening"
      title="What's happening"
      term="overview"
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
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        <CardLink href={DEMO_LINKS.errors} testId="happening-broke" className={TILE}>
          <StatusLine icon={<CircleAlert className="h-5 w-5 text-danger-ink" aria-hidden="true" />} label="Things that broke" term="errors" />
          <p className="mt-2 text-lg font-semibold text-ink">1 problem, and it's new</p>
          <p className="mt-1 text-base text-ink-muted">Some checkouts fail. Maya is on it.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.traces} testId="happening-slow" className={TILE}>
          <StatusLine icon={<Timer className="h-5 w-5 text-warning-ink" aria-hidden="true" />} label="Slow requests" term="traces" />
          <p className="mt-2 text-lg font-semibold text-ink">Most pages answer in {seconds(speed.typicalMs)}</p>
          <p className="mt-1 text-base text-ink-muted">Checkout is the exception: 1 in 20 takes {seconds(checkout.slowMs)}. See one, step by step.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.sessions} testId="happening-recordings" className={TILE}>
          <StatusLine icon={<MonitorPlay className="h-5 w-5 text-ink-muted" aria-hidden="true" />} label="Recordings" term="sessions" />
          <p className="mt-2 text-lg font-semibold text-ink">{recordings.data ? `${count(visits.length)} visits recorded today` : "Today's visits"}</p>
          <p className="mt-1 text-base text-ink-muted">{hitError ? `${hitError} hit the broken checkout. Watch what they saw.` : "Watch what visitors saw."}</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.alerts} testId="happening-alerts" className={TILE}>
          <StatusLine icon={<BellRing className="h-5 w-5 text-danger-ink" aria-hidden="true" />} label="Alerts" />
          <p className="mt-2 text-lg font-semibold text-ink">1 alert is going off</p>
          <p className="mt-1 text-base text-ink-muted">Orders are failing. The on-call person was told.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.badRelease} testId="happening-release" className={TILE}>
          <StatusLine icon={<Rocket className="h-5 w-5 text-ink-muted" aria-hidden="true" />} label="Latest release" />
          <p className="mt-2 text-lg font-semibold text-ink">
            {prod.version} went out {ago(prod.age)}
          </p>
          <p className="mt-1 text-base text-ink-muted">Its automatic check failed. The fix is already in development.</p>
        </CardLink>
        <CardLink href={DEMO_LINKS.people} testId="happening-people" className={TILE}>
          <StatusLine icon={<Users className="h-5 w-5 text-ink-muted" aria-hidden="true" />} label="People" term="analytics" />
          <p className="mt-2 text-lg font-semibold text-ink">{count(people.active)} people this week</p>
          <p className="mt-1 text-base text-ink-muted">{count(people.fresh)} of them are new.</p>
        </CardLink>
      </div>

      <Section title="The parts of the shop" about="Each one is running, and here is how it is doing over the last hour.">
        <Panel className="divide-y divide-border p-0">
          {[
            { name: "Orders and checkout", service: "api", tone: "bad" as const, note: "Some checkouts fail since v1.8.3", href: DEMO_LINKS.serviceDashboard },
            { name: "Order processing (in the background)", service: "worker", tone: "good" as const, note: "Normal" },
            { name: "Stock and inventory", service: "inventory", tone: "good" as const, note: "Normal" },
            { name: "The storefront website", service: "shop-web", tone: "good" as const, note: "Normal" },
          ].map((part) => {
            const stats = part.service === "shop-web" ? null : demoStats({ startMs: demoNow() - HOUR, endMs: demoNow(), envIds: [PROD], service: part.service });
            const body = (
              <>
                <StatusDot tone={part.tone} className="mt-2" />
                <span className="min-w-0 flex-1">
                  <span className="flex flex-wrap items-baseline justify-between gap-x-4">
                    <span className="text-base text-ink">{part.name}</span>
                    <span className={cn("text-base", part.tone === "bad" ? "text-danger-ink" : "text-ink-muted")}>{part.note}</span>
                  </span>
                  <TechLine
                    className="mt-0.5"
                    items={[<Code key="s">{part.service}</Code>, stats && `${count(stats.requests)} requests/h`, stats && `p95 ${Math.round(stats.p95Ms)}ms`, stats && `${(stats.errorRate * 100).toFixed(1)}% errors`]}
                  />
                </span>
              </>
            );
            return part.href ? (
              <Link key={part.name} href={part.href} className="flex min-h-14 items-start gap-3 px-5 py-3 hover:bg-ink/[0.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring" data-testid="happening-part-link">
                {body}
                <ArrowRight className="mt-1 h-4 w-4 shrink-0 text-primary-ink" aria-hidden="true" />
              </Link>
            ) : (
              <div key={part.name} className="flex min-h-14 items-start gap-3 px-5 py-3">
                {body}
              </div>
            );
          })}
        </Panel>
      </Section>

      <PageAgentStrip id="watch-alerts" lead="When something like this happens at 2am, an agent can look first and have a fix waiting." />
      <ShowDetailedView what="overview" />
    </SimplePage>
  );
}

function StatusLine({ icon, label, term }: { icon: React.ReactNode; label: string; term?: string }) {
  return (
    <span className="flex items-center gap-2 text-base font-medium text-ink-muted">
      {icon}
      {label}
      {term && <Term className="text-sm">{term}</Term>}
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
      term="errors"
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
              <CardLink key={issue.id} href={issueHref(issue.id)} testId={`simple-issue-${issue.id}`}>
                <span className="flex flex-wrap items-center gap-2">
                  {issue.status === "new" && <span className="rounded-full border border-danger-border bg-danger-surface px-2 py-0.5 text-sm font-medium text-danger-ink">New</span>}
                  <span className="text-lg font-semibold text-ink">{plain.title}</span>
                </span>
                {plain.why && <span className="mt-1 block text-base text-ink-muted">{plain.why}</span>}
                <span className="mt-2 block text-base text-ink">
                  Hit {count(issue.affected)} orders · started {ago(now - issue.firstSeenMs)}, right after release {issue.firstVersion}
                  {issue.assignee ? ` · ${capital(issue.assignee.name)} is on it` : ""}
                </span>
                <TechLine items={[<Code key="t">{issue.title}</Code>, <Code key="c">{issue.culprit}</Code>, issue.service, `${count(issue.events)} events`]} />
              </CardLink>
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
            <CardLink key={release.version} href={`${DEMO_LINKS.releases}/${release.version}`} testId={`simple-release-${release.version}`}>
              <span className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
                <span className="text-lg font-semibold text-ink">{release.version}</span>
                <span className="text-base text-ink">{plainSubject(release.subject)}</span>
              </span>
              <span className="mt-1 block text-base text-ink-muted">
                by {release.author} · {ago(release.age)}
                {runningIn.length ? ` · running in ${runningIn.map((env) => ENV_SHORT[env]).join(" and ")}` : " · replaced by a newer release"}
              </span>
              <TechLine
                items={[
                  `${release.commits.length} change${release.commits.length === 1 ? "" : "s"}:`,
                  ...release.commits.slice(0, 3).map((commit) => (
                    <span key={commit.sha}>
                      <Code>{commit.sha.slice(0, 7)}</Code> {commit.subject}
                    </span>
                  )),
                  release.commits.length > 3 && `and ${release.commits.length - 3} more`,
                ]}
              />
            </CardLink>
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
      term="product analytics"
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
      term="dashboard"
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

// ── One service's dashboard ─────────────────────────────────────────────────

/** The orders service's rpcs: what its dashboard (service-orders) counts. */
const ORDERS_OPS = ["orders.v1.OrdersService/CreateOrder", "orders.v1.OrdersService/GetOrder", "orders.v1.OrdersService/ListOrders", "orders.v1.OrdersService/CancelOrder"];

/** The orders service's dashboard in the simple view: what its charts say, in a sentence, above them. */
export function ServiceLead() {
  const now = demoNow();
  const { requests, errors, errorsYesterday, create } = useMemo(() => {
    const hour = ORDERS_OPS.map((op) => demoStats({ startMs: now - HOUR, endMs: now, envIds: [PROD], op }));
    const yesterday = ORDERS_OPS.map((op) => demoStats({ startMs: now - 25 * HOUR, endMs: now - 24 * HOUR, envIds: [PROD], op }));
    return {
      requests: hour.reduce((a, s) => a + s.requests, 0),
      errors: hour.reduce((a, s) => a + s.errors, 0),
      errorsYesterday: yesterday.reduce((a, s) => a + s.errors, 0),
      create: hour[0],
    };
  }, [now]);
  return (
    <section className="mx-auto w-full max-w-5xl space-y-4 pb-2" data-testid="simple-service-lead" aria-labelledby="service-lead-title">
      <h1 id="service-lead-title" className="text-3xl font-semibold tracking-tight text-ink">
        Orders
        <Term className="ml-2 text-2xl">the orders service's dashboard</Term>
      </h1>
      <p className="flex max-w-3xl items-start gap-3 text-lg leading-relaxed text-ink">
        <StatusDot tone={errors > 0 ? "bad" : "good"} className="mt-2.5" />
        <span>
          In the last hour the orders service answered {count(requests)} requests.{" "}
          <strong className="font-semibold">
            {count(errors)} failed, all of them customers placing an order
          </strong>{" "}
          ({errorsYesterday ? `${count(errorsYesterday)} this time yesterday` : "none this time yesterday"}), and 1 in 20 orders took {seconds(create.p95Ms)}. The charts below show it hour by hour: the red band starts when {RUNNING["env-prod"].version} went out.
        </span>
      </p>
      <TechLine items={[<Code key="s">orders.v1.OrdersService</Code>, "service api", `CreateOrder p50 ${Math.round(create.p50Ms)}ms · p95 ${Math.round(create.p95Ms)}ms`, `error rate ${requests ? ((errors / requests) * 100).toFixed(1) : "0"}%`]} />
      <div className="flex flex-wrap gap-3">
        <CardLink href={issueHref(DEMO_INCIDENT.issueId)} className="py-3" cue="Why">
          <span className="text-base font-medium text-ink">The failures are the broken checkout</span>
        </CardLink>
        <CardLink href={DEMO_LINKS.traces} className="py-3" cue="See one">
          <span className="text-base font-medium text-ink">The slow requests, step by step</span>
        </CardLink>
      </div>
    </section>
  );
}

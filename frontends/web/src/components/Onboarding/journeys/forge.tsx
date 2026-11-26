/**
 * The "Explore a Forge app" journey (journeys-spec.md §3).
 *
 * A short, optional walk through the forge demo console (`/forge/demo`,
 * components/Forge/Demo): a made-up app called shop, on sample observability
 * data. It opens with what forge is, follows one failing request across three
 * services, its logs and its grouped error, and ends with handing that error
 * to an agent. Exploring on your own is the default: every console view has
 * a one-time tip of its own (Forge/Demo/tips), so the tour only takes the
 * best moments.
 *
 * Drawn with the engine's callout controls (Step X of Y, Back, Next, Exit; no
 * bottom bar, no Skip step). Most required actions move the tour on as soon
 * as they're done (`autoAdvance`), so an action step's explanation sits
 * BEFORE the action, or on the step after it. Next stays enabled throughout
 * (`showNext`): it moves to the next stop, whose route opens what the action
 * would have, so a visitor who cannot find the target is never stuck.
 *
 * Where it walks (`stop`):
 *  - signed in, on either app host: the sample app INSIDE the shell
 *    (`/forge/p/sample_shop/…`, Forge/Sample/sampleApp.ts), on the shell's own
 *    nav and console pages, so the tour teaches the UI people use. Leaving it
 *    (Finish, Exit tour, Back off the first step) returns to the page it was
 *    started from (`returnToStart`), and no sample is left in the nav. While
 *    it is live (`onLiveChange`) the switcher lists the sample, and its
 *    cache goes when it ends (fixtures/scope.tsx);
 *  - the public demo host: shop's console pages (`/forge/p/shop/…`), on the
 *    same shell and the same nav anchors;
 *  - signed out on an app host: `/forge/demo`, which leaves for the demo host.
 *
 * Cuts (isAvailable; lib/demoTour.ts, runtime config DEMO_TOUR):
 *  - the public demo host, `short` (default), 10 steps: intro, instrumented,
 *    trace, hops, logs, issue, ask, analytics, built (the recorded forge run,
 *    a dialog), done;
 *  - the demo host, `full`: every step but the shell's;
 *  - the shell, 16 steps: intro, nav, instrumented, deployments, trace, hops,
 *    logs, issue, oncall, ask, analytics, data, tests, security, configure,
 *    done;
 *  - signed out on an app host, 11 steps: no fg-inside, fg-graph, fg-agent,
 *    fg-analytics, fg-data, fg-built or the shell's steps. Their /forge/demo leaves for the demo
 *    host (Forge/Demo/demoHandoff.ts), `?tour=` kept, so a journey started
 *    there continues on the demo host's cut.
 *
 * Nothing here needs a daemon, a chat or a signed-in user: every target is
 * sample data, every required action is a URL change or a tour signal, and
 * copy that differs (the CLI's name, what Autopilot needs) is `copyByShell`.
 *
 * The ids below are the fixture's (Forge/Observability/__fixtures__); they are
 * repeated rather than imported so the fixtures stay out of the app's main
 * chunk. A test pins them to the fixture's.
 */

import { lazy, Suspense } from "react";

import { FORGE_DEMO_PATH } from "../../Forge/Demo/demoPath";
import { FORGE_ONE_LINER } from "../../Forge/Demo/demoCopy";
import { trackEvent } from "../../../lib/analytics";
import { notOnShortDemo, onDemo, onDemoFull } from "../../../lib/demoTour";
import { DemoCta } from "../../Forge/Demo/DemoCta";
import { SAMPLE_APP, SAMPLE_ID_PREFIX } from "../../Forge/Sample/sampleApp";
import { setSampleTourActive } from "../../Forge/Sample/sampleTour";
import { getDemoTourHooks, getReliantTourHooks } from "../tourShell";
import type { Journey, JourneyRoute, RouteTarget } from "../types";
import { TourCommands } from "./TourCommand";
import { projectsPath, workflowDetailPath } from "@/lib/paths";
import { consoleBase, consolePath, type ConsolePage } from "@/components/Forge/Console/paths";
import { DEFAULT_ENV } from "@/components/Forge/Console/envScope";

/** The CreateOrder trace the tour opens (FIXTURE_TRACE_ID): two failed payment calls, then the worker. */
export const DEMO_TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";
/** The issue that trace belongs to: first seen in release v1.8.3, its latest event is that trace. */
export const DEMO_ISSUE_ID = "iss-orders-deadline";
/** In that trace: inventory's server span, the hop the worker made over Connect (fg-hops). */
export const DEMO_INVENTORY_SPAN_ID = "e1";
/** In that trace: the api's CreateOrder method span, one SELECT, and the worker's consumer span (fg-inside). */
export const DEMO_INSIDE_SPAN_IDS = { method: "a2", query: "b1", worker: "d1" } as const;

/** fg-built's view: the recorded forge run. Its own chunk, so the fixtures stay out of the main one. */
const BuiltTourStep = lazy(() => import("../../Forge/Demo/howBuilt/BuiltTourStep"));

const AUTOPILOT_PATH = workflowDetailPath("builtin://autopilot-oncall");
const RELIANT_APP_URL = "https://app.reliantlabs.io";
const QUICK_START_URL = "https://docs.reliantlabs.io/forge/quick-start";

/**
 * A step on the demo console, opening it with `search` (merged when already
 * there). It closes "How this app was built" unless the step names it.
 */
function demo(search: Record<string, unknown>): JourneyRoute {
  const open: RouteTarget = { to: FORGE_DEMO_PATH, search: { section: undefined, ...search } };
  return { path: FORGE_DEMO_PATH, open, label: "Open the demo console", message: "This step is on the Forge demo console." };
}

/** On the public demo: its boot installs the demo's tour hooks (Forge/Demo/demoShellBoot.ts), and nothing else does. */
const onPublicDemo = (): boolean => getDemoTourHooks() !== null;

/**
 * Whether the tour walks the sample app inside the signed-in shell: someone
 * is signed in on an app host (catalog.ts `inAppShell`), never on the public
 * demo.
 */
export function inAppShell(): boolean {
  return !onPublicDemo() && (getReliantTourHooks()?.inAppShell?.() ?? false);
}

/** Only in the signed-in shell: the steps about its own nav and pages. */
const onlyInShell = () => inAppShell();

/** The steps the signed-in shell leaves out (its nav step, fg-nav, says what fg-console does, for the sample). */
const notInShell = () => !inAppShell();

type StopView = "overview" | "traces" | "errors" | "graph" | "alerts";

/**
 * The console a step walks: the public demo's own app (its tour hooks:
 * shop), the sample app's in the signed-in shell, or none (signed out on an
 * app host: `/forge/demo`, which leaves for the demo host).
 */
function walkedApp(): string | null {
  return getDemoTourHooks()?.app ?? (inAppShell() ? SAMPLE_APP : null);
}

/**
 * A step's page: the walked app's console page for `view` (an Observability
 * view's path covers the views a step moves on to, logs and profiles), or the
 * demo console's (`demo`). Read when the step is, so the shell and the
 * session are known by then.
 */
function stop({ view, ...rest }: { view: StopView } & Record<string, unknown>): JourneyRoute {
  const app = walkedApp();
  if (!app) return demo({ view, ...rest });
  const base = consoleBase(app);
  const search = { ...storyScope(), ...rest };
  const label = "Open the sample app";
  const message = "This step is on the sample app's console.";
  if (view === "overview") return { path: base, open: { to: base, search }, label, message };
  return { path: `${base}/observability`, open: { to: `${base}/observability/${view}`, search }, label, message };
}

/**
 * A step on another of the walked app's console pages (Analytics, Data), in
 * prod unless `search` names an environment. Signed out on an app host, where
 * these steps do not run, the demo console's, like `stop`.
 */
function consoleStop(page: ConsolePage, search: Record<string, unknown> = {}): JourneyRoute {
  const app = walkedApp();
  if (!app) return demo({ view: "overview", ...CLOSED });
  const path = consolePath(app, page);
  return { path, open: { to: path, search: { ...CLOSED, ...storyScope(), ...search } }, label: "Open the sample app", message: "This step is on the sample app's console." };
}

/**
 * On the demo host, the environment the console's steps are scoped to: the
 * story's failing request, its issue and its alert are prod's. Named on every
 * one, because the console remembers the scope a page was opened in
 * (envScope.ts), so a visitor who had picked dev, or fg-data's staging, would
 * otherwise carry it into the steps after. The shell's sample is never
 * remembered, so its steps open on the default, prod, as they are.
 */
const storyScope = (): Record<string, unknown> => (onPublicDemo() ? { env: DEFAULT_ENV } : {});

/**
 * A fixture id (a trace, a span, an issue) as the walked app's pages show
 * it: the sample's scope mints every id with SAMPLE_ID_PREFIX
 * (fixtures/shop/handlers/idPrefix.ts), the demo host's as it is. Read when
 * the step is, like its route.
 */
function shown(id: string): string {
  return walkedApp() === SAMPLE_APP ? `${SAMPLE_ID_PREFIX}${id}` : id;
}

/** A shell step's page: one of the sample app's console pages. */
function samplePage(page: ConsolePage): JourneyRoute {
  const path = consolePath(SAMPLE_APP, page);
  return { path, open: { to: path, search: CLOSED }, label: "Open the sample app", message: "This step is on the sample app's console." };
}

/** On a page of `view`: the demo console's `?view=`, or the console's own path. */
const onView = (location: { pathname: string; search: Record<string, unknown> }, view: string) =>
  location.search.view === view || location.pathname.endsWith(`/observability/${view}`);

// The panel's keys a step clears, so arriving at it (Back included) starts from its list, not an open item.
const CLOSED = { trace: undefined, span: undefined, issue: undefined };

function ExternalLink({ href, children, onClick }: { href: string; children: React.ReactNode; onClick?: () => void }) {
  return (
    <a href={href} target="_blank" rel="noopener noreferrer" onClick={onClick} className="font-medium text-primary-ink underline-offset-2 hover:underline">
      {children}
    </a>
  );
}

/** "Your agent runs `…`": the one line on each step that says what an agent does with it. */
function AgentLine({ children }: { children: React.ReactNode }) {
  return <p className="mt-2 text-xs text-muted-foreground">{children}</p>;
}

/** Two sentences: what Forge is, and what the tour shows. */
function WhatIsForge() {
  return (
    <p>
      {FORGE_ONE_LINER} This is the console it gives every app, here for a sample shop: the next stops follow one failing request
      through it.
    </p>
  );
}

const ONCALL_LEAD =
  "Alerts reach you by text, email or Slack. An alert can also trigger a configured Reliant workflow to investigate, test a fix and propose or release it under your chosen approvals.";

const tryCommands = (cli: string) => [`${cli} obs status prod`, `${cli} obs issues prod`, `${cli} obs traces prod --errors`];
const scaffoldCommands = (cli: string) => [`${cli} scaffold service inventory`, `${cli} scaffold worker fulfilment`, `${cli} lint --tracing`];

function TryOnYourApp({ cli }: { cli: string }) {
  // The public demo hands off here: one call to action, not a command list for someone who has not installed it.
  if (onPublicDemo()) {
    return (
      <>
        <p>
          That&apos;s the tour. Sign up to get this console for your own app, or install Forge and run <code>forge project start</code>. The rest of
          the console is yours to explore.
        </p>
        <div className="mt-3">
          <DemoCta placement="end_card" />
        </div>
      </>
    );
  }
  return (
    <>
      <p>The tour uses sample data to show the monitoring Forge sets up for an app. In your own project:</p>
      <TourCommands commands={scaffoldCommands(cli)} />
      <p className="mt-3">
        Ask your agent to add a service or background worker. Forge generates the starting code and request-tracking setup;
        the agent implements the app&apos;s custom behavior and runs the checks. Once the app is deployed, the agent can inspect
        its real health information (use your own environment name):
      </p>
      <TourCommands commands={tryCommands(cli)} />
      <p className="mt-3">
        New to Forge? <ExternalLink href={QUICK_START_URL}>Quick start</ExternalLink>. The rest of the console is yours to explore.
      </p>
    </>
  );
}

const agentCommands = (cli: string) => [`${cli} obs status prod`, `${cli} obs trace ${DEMO_TRACE_ID} prod`, `${cli} obs traces prod --errors --json`];
const agentSetup = (cli: string) => [`${cli} agent skill load observability/query`, `claude mcp add forge -- ${cli} agent mcp`];

function WhatYourAgentReads({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Your coding agent reads the same data from a terminal. Each <code>forge obs</code> command prints text sized for a model&apos;s
        context. With <code>--json</code> it prints a stable document with the next commands to run, and exit codes a script can branch
        on. <code>obs trace</code> collapses repeated spans, marks the slowest path, and points out time no span accounts for.
      </p>
      <TourCommands commands={agentCommands(cli)} />
      <p className="mt-3">Give it Forge&apos;s playbook and tools once:</p>
      <TourCommands commands={agentSetup(cli)} />
      <p className="mt-2 text-xs text-muted-foreground">
        The MCP server gives an agent Forge&apos;s skills, lint and code generation; it runs <code>forge obs</code> in its own shell. The
        trace id is the sample&apos;s.
      </p>
    </>
  );
}

function Hops({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Follow one request across services to find where it failed or spent time. Here the payment was retried and failed before the
        order was processed and stock was reserved. The timeline connects those actions so you and your agent can investigate the same evidence.
      </p>
      <AgentLine>
        Your agent runs <code>{cli} obs trace {DEMO_TRACE_ID.slice(0, 8)}… prod</code> and reads the same waterfall as a tree, with the
        slowest path marked.
      </AgentLine>
    </>
  );
}

function Logs({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Every log line Forge&apos;s logger writes carries its trace id, so one click gives you exactly what this request logged, in every
        service, with no search to write.
      </p>
      <AgentLine>
        Your agent gets the same lines with <code>{cli} obs logs prod --trace &lt;id&gt;</code>.
      </AgentLine>
    </>
  );
}

function Issue({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Forge groups repeated failures so you can see affected users and follow a failure to its request and release.
        Because Forge manages releases, it can tie a first occurrence or regression to a release and help your agent focus on changed code.
      </p>
      <AgentLine>
        Your agent runs <code>{cli} obs issue &lt;id&gt; prod</code>: the latest event, the release it first appeared in, and the suspect
        commits.
      </AgentLine>
    </>
  );
}

function Deployments({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Deployments holds what the app runs and how it got there: its services, every release, and each environment. Forge builds a
        release once and promotes the same build from staging to prod, so prod runs exactly what staging ran. Promote and roll back are
        on each release.
      </p>
      <AgentLine>
        Your agent runs <code>{cli} env deploy prod &lt;release&gt;</code>, then <code>{cli} obs check prod --wait</code> to prove the release
        healthy.
      </AgentLine>
    </>
  );
}

function Tests({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Tests shows the app&apos;s own test suite, commit by commit: coverage, pass rate, and the tests that fail only some of the time.
        Each recorded run is kept, so a flaky test is found before it holds up a release.
      </p>
      <AgentLine>
        Your agent iterates on <code>task test:short</code>; <code>{cli} lint</code> catches whole classes of bugs before a test runs.
      </AgentLine>
    </>
  );
}

function Analytics({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Analytics shows who uses the app: visitors, sign-ups, active and returning users, and where they came from, counted from the events
        the app sends. Sessions, under Observability, replays a visit with its errors marked.
      </p>
      <AgentLine>
        Your agent reads the same figures with <code>{cli} obs users prod</code>.
      </AgentLine>
    </>
  );
}

function Data({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Data browses the app&apos;s database, table by table, read-only. This is staging. In prod, reading asks for a reason first and every
        read is recorded, so looking at customer data always leaves a trail.
      </p>
      <AgentLine>
        Your agent runs one read-only query with <code>{cli} db query prod … --reason &lt;why&gt;</code>, its reason recorded the same way.
      </AgentLine>
    </>
  );
}

const SECURITY_COPY =
  "Every release Forge builds carries a software bill of materials (SBOM), and Security scans it: which known vulnerabilities each environment runs, and in which packages. A finding can go to an agent to patch on its own branch.";

// Usage (what hosting costs) is in Configure too, where the app is hosted; the public demo serves no Usage
// (DEMO_HIDDEN_PAGES), so the step names the pages every Configure has.
const CONFIGURE_COPY =
  "Configure holds the rest of the app's settings: its secrets, kept out of git; its custom domains, with HTTPS; and an activity log of every change, from releases to settings.";

const SHELL_NAV_COPY =
  "Every Forge app gets these sections in the left nav: what it runs and ships, how it behaves, who uses it, its tests, its data, and its settings. This is shop, a sample app with an api, a worker and an inventory service in dev, staging and prod. Its data is made up, and nothing you do here touches your own apps.";

export const forgeJourney: Journey = {
  id: "forge",
  // v4: on the demo host, the sample app's own console pages (v3: the short tour's intro). Walking the sample app in
  // the signed-in shell is not a bump: a bump re-offers the journey by opening it on the person's next load, and
  // whether to do that to everyone who finished v4 is the owner's call, not this change's.
  version: 4,
  title: "Explore a Forge app",
  description:
    "Walk through a running app's console: one request across three services, its logs and its error, and how an agent gets the problem.",
  controls: "callout",
  // The person's apps (Projects, narrowed to them), on either brand; signed out, sign-in comes first.
  // The demo: nowhere to go, so the visitor stays on the console with the tour closed.
  finish: () => (onPublicDemo() ? null : { to: projectsPath(), search: { show: "apps" } }),
  // In the shell the tour walks a sample, not the person's app: leaving it goes back to where they were.
  returnToStart: () => inAppShell(),
  // While it walks the sample there, the switcher lists it ("shop (sample)"); when the tour stops, however it
  // stops, the row goes and so does everything the sample's scope cached (fixtures/scope.tsx).
  onLiveChange: (live) => setSampleTourActive(live && inAppShell()),
  steps: [
    {
      id: "fg-intro",
      title: "What is Forge?",
      description: <WhatIsForge />,
      get route() {
        return stop({ view: "overview", ...CLOSED });
      },
    },
    {
      id: "fg-console",
      title: "A Forge app's console",
      description:
        "Every Forge project gets this console: releases and environments, observability, product analytics, tests, its data, security and settings, in one place. shop has an api, a worker and an inventory service in dev, staging and prod. The data here is made up.",
      // The console's sections: the open app's rows in the shell's left nav (NavRenderer's `nav-top`), on the
      // demo host too, which runs in the same shell. The step is on the Overview, never in a drill-in: a step
      // that rings the nav on an Observability route points at `nav-drill-in` (the drill-in's header) instead.
      target: "[data-tour='forge-console-nav']",
      tooltipPosition: "right",
      get route() {
        return stop({ view: "overview", ...CLOSED });
      },
      // The demo host's short tour leaves it out: the console is there to explore, with a tip per view.
      isAvailable: () => notInShell() && notOnShortDemo(),
    },
    {
      id: "fg-nav",
      title: "A Forge app's console",
      description: SHELL_NAV_COPY,
      // The shell's main nav (NavRenderer's `nav-top`): with an app open, its rows are the app's sections.
      target: "[data-tour='forge-console-nav']",
      tooltipPosition: "right",
      get route() {
        return stop({ view: "overview", ...CLOSED });
      },
      isAvailable: onlyInShell,
    },
    {
      id: "fg-instrumented",
      title: "Monitoring from the start",
      description:
        "Forge gives your app monitoring from the start, so you can find problems without adding it by hand. Follow a slow or failed request to its logs and the code doing the work; your agent can read the same evidence.",
      // The demo console's health strip; on the console's Overview, its services with their requests, errors and latency.
      target: "[data-testid='obs-overview'], [data-testid='overview-services']",
      get route() {
        return stop({ view: "overview", ...CLOSED });
      },
      // No isAvailable on purpose: in every cut, the demo's included (owner, 2026-11-06). fg-built, at the end,
      // does not replace it; the short demo makes room by leaving out fg-agent.
    },
    {
      id: "fg-deployments",
      title: "Releases, built once",
      description: <Deployments cli="reliant forge" />,
      copyByShell: { forge: { description: <Deployments cli="forge" /> } },
      // The nav's Deployments group, open on the Releases page beside it.
      target: "[data-tour='nav-deployments']",
      tooltipPosition: "right",
      get route() {
        return samplePage({ kind: "releases" });
      },
      isAvailable: onlyInShell,
    },
    {
      id: "fg-trace",
      showNext: true,
      title: "One request, end to end",
      description: "Find exactly where an error happens or time is spent in a user’s journey—even when the request crosses several services. Open the failed CreateOrder request to see its timeline; your agent reads the same evidence.",
      // The console's list row, or the led Traces page's (Observability/Leads).
      get target() {
        const trace = shown(DEMO_TRACE_ID);
        return `[data-testid='obs-trace-row'][data-trace-id='${trace}'], [data-testid='lead-trace-row'][data-trace-id='${trace}']`;
      },
      get route() {
        return stop({ view: "traces", ...CLOSED });
      },
      advanceOn: { kind: "when", test: ({ location }) => location.search.trace === shown(DEMO_TRACE_ID) },
      actionHint: "Open the CreateOrder trace",
      autoAdvance: true,
      signal: "trace_opened",
    },
    {
      id: "fg-hops",
      showNext: true,
      title: "Across services",
      description: <Hops cli="reliant forge" />,
      copyByShell: { forge: { description: <Hops cli="forge" /> } },
      get target() {
        return `[data-testid='trace-span-row'][data-span-id='${shown(DEMO_INVENTORY_SPAN_ID)}']`;
      },
      get route() {
        return stop({ view: "traces", trace: shown(DEMO_TRACE_ID), span: undefined, issue: undefined });
      },
      advanceOn: { kind: "when", test: ({ location }) => location.search.span === shown(DEMO_INVENTORY_SPAN_ID) },
      actionHint: "Click Reserve on inventory, near the bottom",
      autoAdvance: true,
      signal: "service_hop_opened",
    },
    {
      id: "fg-inside",
      title: "Inside a service",
      description: (
        <>
          <p>
            This is CreateOrder inside the api service. Below it are two database queries, with their values replaced by ?, and the
            payment call. Each recorded piece of work is called a span; together they form the request timeline. Forge includes the setup
            for recording these steps.
          </p>
          <p className="mt-2">
            <code>forge lint --tracing</code> checks for common gaps, such as a log call missing the request&apos;s context, to help your
            agent keep the timeline connected.
          </p>
        </>
      ),
      // The demo host's full tour only: the instrumentation forge writes WITHIN a service. The short tour gives
      // its place to fg-analytics (at most ten steps, lib/demoTour.ts); the Traces tip covers spans.
      isAvailable: onDemoFull,
      get target() {
        return `[data-testid='trace-span-row'][data-span-id='${shown(DEMO_INSIDE_SPAN_IDS.method)}']`;
      },
      get route() {
        return stop({ view: "traces", trace: shown(DEMO_TRACE_ID), issue: undefined });
      },
    },
    {
      id: "fg-logs",
      showNext: true,
      title: "What that request logged",
      description: <Logs cli="reliant forge" />,
      copyByShell: { forge: { description: <Logs cli="forge" /> } },
      target: "[data-testid='trace-logs']",
      get route() {
        return stop({ view: "traces", trace: shown(DEMO_TRACE_ID), span: undefined, issue: undefined });
      },
      phases: [
        {},
        {
          when: { kind: "element", selector: "[data-testid='obs-logs-trace-scope']" },
          target: "[data-testid='obs-logs-table']",
          description:
            "Five lines from three services: the order started, the first charge failed and was retried, the retry hit its deadline, the worker processed the order event, and inventory reserved the items.",
        },
      ],
      advanceOn: {
        kind: "when",
        test: ({ location }) => onView(location, "logs") && location.search.trace === shown(DEMO_TRACE_ID),
      },
      // Held: the next step is on another page, and the logs this opens are the point.
      autoAdvance: false,
      actionHint: "Click Logs for this trace",
      signal: "trace_logs_opened",
    },
    {
      id: "fg-profile",
      showNext: true,
      title: "Where the CPU went",
      description:
        "Profiles are linked to traces, so the flamegraph shows the CPU the api spent inside this request: each bar is a function, and wider means more time.",
      target: "[data-testid='trace-flamegraph']",
      // Back to the trace: the flamegraph button is on it.
      get route() {
        return stop({ view: "traces", trace: shown(DEMO_TRACE_ID), span: undefined, issue: undefined });
      },
      advanceOn: { kind: "when", test: ({ location }) => onView(location, "profiles") },
      actionHint: "Click Flamegraph",
      autoAdvance: true,
      signal: "flamegraph_opened",
      // The demo host's short tour leaves it out (DEMO_TOUR); the Profiles tip covers it. So does the shell's,
      // which spends its stops on the app's other sections.
      isAvailable: () => notInShell() && notOnShortDemo(),
    },
    {
      id: "fg-graph",
      title: "Who calls whom",
      description:
        "Because every hop carries the trace, Forge can draw the app from its spans. The browser calls the api; the api uses the database, the payment provider and a queue; the worker calls inventory. Nobody drew this map. Point at a line to see its requests and errors.",
      // The demo host's full tour only (DEMO_TOUR=full).
      isAvailable: onDemoFull,
      target: "[data-testid='graph-view']",
      get route() {
        return stop({ view: "graph", ...CLOSED });
      },
    },
    {
      id: "fg-issue",
      showNext: true,
      title: "Errors, grouped by release",
      description: <Issue cli="reliant forge" />,
      copyByShell: { forge: { description: <Issue cli="forge" /> } },
      // The console's inbox row, or the led Errors page's newest-issue card and rows (Observability/Leads).
      get target() {
        const issue = shown(DEMO_ISSUE_ID);
        return `[data-testid='obs-issue-${issue}'], [data-testid='lead-featured-issue'][data-issue-id='${issue}'], [data-testid='lead-issue-row'][data-issue-id='${issue}']`;
      },
      get route() {
        return stop({ view: "errors", ...CLOSED });
      },
      advanceOn: { kind: "when", test: ({ location }) => location.search.issue === shown(DEMO_ISSUE_ID) },
      actionHint: "Open the newest issue",
      autoAdvance: true,
      signal: "issue_opened",
    },
    {
      id: "fg-oncall",
      title: "How an agent gets called",
      description: (
        <>
          <p>
            {ONCALL_LEAD} Nothing deploys without configuration and permission.
          </p>
          <p className="mt-2">
            <ExternalLink href={AUTOPILOT_PATH} onClick={() => trackEvent("tour_link_clicked", { journey: "forge", target: "autopilot" })}>
              See Autopilot on-call
            </ExternalLink>
          </p>
        </>
      ),
      copyByShell: {
        forge: {
          description: (
            <>
              <p>
                {ONCALL_LEAD} Nothing deploys without configuration and permission. You can also hand an alert to your own agent.
              </p>
              <p className="mt-2">
                <ExternalLink
                  href={`${RELIANT_APP_URL}${AUTOPILOT_PATH}`}
                  onClick={() => trackEvent("tour_link_clicked", { journey: "forge", target: "autopilot" })}
                >
                  Set it up in Reliant
                </ExternalLink>
              </p>
            </>
          ),
        },
      },
      target: "[data-testid='obs-alerts-firing']",
      get route() {
        return stop({ view: "alerts", ...CLOSED });
      },
      // The demo host's short tour leaves it out (DEMO_TOUR); the Alerts tip covers it.
      isAvailable: notOnShortDemo,
    },
    {
      id: "fg-agent",
      title: "What your agent reads",
      description: <WhatYourAgentReads cli="reliant forge" />,
      copyByShell: { forge: { description: <WhatYourAgentReads cli="forge" /> } },
      // The demo host's full tour only (DEMO_TOUR=full): the short tour makes room for fg-built, whose card
      // already says what forge gives an agent, and fg-ask hands the issue to one.
      isAvailable: onDemoFull,
    },
    {
      id: "fg-ask",
      showNext: true,
      title: "Hand it to an agent",
      description:
        "On your own app, Investigate in chat opens a new chat with this issue written up, and Fix with agent starts one on its own branch. This is sample data, so here it shows what the agent is given: ids and the commands to fetch the rest. It's never the log text itself.",
      copyByShell: {
        forge: {
          description:
            "This issue is a good one to hand to your own agent: Claude Code, Codex or Cursor. The prompt holds ids and Forge commands, not log text, so your agent fetches the details with its own login.",
        },
      },
      get route() {
        return stop({ view: "errors", issue: shown(DEMO_ISSUE_ID), trace: undefined, span: undefined });
      },
      phases: [
        { target: "[data-testid='ask-reliant-actions']" },
        {
          when: { kind: "element", selector: "[data-testid='ask-agent-sheet']" },
          target: "[data-testid='ask-agent-sheet']",
          description:
            "Copy the prompt into your agent, or run the commands yourself. Or skip the copying: a Reliant workflow can do this for every new issue. The last line of the sheet says how.",
        },
      ],
      advanceOn: { kind: "event", event: "askAgent:opened" },
      // Held: the sheet it opens is the point of the step.
      autoAdvance: false,
      actionHint: "Click Ask an agent",
      signal: "ask_agent_opened",
    },
    // Experiments (Analytics ▾) and ML get their steps here once the demo's sample app serves them.
    {
      id: "fg-analytics",
      title: "Who uses the app",
      description: <Analytics cli="reliant forge" />,
      copyByShell: { forge: { description: <Analytics cli="forge" /> } },
      // The nav's Analytics group, open on its first tab (Product overview) beside it.
      target: "[data-tour='nav-analytics']",
      tooltipPosition: "right",
      get route() {
        return consoleStop({ kind: "analytics" });
      },
      // Wherever the tour walks an app's console: the shell's sample and both of the demo host's cuts.
      isAvailable: () => walkedApp() !== null,
    },
    {
      id: "fg-data",
      title: "The app's database",
      description: <Data cli="reliant forge" />,
      copyByShell: { forge: { description: <Data cli="forge" /> } },
      target: "[data-tour='nav-data']",
      tooltipPosition: "right",
      // On staging: prod's database is audited, and its reason dialog would open over the tour (DataBrowser).
      get route() {
        return consoleStop({ kind: "data" }, { env: "staging" });
      },
      // The demo host's short tour leaves it out (at most ten steps); the Data tip covers it.
      isAvailable: () => inAppShell() || onDemoFull(),
    },
    {
      id: "fg-tests",
      title: "The app's tests",
      description: <Tests cli="reliant forge" />,
      copyByShell: { forge: { description: <Tests cli="forge" /> } },
      target: "[data-tour='nav-tests']",
      tooltipPosition: "right",
      get route() {
        return samplePage({ kind: "tests" });
      },
      isAvailable: onlyInShell,
    },
    {
      id: "fg-security",
      title: "What each release ships",
      description: SECURITY_COPY,
      target: "[data-tour='nav-security']",
      tooltipPosition: "right",
      get route() {
        return samplePage({ kind: "vulnerabilities" });
      },
      isAvailable: onlyInShell,
    },
    {
      id: "fg-configure",
      title: "Secrets, domains and activity",
      description: CONFIGURE_COPY,
      target: "[data-tour='nav-configure']",
      tooltipPosition: "right",
      get route() {
        return samplePage({ kind: "secrets" });
      },
      isAvailable: onlyInShell,
    },
    {
      id: "fg-built",
      title: "How it was built",
      // The recorded forge run as the step's own view: a wide modal card holding the player (no spotlight, the
      // page behind scroll-locked), so the player can't be scrolled out from under the step. It plays by itself.
      description: (
        <Suspense fallback={<p>Loading the recorded run…</p>}>
          <BuiltTourStep />
        </Suspense>
      ),
      card: { wide: true, modal: true },
      // The demo host only: the last thing before the end card.
      isAvailable: onDemo,
      get route() {
        return stop({ view: "overview", ...CLOSED });
      },
    },
    {
      id: "fg-done",
      title: "Try it on your app",
      description: <TryOnYourApp cli="reliant forge" />,
      copyByShell: { forge: { description: <TryOnYourApp cli="forge" /> } },
    },
  ],
};

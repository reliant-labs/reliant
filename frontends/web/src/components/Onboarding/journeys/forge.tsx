/**
 * The "Explore a forge app" journey (journeys-spec.md §3).
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
 * BEFORE the action, or on the step after it.
 *
 * Cuts (isAvailable; lib/demoTour.ts, runtime config DEMO_TOUR):
 *  - the public demo host, `short` (default), 10 steps: intro, instrumented,
 *    trace, hops, inside, logs, issue, ask, built (the recorded forge run, a
 *    dialog), done;
 *  - the demo host, `full`: every step;
 *  - Reliant and the forge shell, 11 steps: no fg-inside, fg-graph,
 *    fg-agent or fg-built.
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
import { FORGE_ONE_LINER, FORGE_REPO_URL, FORGE_WHAT_IT_DOES } from "../../Forge/Demo/demoCopy";
import { trackEvent } from "../../../lib/analytics";
import { isForgeUIEnabled } from "../../../lib/forgeFeature";
import { isDemoShell } from "../../../lib/shell";
import { notOnShortDemo, onDemo, onDemoFull } from "../../../lib/demoTour";
import { DemoCta, InstallForgeLink } from "../../Forge/Demo/DemoCta";
import type { Journey, JourneyRoute, RouteTarget } from "../types";
import { TourCommands } from "./TourCommand";

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

const AUTOPILOT_PATH = "/workflows/library/builtin%3A%2F%2Fautopilot-oncall";
const RELIANT_APP_URL = "https://app.reliantlabs.io";
const QUICK_START_URL = "https://docs.reliantlabs.io/forge/quick-start";

/**
 * A step on the demo console, opening it with `search` (merged when already
 * there). It closes "How this app was built" unless the step names it.
 */
function demo(search: Record<string, unknown>): JourneyRoute {
  const open: RouteTarget = { to: FORGE_DEMO_PATH, search: { section: undefined, ...search } };
  return { path: FORGE_DEMO_PATH, open, label: "Open the demo console", message: "This step is on the forge demo console." };
}

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

function WhatIsForge() {
  return (
    <>
      <p>{FORGE_ONE_LINER}</p>
      <p className="mt-2">{FORGE_WHAT_IT_DOES}</p>
      <p className="mt-2">
        This is the console forge gives every app, for a sample app called shop. A few steps follow one failing request through it.
      </p>
      <p className="mt-3 flex flex-wrap gap-x-4 gap-y-1">
        <InstallForgeLink placement="tour_intro" />
        <ExternalLink href={FORGE_REPO_URL}>It&apos;s open source</ExternalLink>
      </p>
    </>
  );
}

const ONCALL_LEAD =
  "Configure notifications by text, email or Slack where supported. An alert can trigger a configured Reliant workflow to investigate, test a fix and propose or release it under your chosen approvals.";

const tryCommands = (cli: string) => [`${cli} obs status prod`, `${cli} obs issues prod`, `${cli} obs traces prod --errors`];
const scaffoldCommands = (cli: string) => [`${cli} scaffold service inventory`, `${cli} scaffold worker fulfilment`, `${cli} lint --tracing`];

function TryOnYourApp({ cli }: { cli: string }) {
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
        New to forge? <ExternalLink href={QUICK_START_URL}>Quick start</ExternalLink>. The rest of the console is yours to explore: each
        view has a short tip the first time you open it.
      </p>
      {/* The public demo's way out: install it, or make an account. */}
      {isDemoShell() && (
        <div className="mt-3">
          <DemoCta placement="end_card" />
        </div>
      )}
    </>
  );
}

const agentCommands = (cli: string) => [`${cli} obs status prod`, `${cli} obs trace ${DEMO_TRACE_ID} prod`, `${cli} obs traces prod --errors --json`];
const agentSetup = (cli: string) => [`${cli} skill load observability/query`, `claude mcp add forge -- ${cli} mcp`];

function WhatYourAgentReads({ cli }: { cli: string }) {
  return (
    <>
      <p>
        Your coding agent reads the same data from a terminal. Each <code>forge obs</code> command prints text sized for a model&apos;s
        context. With <code>--json</code> it prints a stable document with the next commands to run, and exit codes a script can branch
        on. <code>obs trace</code> collapses repeated spans, marks the slowest path, and points out time no span accounts for.
      </p>
      <TourCommands commands={agentCommands(cli)} />
      <p className="mt-3">Give it forge&apos;s playbook and tools once:</p>
      <TourCommands commands={agentSetup(cli)} />
      <p className="mt-2 text-xs text-muted-foreground">
        The MCP server gives an agent forge&apos;s skills, lint and code generation; it runs <code>forge obs</code> in its own shell. The
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
        Every log line forge&apos;s logger writes carries its trace id, so one click gives you exactly what this request logged, in every
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
        Because forge manages releases, it can tie a first occurrence or regression to a release and help your agent focus on changed code.
      </p>
      <AgentLine>
        Your agent runs <code>{cli} obs issue &lt;id&gt; prod</code>: the latest event, the release it first appeared in, and the suspect
        commits.
      </AgentLine>
    </>
  );
}

export const forgeJourney: Journey = {
  id: "forge",
  // v3: the short, optional tour with compact controls and the "What is forge?" intro (v2 was the 9-step console tour).
  version: 3,
  title: "Explore a forge app",
  description:
    "Walk through a running app's console: one request across three services, its logs and its error, and how an agent gets the problem.",
  controls: "callout",
  // Settings → Developer can switch the forge UI off; that turns its tour off too. (On by default, and in the forge shell.)
  isAvailable: isForgeUIEnabled,
  // Reliant: forge's Apps (the bare /forge lands there). The forge shell: its project picker, which signs a visitor in first.
  // The demo: nowhere to go, so the visitor stays on the console with the tour closed.
  finish: (shell) => (shell === "reliant" ? { to: "/forge" } : isDemoShell() ? null : { to: "/apps" }),
  steps: [
    {
      id: "fg-intro",
      title: "What is forge?",
      description: <WhatIsForge />,
      route: demo({ view: "overview", ...CLOSED }),
    },
    {
      id: "fg-console",
      title: "A forge app's console",
      description:
        "Every forge project gets this console: environments, releases, domains, secrets and observability, in one place. shop has an api, a worker and an inventory service in dev, staging and prod. The data here is made up.",
      // The console's sidebar (forge-ui SidebarLayout's nav, inside ConsoleShell).
      target: "[data-tour='forge-console'] #sidebar-layout-nav",
      tooltipPosition: "right",
      route: demo({ view: "overview", ...CLOSED }),
      // The demo host's short tour leaves it out: the console is there to explore, with a tip per view.
      isAvailable: notOnShortDemo,
    },
    {
      id: "fg-instrumented",
      title: "Monitoring from the start",
      description:
        "Forge gives your app monitoring from the start, so you can find problems without adding it by hand. Follow a slow or failed request to its logs and the code doing the work; your agent can read the same evidence.",
      target: "[data-testid='obs-overview']",
      route: demo({ view: "overview", ...CLOSED }),
      // No isAvailable on purpose: in every cut, the demo's included (owner, 2026-11-06). fg-built, at the end,
      // does not replace it; the short demo makes room by leaving out fg-agent.
    },
    {
      id: "fg-trace",
      title: "One request, end to end",
      description: "Find exactly where an error happens or time is spent in a user’s journey—even when the request crosses several services. Open the failed CreateOrder request to see its timeline; your agent reads the same evidence.",
      target: `[data-testid='obs-trace-row'][data-trace-id='${DEMO_TRACE_ID}']`,
      route: demo({ view: "traces", ...CLOSED }),
      advanceOn: { kind: "when", test: ({ location }) => location.search.trace === DEMO_TRACE_ID },
      actionHint: "Open the CreateOrder trace at the top",
      autoAdvance: true,
      signal: "trace_opened",
    },
    {
      id: "fg-hops",
      title: "Across services",
      description: <Hops cli="reliant forge" />,
      copyByShell: { forge: { description: <Hops cli="forge" /> } },
      target: `[data-testid='trace-span-row'][data-span-id='${DEMO_INVENTORY_SPAN_ID}']`,
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      advanceOn: { kind: "when", test: ({ location }) => location.search.span === DEMO_INVENTORY_SPAN_ID },
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
      // The demo host only: the instrumentation forge writes WITHIN a service.
      isAvailable: onDemo,
      target: `[data-testid='trace-span-row'][data-span-id='${DEMO_INSIDE_SPAN_IDS.method}']`,
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, issue: undefined }),
    },
    {
      id: "fg-logs",
      title: "What that request logged",
      description: <Logs cli="reliant forge" />,
      copyByShell: { forge: { description: <Logs cli="forge" /> } },
      target: "[data-testid='trace-logs']",
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
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
        test: ({ location }) => location.search.view === "logs" && location.search.trace === DEMO_TRACE_ID,
      },
      // Held: the next step is on another page, and the logs this opens are the point.
      autoAdvance: false,
      actionHint: "Click Logs for this trace",
      signal: "trace_logs_opened",
    },
    {
      id: "fg-profile",
      title: "Where the CPU went",
      description:
        "Profiles are linked to traces, so the flamegraph shows the CPU the api spent inside this request: each bar is a function, and wider means more time.",
      target: "[data-testid='trace-flamegraph']",
      // Back to the trace: the flamegraph button is on it.
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      advanceOn: { kind: "when", test: ({ location }) => location.search.view === "profiles" },
      actionHint: "Click Flamegraph",
      autoAdvance: true,
      signal: "flamegraph_opened",
      // The demo host's short tour leaves it out (DEMO_TOUR); the Profiles tip covers it.
      isAvailable: notOnShortDemo,
    },
    {
      id: "fg-graph",
      title: "Who calls whom",
      description:
        "Because every hop carries the trace, forge can draw the app from its spans. The browser calls the api; the api uses the database, the payment provider and a queue; the worker calls inventory. Nobody drew this map. Point at a line to see its requests and errors.",
      // The demo host's full tour only (DEMO_TOUR=full).
      isAvailable: onDemoFull,
      target: "[data-testid='graph-view']",
      route: demo({ view: "graph", ...CLOSED }),
    },
    {
      id: "fg-issue",
      title: "Errors, grouped by release",
      description: <Issue cli="reliant forge" />,
      copyByShell: { forge: { description: <Issue cli="forge" /> } },
      target: `[data-testid='obs-issue-${DEMO_ISSUE_ID}']`,
      route: demo({ view: "errors", ...CLOSED }),
      advanceOn: { kind: "when", test: ({ location }) => location.search.issue === DEMO_ISSUE_ID },
      actionHint: "Open CreateOrder: context deadline exceeded",
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
      route: demo({ view: "alerts", ...CLOSED }),
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
      title: "Hand it to an agent",
      description:
        "On your own app, Investigate in chat opens a new chat with this issue written up, and Fix with agent starts one on its own branch. This is sample data, so here it shows what the agent is given: ids and the commands to fetch the rest. It's never the log text itself.",
      copyByShell: {
        forge: {
          description:
            "This issue is a good one to hand to your own agent: Claude Code, Codex or Cursor. The prompt holds ids and forge commands, not log text, so your agent fetches the details with its own login.",
        },
      },
      route: demo({ view: "errors", issue: DEMO_ISSUE_ID, trace: undefined, span: undefined }),
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
    {
      id: "fg-built",
      title: "How this app was built",
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
      route: demo({ view: "overview", ...CLOSED }),
    },
    {
      id: "fg-done",
      title: "Try it on your app",
      description: <TryOnYourApp cli="reliant forge" />,
      copyByShell: { forge: { description: <TryOnYourApp cli="forge" /> } },
    },
  ],
};

/**
 * The "Explore a forge app" journey (journeys-spec.md §3).
 *
 * Nine steps through the forge demo console (`/forge/demo`,
 * components/Forge/Demo): a made-up app called shop, on fixture
 * observability data. It shows how forge instruments an app (a trace, its
 * logs, its CPU profile, the grouped error), then how an agent gets the
 * problem: an alert starting Autopilot on-call, and the Ask-an-agent handoff.
 *
 * The public demo host (demo.reliantlabs.io, lib/shell.ts `demo`) starts it on
 * every visit, so it runs a SHORTER, different cut there (DEMO_TOUR, default
 * `short`, at most ten steps): how the app was built (fg-built, the recorded
 * forge run, in place of fg-instrumented), no profile or on-call step, and
 * instead the instrumentation forge writes between services (fg-hops) and
 * inside one (fg-inside), and what a coding agent reads (fg-agent).
 * DEMO_TOUR=full adds the service graph (fg-graph) and puts the profile and
 * on-call steps back.
 *
 * It runs unchanged in both shells. Nothing here needs a daemon, a chat or a
 * signed-in user: every step's target is fixture data, every required action
 * is a URL change or a tour signal, and copy that differs (the CLI's name,
 * what Autopilot needs) is `copyByShell`.
 *
 * The ids below are the fixture's (Forge/Observability/__fixtures__); they are
 * repeated rather than imported so the fixtures stay out of the app's main
 * chunk. A test pins them to the fixture's.
 */

import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "../../Forge/Demo/demoPath";
import { trackEvent } from "../../../lib/analytics";
import { isForgeUIEnabled } from "../../../lib/forgeFeature";
import { isDemoShell } from "../../../lib/shell";
import { notOnDemo, notOnShortDemo, onDemo, onDemoFull } from "../../../lib/demoTour";
import { DemoCta } from "../../Forge/Demo/DemoCta";
import type { Journey, JourneyRoute, RouteTarget } from "../types";
import { TourCommands } from "./TourCommand";

/** The CreateOrder trace the tour opens (FIXTURE_TRACE_ID): two failed payment calls, then the worker. */
export const DEMO_TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";
/** The issue that trace belongs to: new in v1.8.3, its latest event is that trace. */
export const DEMO_ISSUE_ID = "iss-orders-deadline";
/** In that trace: inventory's server span, the hop the worker made over Connect (fg-hops). */
export const DEMO_INVENTORY_SPAN_ID = "e1";
/** In that trace: the api's CreateOrder method span, one SELECT, and the worker's consumer span (fg-inside). */
export const DEMO_INSIDE_SPAN_IDS = { method: "a2", query: "b1", worker: "d1" } as const;

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

const ONCALL_LEAD =
  "Alerts, and new or regressed issues, can start an agent. The Autopilot on-call workflow listens for them.";

const tryCommands = (cli: string) => [`${cli} obs status prod`, `${cli} obs issues prod`, `${cli} obs traces prod --errors`];
const scaffoldCommands = (cli: string) => [`${cli} scaffold service inventory`, `${cli} scaffold worker fulfilment`, `${cli} lint --tracing`];

function TryOnYourApp({ cli }: { cli: string }) {
  return (
    <>
      <p>Everything in this tour came from code forge writes. In your own project:</p>
      <TourCommands commands={scaffoldCommands(cli)} />
      <p className="mt-3">
        A new service comes with its traced handlers and method wrapper, and a worker&apos;s every run is a trace of its own. The lint
        finds where a trace would break. Then, on a deployed environment (use your own name):
      </p>
      <TourCommands commands={tryCommands(cli)} />
      <p className="mt-3">
        New to forge? <ExternalLink href={QUICK_START_URL}>Quick start</ExternalLink>
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

export const forgeJourney: Journey = {
  id: "forge",
  // v2: the demo console journey (v1 was the engine's two-step placeholder).
  version: 2,
  title: "Explore a forge app",
  description:
    "Walk through a running app's console: traces, logs and profiles, and how an alert hands a problem to an agent.",
  // Settings → Developer can switch the forge UI off; that turns its tour off too. (On by default, and in the forge shell.)
  isAvailable: isForgeUIEnabled,
  // Reliant: forge's Apps (the bare /forge lands there). The forge shell: its project picker, which signs a visitor in first.
  // The demo: nowhere to go, so the visitor stays on the console with the tour closed.
  finish: (shell) => (shell === "reliant" ? { to: "/forge" } : isDemoShell() ? null : { to: "/apps" }),
  steps: [
    {
      id: "fg-console",
      title: "A forge app's console",
      description:
        "This is the console for shop, a sample forge app with an api and a worker in dev, staging and prod. Every forge project gets this console with environments, releases, domains, secrets and observability. The data here is made up.",
      // The console's sidebar (forge-ui SidebarLayout's nav, inside ConsoleShell).
      target: "[data-tour='forge-console'] #sidebar-layout-nav",
      tooltipPosition: "right",
      route: demo({ view: "overview", ...CLOSED }),
    },
    {
      id: "fg-built",
      title: "How this app was built",
      description: (
        <>
          <p>
            This app&apos;s code started as one forge command and a few lines of proto. Press Play to watch the real run: forge made the
            project, then <code>forge scaffold</code> turned each proto message into a migration, an ORM, handlers, React hooks and tests.
          </p>
          <p className="mt-2">
            Nobody added tracing by hand. forge wired OpenTelemetry into the server, its Connect handlers and clients, the database driver
            and the browser, and wrote the dashboards and alerts. Below the terminal, every file it wrote is counted, and you can follow
            one entity from its proto to its test.
          </p>
        </>
      ),
      // The demo host only. It takes the place of fg-instrumented there: the same claim, with the files to back it.
      isAvailable: onDemo,
      target: "[data-testid='scaffold-replay']",
      tooltipPosition: "bottom",
      route: demo({ view: "overview", section: DEMO_SECTION_BUILT, ...CLOSED }),
    },
    {
      id: "fg-instrumented",
      title: "Instrumented from the start",
      description:
        "Nobody added tracing to this app by hand. forge writes the code at every boundary it owns: the Connect handlers and clients, a wrapper around each service package, and the database driver. It also ships helpers that carry a trace across NATS and Temporal. Each of these opens a span and passes the trace on, and every log line gets the trace id. Services also push a CPU profile about once a minute. The rate, errors and latency here come from those spans.",
      target: "[data-testid='obs-overview']",
      route: demo({ view: "overview", ...CLOSED }),
      // Not on the demo host: fg-built makes the same point there, from the recorded run.
      isAvailable: notOnDemo,
    },
    {
      id: "fg-trace",
      title: "One request, end to end",
      description: "Open the CreateOrder trace at the top. It's the one with an error.",
      route: demo({ view: "traces", ...CLOSED }),
      phases: [
        { target: `[data-testid='obs-trace-row'][data-trace-id='${DEMO_TRACE_ID}']` },
        {
          when: { kind: "element", selector: "[data-testid='trace-waterfall']" },
          target: "[data-testid='trace-waterfall']",
          description:
            "Each bar is a span. The call to the payment provider timed out once, was retried, and failed, which is most of the 182 ms. The order event then went over a queue to the worker, which called the inventory service. All of that is one trace.",
        },
      ],
      advanceOn: { kind: "when", test: ({ location }) => location.search.trace === DEMO_TRACE_ID },
      actionHint: "Open the CreateOrder trace",
      signal: "trace_opened",
    },
    {
      id: "fg-hops",
      title: "Across services",
      description: "This request crossed three services. Click Reserve on inventory, near the bottom.",
      // The demo host only: the instrumentation forge writes BETWEEN services.
      isAvailable: onDemo,
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      phases: [
        { target: `[data-testid='trace-span-row'][data-span-id='${DEMO_INVENTORY_SPAN_ID}']` },
        {
          when: { kind: "element", selector: "[data-testid='span-details']" },
          target: "[data-testid='span-details']",
          description:
            "The worker called inventory over Connect. forge's client added the trace context to the request headers, and inventory's server picked it up, so inventory's spans joined this trace with no tracing code in either service. The hop from the api to the worker went over NATS: forge's NATS helpers put the trace context in the message headers, and the worker carried on from it.",
        },
      ],
      advanceOn: { kind: "when", test: ({ location }) => location.search.span === DEMO_INVENTORY_SPAN_ID },
      actionHint: "Click Reserve on inventory",
      signal: "service_hop_opened",
    },
    {
      id: "fg-inside",
      title: "Inside a service",
      description: (
        <>
          <p>
            Inside the api, the RPC span holds a span for the CreateOrder method, one for each query, and one for the payment call. Each
            layer is a span because forge owns that boundary, not because someone remembered to add one.
          </p>
          <p className="mt-2">
            <code>forge lint --tracing</code> points out the places a trace would break, such as a log call without its context.
          </p>
        </>
      ),
      // The demo host only: the instrumentation forge writes WITHIN a service.
      isAvailable: onDemo,
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, issue: undefined }),
      targets: [
        { selector: `[data-testid='trace-span-row'][data-span-id='${DEMO_INSIDE_SPAN_IDS.method}']`, label: "A method", description: "forge wraps every service package, so each method call is a span." },
        { selector: `[data-testid='trace-span-row'][data-span-id='${DEMO_INSIDE_SPAN_IDS.query}']`, label: "A query", description: "The database driver makes a span for each statement, with the values replaced by ?." },
        { selector: `[data-testid='trace-span-row'][data-span-id='${DEMO_INSIDE_SPAN_IDS.worker}']`, label: "A worker", description: "Each message the worker handles continues the trace that sent it." },
      ],
    },
    {
      id: "fg-logs",
      title: "What that request logged",
      description:
        "Click Logs for this trace. Every log line carries its trace id, so you get exactly what this request logged across every service, without writing a search.",
      target: "[data-testid='trace-logs']",
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      phases: [
        {},
        {
          when: { kind: "element", selector: "[data-testid='obs-logs-trace-scope']" },
          target: "[data-testid='obs-logs-trace-scope']",
          description:
            "Five lines from three services: the order started, the first charge failed and was retried, the retry hit its deadline, the worker processed the order event, and inventory reserved the items.",
        },
      ],
      advanceOn: {
        kind: "when",
        test: ({ location }) => location.search.view === "logs" && location.search.trace === DEMO_TRACE_ID,
      },
      actionHint: "Click Logs for this trace",
      signal: "trace_logs_opened",
    },
    {
      id: "fg-profile",
      title: "Where the CPU went",
      description:
        "Click Flamegraph. Profiles are linked to traces, so this is the CPU the api spent inside that request. Each bar is a function, and wider means more time.",
      target: "[data-testid='trace-flamegraph']",
      // Back to the trace: the flamegraph button is on it.
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      phases: [
        {},
        { when: { kind: "element", selector: "[data-testid='obs-profiles']" }, target: "[data-testid='obs-profiles']" },
      ],
      advanceOn: { kind: "when", test: ({ location }) => location.search.view === "profiles" },
      actionHint: "Click Flamegraph",
      signal: "flamegraph_opened",
      // The demo host's short tour leaves it out (DEMO_TOUR).
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
      title: "Errors, grouped",
      description: "forge groups errors into issues. Open CreateOrder: context deadline exceeded.",
      route: demo({ view: "errors", ...CLOSED }),
      phases: [
        { target: `[data-testid='obs-issue-${DEMO_ISSUE_ID}']` },
        {
          when: { kind: "element", selector: "[data-testid='obs-issue-header']" },
          target: "[data-testid='obs-issue-header']",
          description:
            "It's new in release v1.8.3. Its latest event links back to the trace you just read, and the CLI can list the commits in that release that touched the failing code.",
        },
      ],
      advanceOn: { kind: "when", test: ({ location }) => location.search.issue === DEMO_ISSUE_ID },
      actionHint: "Open the CreateOrder issue",
      signal: "issue_opened",
    },
    {
      id: "fg-oncall",
      title: "How an agent gets called",
      description: (
        <>
          <p>
            {ONCALL_LEAD} Enable it once and every alert becomes a GitHub issue that an agent investigates read-only and
            writes up with the evidence and a likely fix. It never marks the work ready; a person does.
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
                {ONCALL_LEAD} Enable it once and every alert becomes a GitHub issue that an agent investigates. Autopilot
                runs in Reliant. Without it, you can hand any alert or issue to your own agent, which is the next step.
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
      // The demo host's short tour leaves it out (DEMO_TOUR).
      isAvailable: notOnShortDemo,
    },
    {
      id: "fg-agent",
      title: "What your agent reads",
      description: <WhatYourAgentReads cli="reliant forge" />,
      copyByShell: { forge: { description: <WhatYourAgentReads cli="forge" /> } },
      // The demo host only: how forge helps the model.
      isAvailable: onDemo,
    },
    {
      id: "fg-ask",
      title: "Ask an agent about it",
      description:
        "On your own app, Investigate in chat opens a new chat with this issue written up, and Fix with agent starts one on its own branch. This is sample data, so here it shows what the agent is given: ids and the commands to fetch the rest. It's never the log text itself.",
      copyByShell: {
        forge: {
          description:
            "Hand it to your own agent. Copy the prompt into Claude Code, Codex or Cursor. It holds ids and forge commands, not log text, so your agent fetches the details with its own login.",
        },
      },
      route: demo({ view: "errors", issue: DEMO_ISSUE_ID, trace: undefined, span: undefined }),
      phases: [
        { target: "[data-testid='ask-reliant-actions']" },
        { when: { kind: "element", selector: "[data-testid='ask-agent-sheet']" }, target: "[data-testid='ask-agent-sheet']" },
      ],
      advanceOn: { kind: "event", event: "askAgent:opened" },
      afterAction: "Copy something to try it.",
      actionHint: "Click Ask an agent",
      signal: "ask_agent_opened",
    },
    {
      id: "fg-done",
      title: "Try it on your app",
      description: <TryOnYourApp cli="reliant forge" />,
      copyByShell: { forge: { description: <TryOnYourApp cli="forge" /> } },
    },
  ],
};

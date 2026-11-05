/**
 * The "Explore a forge app" journey (journeys-spec.md §3).
 *
 * Nine steps through the forge demo console (`/forge/demo`,
 * components/Forge/Demo): a made-up app called shop, on fixture
 * observability data. It shows how forge instruments an app (a trace, its
 * logs, its CPU profile, the grouped error), then how an agent gets the
 * problem: an alert starting Autopilot on-call, and the Ask-an-agent handoff.
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

import { FORGE_DEMO_PATH } from "../../Forge/Demo/demoPath";
import { trackEvent } from "../../../lib/analytics";
import { isForgeUIEnabled } from "../../../lib/forgeFeature";
import { isDemoShell } from "../../../lib/shell";
import type { Journey, JourneyRoute, RouteTarget } from "../types";
import { TourCommands } from "./TourCommand";

/** The CreateOrder trace the tour opens (FIXTURE_TRACE_ID): two failed payment calls, then the worker. */
export const DEMO_TRACE_ID = "4bf92f3577b34da6a3ce929d0e0e4736";
/** The issue that trace belongs to: new in v1.8.3, its latest event is that trace. */
export const DEMO_ISSUE_ID = "iss-orders-deadline";

const AUTOPILOT_PATH = "/workflows/library/builtin%3A%2F%2Fautopilot-oncall";
const RELIANT_APP_URL = "https://app.reliantlabs.io";
const QUICK_START_URL = "https://docs.reliantlabs.io/forge/quick-start";

/** A step on the demo console, opening it with `search` (merged when already there). */
function demo(search: Record<string, unknown>): JourneyRoute {
  const open: RouteTarget = { to: FORGE_DEMO_PATH, search };
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

function TryOnYourApp({ cli }: { cli: string }) {
  return (
    <>
      <p>In a forge project, these show what&apos;s wrong right now, the grouped errors, and one trace. Use your own environment name.</p>
      <TourCommands commands={tryCommands(cli)} />
      <p className="mt-3">
        New to forge? <ExternalLink href={QUICK_START_URL}>Quick start</ExternalLink>
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
      id: "fg-instrumented",
      title: "Instrumented from the start",
      description:
        "Nobody added tracing to this app by hand. forge's generated handlers and its observe package send a span for every RPC, database query and queue message, and put the trace id on every log line. Services also push a CPU profile about once a minute. The rate, errors and latency here come from those spans.",
      target: "[data-testid='obs-overview']",
      route: demo({ view: "overview", ...CLOSED }),
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
            "Each bar is a span. The call to the payment provider timed out once, was retried, and failed, which is most of the 182 ms. The order event then went over a queue to the worker, and that work is part of the same trace.",
        },
      ],
      advanceOn: { kind: "when", test: ({ location }) => location.search.trace === DEMO_TRACE_ID },
      actionHint: "Open the CreateOrder trace",
      signal: "trace_opened",
    },
    {
      id: "fg-logs",
      title: "What that request logged",
      description:
        "Click Logs for this trace. Every log line carries its trace id, so you get exactly what this request logged across both services, without writing a search.",
      target: "[data-testid='trace-logs']",
      route: demo({ view: "traces", trace: DEMO_TRACE_ID, span: undefined, issue: undefined }),
      phases: [
        {},
        {
          when: { kind: "element", selector: "[data-testid='obs-logs-trace-scope']" },
          target: "[data-testid='obs-logs-trace-scope']",
          description:
            "Four lines from two services: the order started, the first charge failed and was retried, the retry hit its deadline, and the worker still processed the order event.",
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

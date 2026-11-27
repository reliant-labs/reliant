// Copyright (c) 2025 Reliant Labs

/** One brief explanation per page. Commands and secondary detail stay behind Agent help. */
import type { DemoTipKey } from "./tipKeys";

export interface DemoTip {
  title: string;
  body: string;
  help: string;
  command: string;
  /** A single anchor for the optional explanation of this page, not a sequence of local stops. */
  target?: string;
}

const tid = (id: string) => `[data-testid='${id}']`;

export const DEMO_TIPS: Record<DemoTipKey, DemoTip> = {
  metrics: {
    title: "Metrics",
    body: "Compare traffic, failures and response times to see whether your app is getting better or worse. Choose a measurement and time range to investigate a change.",
    help: "Your agent can query the same measurements alongside recent changes.",
    command: "forge obs status prod",
  },
  rpc: {
    title: "RPC explorer",
    body: "Find the operations that are slow or failing most often. Open one to follow its requests and focus your investigation on the right part of the app.",
    help: "Your agent can find failed requests for the same service and inspect their details.",
    command: "forge obs traces prod --errors",
  },
  graph: {
    title: "Service graph",
    body: "See which parts of your app depend on one another, so a failure is easier to follow. Select a service or connection to inspect the work passing through it.",
    help: "Your agent can follow a request across the same services to locate the problem.",
    command: "forge obs traces prod --errors",
  },
  workload: {
    title: "One service or job",
    body: "Check whether this part of your app is healthy and which version it runs. Use its logs, requests and history to understand a failure before changing anything.",
    help: "Your agent can combine service health with logs and recent changes.",
    command: "forge obs status prod",
  },
  overview: {
    title: "Overview",
    body: "Forge builds, deploys and monitors your app, so what changed and whether it is healthy are on one page.",
    help: "Your agent can read the same health and release information to decide what to investigate next.",
    command: "forge obs status prod",
  },
  releases: {
    title: "Releases",
    body: "See what shipped and where it is running. Open a release to compare its changes and health before promoting the same tested version to another environment.",
    help: "Your agent can inspect a release and check whether a deployment made things better or worse.",
    command: "forge env releases list prod",
    target: tid("releases-table"),
  },
  release: {
    title: "One release",
    body: "See the changes in this version and which environments received it. This helps you connect a problem to changed code, rather than guessing what shipped.",
    help: "Your agent reads the same commits and deployment history before proposing a change.",
    command: "forge env releases show prod v1.8.3",
    target: tid("release-commits-panel"),
  },
  environments: {
    title: "Environments",
    body: "Environments are separate places to develop, test and run your app for users. Compare dev, staging and production to see what is running and whether it matches the project's configuration.",
    help: "Ask your agent to compare environments or investigate a failed deployment. It reads the same versions, configuration and health information.",
    command: "forge env list",
    target: tid("forge-env-list"),
  },
  environment: {
    title: "One environment",
    body: "See which version and services run here, and whether they are healthy. Open a service or deployment to investigate what needs attention.",
    help: "Your agent can read the release, health and deployment status without needing to navigate this page.",
    command: "forge env status prod",
    target: tid("env-page-header"),
  },
  secrets: {
    title: "Secrets",
    body: "Check which environments have the credentials your app needs, without exposing their values. Open a name to inspect its versions and find missing configuration.",
    help: "Your agent can check for missing secrets by name without reading their values.",
    command: "forge env secret list --env prod",
    target: tid("managed-secrets"),
  },
  domains: {
    title: "Domains",
    body: "See which web addresses reach your app and whether their certificates are ready. Open a domain to check what is still needed before visitors can use it.",
    help: "Your agent can inspect the required DNS records and verification status.",
    command: "forge cloud domain show <host> --env prod",
    target: tid("domain-row-shop.example.com"),
  },
  activity: {
    title: "Activity",
    body: "See who changed what and when, including releases and configuration. Select an event to connect a problem to the change that came before it.",
    help: "Your agent can start an investigation by checking recent changes.",
    command: "forge obs changes prod --since 6h",
    target: tid("activity-stream-feed"),
  },
  auth: {
    title: "Auth",
    body: "See who can sign in to your app in each environment: their state, their roles and when they last signed in. Reliant runs the sign-in service, so there is no auth server to host.",
    help: "Your agent can list the same users and roles, and check the sign-in service is ready after a deploy.",
    command: "forge cloud identity users prod",
    target: tid("identity-users-panel"),
  },
  services: {
    title: "Services",
    body: "Everything your app runs in this environment, live: each service, worker, job and the static site, with its status, CPU, memory, requests and the release it runs. Open one for its logs, requests and history.",
    help: "Your agent can read the same health, restarts and failing jobs before deciding what to look at.",
    command: "forge obs workloads prod",
  },
  storage: {
    title: "Storage",
    body: "Browse your app's object storage buckets: uploads, files and assets, per environment. In production, reading files asks for a reason first, and every read is recorded for your team to see.",
    help: "Your agent can check what each environment declares before changing how the app stores files.",
    command: "forge env shape prod",
  },
  data: {
    title: "Data",
    body: "Browse the rows in your app's database, table by table, with filters and sorting. In production, reading asks for a reason first and is recorded, so looking at customer data always leaves a trail.",
    help: "Your agent can run one read-only query against an environment's database, with its reason recorded.",
    command: "forge db query prod \"select count(*) from orders\" --confirm-env prod --reason \"<why>\"",
  },
  vulnerabilities: {
    title: "Vulnerabilities",
    body: "Known vulnerabilities in the code each environment runs, found in the software list recorded with every release. The most severe come first, with whether a fix exists and whether the environment's policy would block a release over it.",
    help: "Your agent can list the same findings, bump each fixable dependency to its fixed version, and check the result.",
    command: "forge vuln list prod",
  },
  sboms: {
    title: "SBOMs",
    body: "The software bill of materials recorded for each part of a release: every package it contains, and its version. Vulnerability scans read these lists, so a part without one is never reported as clean.",
    help: "Your agent can print one artifact's SBOM to check which version of a package shipped.",
    command: "forge vuln sbom prod --artifact api",
  },
  tests: {
    title: "Tests",
    body: "How your test suite is doing on the main branch: coverage over time with releases marked, the pass rate, flaky tests and the last run. Test runs are recorded per commit, so they show before anything is deployed.",
    help: "Your agent can summarise a commit's recorded runs and coverage before it changes code.",
    command: "forge test report",
  },
  "tests-coverage": {
    title: "Coverage",
    body: "Which code your tests run, by package and by file, merged across your test lanes. Open a file to see which of its lines no test reaches.",
    help: "Your agent can read the same coverage for a path before it writes the missing tests.",
    command: "forge test coverage",
  },
  "tests-runs": {
    title: "Runs",
    body: "Every recorded test run, newest first, from CI or a laptop: its result, how long it took and what failed. Open a run for its failures and slowest tests.",
    help: "Your agent reads a failed run's failures before deciding whether it is a real break or a flaky test.",
    command: "forge test report",
  },
  "tests-flaky": {
    title: "Flaky tests",
    body: "Tests that failed and then passed on the same code. Each shows how it was caught and where it stands, from suspected to quarantined to fixed, so a flaky test is tracked instead of re-run until it passes.",
    help: "Your agent can rerun a red run's failing tests once to tell a flaky test from a break, and quarantine a confirmed one.",
    command: "forge test flakes",
  },
  analytics: {
    title: "Analytics",
    body: "Who your users are, what they do and where they come from: visitors, sign-ups, active users and the figures that moved, from the events your app sends.",
    help: "Your agent can read active, new and returning users from the same events.",
    command: "forge obs users prod",
  },
  events: {
    title: "Events & metrics",
    body: "The product events your app sends, and the numbers your services report. Open a metric to chart it over time.",
    help: "Your agent can list and query the same metrics.",
    command: "forge obs metrics list prod",
  },
  experiments: {
    title: "Experiments",
    body: "Change what your app does without a deploy: one version for everyone, a share of users that you raise as you gain confidence, or two versions compared on one goal. In prod, the payment retry behind the checkout incident was switched off minutes ago, and express checkout is winning its test.",
    help: "Your agent can turn a change off, raise a rollout or read a test's results through the same API, and every action it takes lands in the change's history.",
    command: "forge generate",
    target: tid("change-row-express-checkout"),
  },
  "obs-overview": {
    title: "Observability",
    body: "Every service's traffic, failures and response times, the top errors, the slowest operations and the alerts firing, on one page. Open any of them to follow it into requests and logs.",
    help: "Your agent can ask what is wrong right now and read the same services, issues and alerts.",
    command: "forge obs status prod",
  },
  dashboards: {
    title: "Dashboards",
    body: "Spot changes in traffic, failures and response times before they become bigger problems. Open a dashboard to compare services or releases over time.",
    help: "Your agent can query the same measurements and investigate an empty or unexpected panel.",
    command: "forge obs dashboards audit prod",
    target: tid("dashboards-list"),
  },
  traces: {
    title: "Traces",
    body: "Find exactly where an error happens or time is spent in a user’s journey—even when the request crosses several services. Open a request below to see its timeline; your agent can read the same evidence to investigate.",
    help: "Choose a failed or slow request, then open its timeline and related logs. Your agent uses the request’s trace ID to read the same timeline, including the slowest part.",
    command: "forge obs traces prod --errors",
    target: tid("obs-trace-row"),
  },
  trace: {
    title: "One request, end to end",
    body: "Follow the work behind one request to find the slow or failing part. Select a row for its details, or open its logs to understand what happened.",
    help: "Your agent reads this same request timeline by trace ID, so you do not need to copy screenshots or log text.",
    command: "forge obs trace <id> prod",
    target: tid("trace-waterfall"),
  },
  logs: {
    title: "Logs",
    body: "Read what your app recorded around a problem, without searching each service separately. Search or select a line, then follow its request to see the surrounding context.",
    help: "Your agent searches the same logs and can narrow them to one request with --trace <id>.",
    command: "forge obs logs prod 'service:api severity:error'",
    target: tid("obs-log-row"),
  },
  profiles: {
    title: "Profiles",
    body: "Find code that uses the most time or memory, so you can focus an optimization where it matters. Choose a service and profile; wider bars show where more work happened.",
    help: "Your agent can export the same profile and inspect it before proposing an optimization.",
    command: "forge obs profile --service api",
    target: tid("obs-profiles"),
  },
  errors: {
    title: "Errors",
    body: "Group repeated failures and see how many users are affected, with links to the request and release. Because Forge manages releases, it can tie a first occurrence or regression to a release and help your agent focus on changed code. Open an issue to investigate.",
    help: "Your agent reads the grouped failure, affected users, related trace and release history before proposing a fix.",
    command: "forge obs issue <id> prod",
    target: tid("obs-issue-iss-orders-deadline"),
  },
  alerts: {
    title: "Alerts",
    body: "Alerts tell you when your app needs attention, by text, email or Slack. Open one to see the evidence behind it.",
    help: "An alert can also start a Reliant workflow that investigates and proposes a fix, with the approvals you choose.",
    command: "forge obs status prod",
    target: tid("obs-alert-CheckoutP95High"),
  },
  "site-insights": {
    title: "Site insights",
    body: "How fast each page of your site loads and responds for real visitors, on desktop and mobile. The worst-scoring pages come first, so you know where speeding things up helps people most.",
    help: "Your agent can query the same page-load measurements before it changes a slow page.",
    command: "forge obs query prod --sql \"<select>\"",
    target: tid("speed-insights-routes"),
  },
  "site-insights-seo": {
    title: "SEO",
    body: "After every release, each page of your site is checked the way search engines and link previews read it. The worst pages come first: open one for what to fix and how its link looks in Slack, X and LinkedIn.",
    help: "Your agent can audit the built site locally before the next release, with the same rules.",
    command: "forge site audit dist",
    target: tid("site-insights-seo-routes"),
  },
  sessions: {
    title: "Sessions",
    body: "Understand what a visitor experienced before something went wrong. Open a session to follow its pages, actions and errors alongside the requests behind them.",
    help: "Your agent can read the same session as a timeline and follow its related requests.",
    command: "forge obs sessions prod",
    target: tid("obs-sessions-list"),
  },
};

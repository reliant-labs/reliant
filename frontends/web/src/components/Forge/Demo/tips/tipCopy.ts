// Copyright (c) 2025 Reliant Labs

/**
 * The demo's page tours, one per console page (DemoTips): a card saying what
 * the page is, how it ties to the rest of forge (it scaffolds, deploys,
 * releases and observes the same app), what a coding agent does with it and
 * the command it runs — then, on "Show me around", 2–4 callouts on the page's
 * key parts.
 *
 * Every command is checked against `forge <cmd> --help`. Plain data, so a
 * test reads every word (DemoTipsAndToast.test.tsx). Every callout's selector
 * is a `data-testid` the page renders, found on the demo site; a renamed
 * testid only drops its stop (DemoTips leaves out a stop with no target), so
 * re-probe the pages when one changes.
 */

import type { DemoTipKey } from "./tipKeys";

export interface PageCallout {
  /** The element to point at. Several selectors: whichever the page renders (the first in document order). */
  selector: string;
  title: string;
  body: string;
}

export interface DemoTip {
  title: string;
  body: string;
  /** "Your agent …": the sentence around the command. */
  agent: { before: string; command: string; after: string };
  /** The tour's stops, in order. A stop whose target the page doesn't render is left out. */
  callouts: PageCallout[];
}

const tid = (...ids: string[]) => ids.map((id) => `[data-testid='${id}']`).join(", ");

export const DEMO_TIPS: Record<DemoTipKey, DemoTip> = {
  overview: {
    title: "The app at a glance",
    body: "Every environment, its health and what changed, on one page. The rate, errors and latency come from the spans forge's generated handlers emit: nobody wrote the instrumentation by hand.",
    agent: { before: "Your agent asks the same question with", command: "forge obs status prod", after: ": what is wrong right now, and which release is in force." },
    callouts: [
      { selector: tid("overview-env-strip", "obs-overview"), title: "Every environment", body: "dev, staging and prod, and the release each one runs. forge deploys them, so it knows." },
      { selector: tid("observability-strip"), title: "Health from the spans", body: "Requests, errors and p95 latency, from the traces every service sends without a line of tracing code." },
      { selector: tid("overview-attention"), title: "What needs you", body: "Firing alerts, crash-looping workloads and failing jobs, each with the rollback forge can plan." },
      { selector: tid("overview-recent-releases"), title: "What changed", body: "The newest releases. Because forge cuts them, it can line any regression up with the release that brought it." },
    ],
  },
  releases: {
    title: "Releases",
    body: "forge builds a release once and promotes the same artifacts from dev to staging to prod. Because it also collects the telemetry, it can judge each release against the one before it.",
    agent: { before: "Your agent reads the timeline with", command: "forge env releases list prod", after: ", and after a deploy runs forge obs check prod --wait --json." },
    callouts: [
      { selector: tid("releases-pipeline"), title: "Where each release is", body: "What dev, staging and prod run right now, and how far ahead dev is." },
      { selector: tid("releases-table"), title: "Every release", body: "Its commits, its checks, and every environment it ran in." },
      { selector: tid("releases-deploys"), title: "Every deploy", body: "Each forge env deploy, from a terminal, CI or an agent, with its output, including the ones that failed." },
    ],
  },
  release: {
    title: "One release",
    body: "Exactly what shipped: the commits, the images and their digests, and the environments it reached. Built once, so prod runs the bytes staging tested.",
    agent: { before: "Your agent reads one release in full with", command: "forge env releases show prod v1.8.3", after: ": commits, what changed, and its gates." },
    callouts: [
      { selector: tid("release-env-picker", "release-promote-blocked"), title: "Promote it", body: "forge plans a promotion before anything applies. In the demo, changes are off." },
      { selector: tid("release-commits-panel", "release-changes"), title: "What's in it", body: "The commits since the release before, so a regression has a short suspect list." },
      { selector: tid("release-timeline", "release-history"), title: "Where it went", body: "Every promotion of this release, by whom, and how its rollout went." },
    ],
  },
  environments: {
    title: "Environments",
    body: "dev, staging and prod are declared in the repo, so forge knows what each one should be running and can show where it drifted.",
    agent: { before: "Your agent lists them with", command: "forge env list", after: ", and reads one with forge env status prod." },
    callouts: [
      { selector: tid("env-row-prod", "forge-env-list"), title: "One row per environment", body: "Its release, its health and its last deploy." },
      { selector: tid("obs-check-prod"), title: "Was the deploy good?", body: "forge compares the window after a promotion with the window before it, per service." },
      { selector: tid("drift-prod"), title: "Drift", body: "Whether what runs matches what the repo declares." },
    ],
  },
  environment: {
    title: "One environment",
    body: "What it runs, whether it's healthy, and how to change it: deploys and promotions, its workloads, domains and secrets, all declared in the repo.",
    agent: { before: "Your agent reads it with", command: "forge env status prod", after: ": the release it runs, the rollout, health and gates." },
    callouts: [
      { selector: tid("env-page-header"), title: "Release and health", body: "The release in force and the environment's verdict." },
      { selector: tid("live-section", "env-state"), title: "Declared vs observed", body: "What the repo declares, and what the platform reports running." },
      { selector: tid("env-domains", "env-overview-domains"), title: "Its domains", body: "Every hostname bound to this environment, with its certificate state." },
    ],
  },
  secrets: {
    title: "Secrets",
    body: "Each secret the app declares, and which environments have a value for it. Values never leave the store: the console and the CLI only ever show names.",
    agent: { before: "Your agent runs", command: "forge secret list --env prod", after: " to see which declared secrets are missing a value, without reading any." },
    callouts: [
      { selector: tid("secret-row-DATABASE_URL", "managed-secrets"), title: "Names, never values", body: "Its versions and who set them. The value stays in the store." },
      { selector: tid("secrets-protected-badge"), title: "Protected", body: "Changing a prod secret asks you to prove it's you first." },
      { selector: tid("add-secret"), title: "Add one", body: "Or with forge secret set. In the demo, changes are off." },
    ],
  },
  domains: {
    title: "Domains",
    body: "A domain is claimed once for your organization and bound to one environment. forge prints the DNS records to publish, checks them, and then issues the certificate.",
    agent: { before: "Your agent runs", command: "forge domain show <host> --env prod", after: " for the exact records, and where verification is stuck." },
    callouts: [
      { selector: tid("domain-row-shop.example.com"), title: "A live domain", body: "Bound to prod's storefront, its certificate issued." },
      { selector: tid("domain-row-staging.shop.example.com"), title: "One still verifying", body: "forge checks its DNS records until they resolve, then issues the certificate." },
    ],
  },
  activity: {
    title: "Activity",
    body: "Everything that changed: promotions, rollbacks, secret rotations and triage, by person, CI or agent. forge records it because forge did it.",
    agent: { before: "Your agent asks what changed with", command: "forge obs changes prod --since 6h", after: ", the first question in any incident." },
    callouts: [
      { selector: tid("activity-stream-feed", "activity-stream"), title: "Newest first", body: "Who did what, where, and through which surface." },
      { selector: tid("activity-facts"), title: "The detail", body: "The facts of the change you picked." },
    ],
  },
  dashboards: {
    title: "Dashboards",
    body: "Each release carries its dashboards, made from the services and RPCs forge scaffolded, so a new endpoint has its panels the day it ships.",
    agent: { before: "Your agent runs", command: "forge obs dashboards audit prod", after: " to replay every panel as the console does, and say why an empty one is empty." },
    callouts: [
      { selector: tid("observability-services"), title: "Every service", body: "Its requests, errors and latency, from the spans forge's handlers emit." },
      { selector: tid("hub-groups"), title: "Generated dashboards", body: "Services, jobs, the product funnel and the platform, each from what the app declares." },
      { selector: tid("hub-releases"), title: "By release", body: "Each release's own dashboards, so you can compare before and after." },
    ],
  },
  errors: {
    title: "Issues",
    body: "Errors are grouped into issues. Because forge also ships your releases, it knows which release each issue first appeared in, and which commits in that release touched the failing code.",
    agent: { before: "Your agent runs", command: "forge obs issue <id> prod", after: " for the latest event, the release it first appeared in, and the suspect commits." },
    callouts: [
      { selector: tid("obs-errors-status"), title: "By status", body: "New, regressed, resolved: an issue's state survives log retention." },
      { selector: tid("obs-issue-iss-orders-deadline", "obs-errors-list"), title: "One issue", body: "How often, how many traces, and the release it first appeared in." },
    ],
  },
  traces: {
    title: "Traces",
    body: "Every request is a trace. forge's Connect clients and servers and its NATS helpers carry it from service to service. Open one with an error to see where its time went.",
    agent: { before: "Your agent finds the failing ones with", command: "forge obs traces prod --errors", after: "." },
    callouts: [
      { selector: tid("obs-trace-latency", "obs-trace-red"), title: "Latency at a glance", body: "Every request in the window by duration: a fast middle, a long tail, and the outliers." },
      { selector: tid("obs-errors-only"), title: "Just the failures", body: "Most of them are on the payments path since v1.8.3." },
      { selector: tid("obs-trace-table"), title: "Every request", body: "Its route, services, duration and errors. Open one for its waterfall." },
    ],
  },
  trace: {
    title: "One trace",
    body: "Each bar is a span, indented under whatever called it. Click a span for its attributes, or Logs for every line this request wrote.",
    agent: { before: "Your agent runs", command: "forge obs trace <id> prod", after: " and reads the same waterfall as a tree, with the slowest path marked." },
    callouts: [
      { selector: tid("trace-waterfall"), title: "The waterfall", body: "One request across services: queues and Connect hops included." },
      { selector: tid("trace-logs"), title: "Its logs", body: "Every line this request wrote, in every service, by trace id." },
      { selector: tid("trace-flamegraph"), title: "Its CPU", body: "Profiles are linked to traces: the code behind a slow span." },
    ],
  },
  logs: {
    title: "Logs",
    body: "forge's logger stamps each line with its service and trace id, so any line leads back to the request that wrote it.",
    agent: { before: "Your agent searches the same logs with", command: "forge obs logs prod 'service:api severity:error'", after: ", or one trace's with --trace <id>." },
    callouts: [
      { selector: tid("obs-log-query"), title: "Search", body: "Words, fields, OR and NOT: the same query language the CLI takes." },
      { selector: tid("obs-severity-error"), title: "By level", body: "The error bursts line up with the payments issue." },
      { selector: tid("obs-logs-table"), title: "Every line", body: "Open one for its trace, its pod and its attributes." },
    ],
  },
  profiles: {
    title: "Profiles",
    body: "Every service forge scaffolds pushes a CPU profile about once a minute, linked to its traces, so you can see the code behind a slow span. Wider bars mean more time.",
    agent: { before: "Your agent exports the same profile with", command: "forge obs profile --service api", after: " and reads it with go tool pprof." },
    callouts: [
      { selector: tid("obs-profile-service"), title: "Pick a service", body: "api, worker and inventory each have their own profile." },
      { selector: tid("obs-profile-type"), title: "CPU, memory, goroutines", body: "The same profile, by what it samples." },
      { selector: tid("obs-profiles"), title: "The flamegraph", body: "Each bar is a function; wider means more time." },
    ],
  },
  alerts: {
    title: "Alerts",
    body: "Each environment's alert rules are declared with the app and deploy with it. Because forge also does the deploys, it can line an alert up with what changed just before it fired.",
    agent: { before: "Your agent sees what is firing with", command: "forge obs status prod", after: ", and what changed just before with forge obs changes prod." },
    callouts: [
      { selector: tid("obs-alerts-firing"), title: "Firing now", body: "Each with its value, its labels, and a way to hand it to an agent." },
      { selector: tid("obs-alerts-tab-history"), title: "History", body: "When each alert fired and resolved." },
      { selector: tid("obs-alerts-tab-rules"), title: "Rules from the repo", body: "Declared in the app's KCL, so they ship with it." },
    ],
  },
  sessions: {
    title: "Sessions",
    body: "Real users' browser sessions: the pages they saw, what they clicked and the errors they hit, joined to the backend traces their requests made.",
    agent: { before: "Your agent lists the sessions that hit errors with", command: "forge obs sessions prod", after: ", and reads one as a text timeline with forge obs session <id> prod." },
    callouts: [
      { selector: tid("obs-sessions-with-errors"), title: "Just the broken ones", body: "Sessions that hit an error, like the checkout failures since v1.8.3." },
      { selector: tid("obs-sessions-list"), title: "Every session", body: "Its pages, its device and the build it ran. Open one for the replay." },
    ],
  },
};

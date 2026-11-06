// Copyright (c) 2025 Reliant Labs

/**
 * The demo's one-time tips, one per console view (DemoTips). Each says what
 * the view is in a sentence or two, connects it to the rest of what forge
 * does (it scaffolds, deploys, releases and observes the same app), and ends
 * with what a coding agent does with it, and the command it runs.
 *
 * Every command is checked against `forge <cmd> --help`. Plain strings, so a
 * test can read every word (tipCopy.test.ts).
 */

import type { DemoTipKey } from "./tipKeys";

export interface DemoTip {
  title: string;
  body: string;
  /** "Your agent …": the sentence around the command. */
  agent: { before: string; command: string; after: string };
}

export const DEMO_TIPS: Record<DemoTipKey, DemoTip> = {
  overview: {
    title: "The app at a glance",
    body: "Rate, errors and latency for every service, from the spans forge's generated handlers emit. Nobody wrote the instrumentation by hand.",
    agent: { before: "Your agent asks the same question with", command: "forge obs status prod", after: ": what is wrong right now, and which release is in force." },
  },
  traces: {
    title: "Traces",
    body: "Every request is a trace. forge's Connect clients and servers and its NATS helpers carry it from service to service. Open one with an error to see where its time went.",
    agent: { before: "Your agent finds the failing ones with", command: "forge obs traces prod --errors", after: "." },
  },
  trace: {
    title: "One trace",
    body: "Each bar is a span, indented under whatever called it. Click a span for its attributes, or Logs for every line this request wrote.",
    agent: {
      before: "Your agent runs",
      command: "forge obs trace <id> prod",
      after: " and reads the same waterfall as a tree, with the slowest path marked and time no span covers called out.",
    },
  },
  logs: {
    title: "Logs",
    body: "forge's logger stamps each line with its service and trace id, so any line leads back to the request that wrote it.",
    agent: { before: "Your agent searches the same logs with", command: "forge obs logs prod 'service:api severity:error'", after: ", or one trace's with --trace <id>." },
  },
  profiles: {
    title: "Profiles",
    body: "Every service forge scaffolds pushes a CPU profile about once a minute, linked to its traces, so you can see the code behind a slow span. Wider bars mean more time.",
    agent: { before: "Your agent exports the same profile with", command: "forge obs profile --service api", after: " and reads it with go tool pprof." },
  },
  errors: {
    title: "Issues",
    body: "Errors are grouped into issues. Because forge also ships your releases, it knows which release each issue first appeared in, and which commits in that release touched the failing code.",
    agent: { before: "Your agent runs", command: "forge obs issue <id> prod", after: " for the latest event, the release it first appeared in, and the suspect commits." },
  },
  releases: {
    title: "Releases",
    body: "forge builds a release once and promotes the same artifacts from dev to staging to prod. Because it also collects the telemetry, it can judge each release against the one before it.",
    agent: { before: "After a deploy your agent runs", command: "forge obs check prod --wait --json", after: " and acts on the verdict: pass, fail or not enough data yet." },
  },
  environments: {
    title: "Environments",
    body: "dev, staging and prod are declared in the repo, so forge knows what each one should be running and can show where it drifted.",
    agent: { before: "Your agent reads one environment with", command: "forge env status prod", after: ": the release it runs, the rollout, health and gates." },
  },
  secrets: {
    title: "Secrets",
    body: "Each secret the app declares, and which environments have a value for it. Values never leave the store: the console and the CLI only ever show names.",
    agent: { before: "Your agent runs", command: "forge secret list --env prod", after: " to see which declared secrets are missing a value, without reading any." },
  },
  domains: {
    title: "Domains",
    body: "A domain is claimed once for your organization and bound to one environment. forge prints the DNS records to publish, checks them, and then issues the certificate.",
    agent: { before: "Your agent runs", command: "forge domain show <host> --env prod", after: " for the exact records, and where verification is stuck." },
  },
  dashboards: {
    title: "Dashboards",
    body: "Each release carries its dashboards, made from the services and RPCs forge scaffolded, so a new endpoint has its panels the day it ships.",
    agent: { before: "Your agent runs", command: "forge obs dashboards audit prod", after: " to replay every panel as the console does, and say why an empty one is empty." },
  },
  alerts: {
    title: "Alerts",
    body: "Each environment's alert rules are declared with the app and deploy with it. Because forge also does the deploys, it can line an alert up with what changed just before it fired.",
    agent: { before: "Your agent sees what is firing with", command: "forge obs status prod", after: ", and what changed just before with forge obs changes prod." },
  },
};

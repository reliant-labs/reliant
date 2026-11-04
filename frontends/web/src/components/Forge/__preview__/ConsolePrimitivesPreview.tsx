// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the forge-ui primitives, composed the way the console
 * uses them, on fabricated data — the Cloudflare-style SidebarLayout, a
 * PageHeader with breadcrumb + status + meta + actions, underline Tabs, Cards
 * with flush DataTables (whole-row links, numeric columns), Status, Tag and
 * Mono.
 *
 *   /forge-ui-preview?page=overview|releases|observability|logs|alerts|dashboards|environments|secrets|activity
 *   &rail=1 collapses the nav to the icon rail.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import {
  Activity,
  Gauge,
  Globe,
  Home,
  KeyRound,
  Layers,
  Settings,
  SlidersHorizontal,
  Tag as TagIcon,
} from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import DataTable from "@/components/forge-ui/data_table";
import Mono from "@/components/forge-ui/mono";
import PageHeader from "@/components/forge-ui/page_header";
import SidebarLayout, { type SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import Tabs from "@/components/forge-ui/tabs";
import Tag from "@/components/forge-ui/tag";

const BASE = "/forge-ui-preview";
const href = (page: string, rail: boolean) => `${BASE}?page=${page}${rail ? "&rail=1" : ""}`;

type Workload = {
  name: string;
  kind: string;
  tone: StatusTone;
  state: string;
  version: string;
  replicas: string;
  p95: string;
  errors: string;
  digest: string;
};

const WORKLOADS: Workload[] = [
  { name: "api", kind: "service", tone: "success", state: "Healthy", version: "v1.8.2", replicas: "3/3", p95: "182 ms", errors: "0.02%", digest: "sha256:4be1c0a9d2f7e3b6a5c4d3e2f1a0b9c8d7e6f5a4" },
  { name: "worker", kind: "service", tone: "warning", state: "Degraded", version: "v1.8.2", replicas: "2/3", p95: "1.4 s", errors: "1.20%", digest: "sha256:9c8d7e6f5a4b3c2d1e0f9a8b7c6d5e4f3a2b1c0d" },
  { name: "web", kind: "service", tone: "success", state: "Healthy", version: "v1.8.2", replicas: "2/2", p95: "64 ms", errors: "0.00%", digest: "sha256:1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b" },
  { name: "billing-sync", kind: "job", tone: "danger", state: "Failed", version: "v1.8.1", replicas: "0/1", p95: "—", errors: "—", digest: "sha256:0f9e8d7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e" },
  { name: "migrate", kind: "job", tone: "neutral", state: "Completed", version: "v1.8.2", replicas: "—", p95: "—", errors: "—", digest: "sha256:5d4c3b2a1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6c" },
];

const RELEASES = [
  { version: "v1.8.2", sha: "9f3c2a17e4b0c6d8a1f2e3d4c5b6a7980f1e2d3c", subject: "Retry webhook delivery with backoff", author: "ada", prod: "Live", age: "2h" },
  { version: "v1.8.1", sha: "1e2d3c4b5a69788f7e6d5c4b3a2918f7e6d5c4b3", subject: "Split billing sync into its own job", author: "lin", prod: "Rolled back", age: "1d" },
  { version: "v1.8.0", sha: "7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e0f9a8b", subject: "Workload detail: pods table", author: "sam", prod: "Superseded", age: "3d" },
];

function nav(page: string, rail: boolean): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const item = (id: string, label: string, icon: React.ReactNode, extra: Partial<SidebarNavItem> = {}) => ({
    label,
    href: href(id, rail),
    icon,
    active: page === id,
    ...extra,
  });
  const child = (id: string, label: string, extra: Partial<SidebarNavItem> = {}) => ({
    label,
    href: href(id, rail),
    active: page === id,
    ...extra,
  });
  return {
    items: [
      item("overview", "Overview", <Home />, { badge: { type: "dot", tone: "warning", label: "A workload is degraded" } }),
      item("releases", "Releases", <TagIcon />),
      item("observability", "Observability", <Gauge />, {
        children: [
          child("logs", "Logs"),
          child("traces", "Traces"),
          child("metrics", "Metrics"),
          child("profiles", "Profiles"),
          child("dashboards", "Dashboards", {
            badge: { type: "new" },
            children: [child("dash-api", "API latency"), child("dash-queue", "Queue depth")],
          }),
          child("alerts", "Alerts", { badge: { type: "count", value: 1, tone: "danger", label: "firing" } }),
        ],
      }),
      item("environments", "Environments", <Layers />, {
        section: "Configure",
        children: [
          child("env-prod", "prod"),
          child("env-e2e", "e2e"),
          child("env-dev", "dev"),
          child("env-previews", "Previews", { badge: { type: "count", value: 3 } }),
        ],
      }),
      item("secrets", "Secrets", <KeyRound />, { section: "Configure" }),
      item("domains", "Domains", <Globe />, { section: "Configure" }),
      item("monitoring", "Monitoring settings", <SlidersHorizontal />, { section: "Configure" }),
      item("settings", "Project settings", <Settings />, { section: "Configure" }),
    ],
    footer: [item("activity", "Activity", <Activity />)],
  };
}

function Overview() {
  return (
    <>
      <PageHeader
        breadcrumbs={[{ label: "control-plane", href: href("overview", false) }, { label: "Overview" }]}
        title="Overview"
        meta={
          <>
            <Tag>prod</Tag>
            <span>gke · us-central1</span>
            <span>
              Release <span className="text-ink">v1.8.2</span>, 2h ago
            </span>
          </>
        }
        actions={[{ label: "View releases", href: href("releases", false) }, { label: "Deploy to prod", variant: "primary" }]}
      />
      <div className="grid grid-cols-4 gap-4">
        {[
          ["Requests / s", "1,284"],
          ["p95 latency", "182 ms"],
          ["Error rate", "0.31%"],
          ["Spend today", "$42.10"],
        ].map(([caption, figure]) => (
          <Card key={caption}>
            <div className="text-xs text-ink-muted">{caption}</div>
            <div className="mt-1 text-xl font-semibold tabular-nums text-ink">{figure}</div>
          </Card>
        ))}
      </div>
      <Card padding="none" className="mt-8">
        <div className="px-4 pt-3">
          <CardHeader title="Services and jobs" description="Every workload in prod" />
        </div>
        <DataTable<Workload>
          flush
          ariaLabel="Workloads"
          data={WORKLOADS}
          getRowKey={(w) => w.name}
          getRowHref={(w) => href(`workload-${w.name}`, false)}
          columns={[
            { key: "name", header: "Name", render: (_, w) => <span className="font-medium">{w.name}</span> },
            { key: "state", header: "Status", render: (_, w) => <Status tone={w.tone} label={w.state} /> },
            { key: "kind", header: "Kind", render: (_, w) => <Tag>{w.kind}</Tag> },
            { key: "version", header: "Version" },
            { key: "replicas", header: "Replicas", numeric: true },
            { key: "p95", header: "p95", numeric: true },
            { key: "errors", header: "Errors", numeric: true },
            { key: "digest", header: "Image", interactive: true, render: (_, w) => <Mono value={w.digest} /> },
          ]}
        />
      </Card>
    </>
  );
}

function Releases() {
  return (
    <>
      <PageHeader title="Releases" meta={<span>3 releases in the last 7 days</span>} />
      <Tabs
        ariaLabel="Release filter"
        tabs={[
          { id: "all", label: "All", badge: 3 },
          { id: "prod", label: "On prod" },
          { id: "previews", label: "Previews", badge: 3 },
        ]}
      >
        {() => (
          <Card padding="none">
            <DataTable
              flush
              ariaLabel="Releases"
              data={RELEASES}
              getRowKey={(r) => r.version}
              getRowHref={(r) => href(`release-${r.version}`, false)}
              columns={[
                { key: "version", header: "Version", render: (v) => <span className="font-medium">{String(v)}</span> },
                { key: "subject", header: "Commit" },
                { key: "sha", header: "Sha", interactive: true, render: (v) => <Mono value={String(v)} /> },
                { key: "author", header: "Author" },
                {
                  key: "prod",
                  header: "prod",
                  render: (v) =>
                    v === "Live" ? (
                      <Status tone="success" label="Live" />
                    ) : v === "Rolled back" ? (
                      <Tag tone="danger">rolled back</Tag>
                    ) : (
                      <Status tone="neutral" label={String(v)} />
                    ),
                },
                { key: "age", header: "Cut", numeric: true },
              ]}
            />
          </Card>
        )}
      </Tabs>
    </>
  );
}

function Generic({ title }: { title: string }) {
  return (
    <>
      <PageHeader
        breadcrumbs={[{ label: "Observability", href: href("observability", false) }, { label: title }]}
        title={title}
        titleAccessory={<Status tone="danger" label="1 firing" />}
      />
      <Card>
        <CardHeader title="Panel title" description="A sentence-case caption under it" />
        <p className="text-sm text-ink-muted">
          Panels are bordered, unshadowed, and never nest; the title is part of the panel.
        </p>
      </Card>
    </>
  );
}

export default function ConsolePrimitivesPreview() {
  const search = useSearch({ strict: false }) as { page?: string; rail?: string };
  const page = search.page ?? "overview";
  const rail = search.rail === "1";
  const { items, footer } = nav(page, rail);
  const navigate = useNavigate();
  return (
    <div className="forge-ui">
      <SidebarLayout
        navLabel="Console"
        brand={
          <span className="flex items-center gap-2 text-sm font-semibold text-ink">
            <span className="grid h-5.5 w-5.5 place-items-center rounded-md bg-accent text-xs text-on-accent">R</span>
            control-plane
          </span>
        }
        brandCollapsed={
          <span className="grid h-5.5 w-5.5 place-items-center rounded-md bg-accent text-xs font-semibold text-on-accent">R</span>
        }
        navItems={items}
        footerItems={footer}
        user={{ name: "ada", email: "ada@example.com" }}
        collapsed={rail}
        onToggle={() => void navigate({ to: BASE, search: rail ? { page } : { page, rail: "1" } })}
        headerContent={
          <div className="flex min-w-0 flex-1 items-center gap-1.5 text-sm text-ink-muted">
            <span>control-plane</span>
            <span aria-hidden="true">›</span>
            <span className="font-medium text-ink">{page}</span>
          </div>
        }
      >
        {page === "overview" ? <Overview /> : page === "releases" ? <Releases /> : <Generic title={page} />}
      </SidebarLayout>
    </div>
  );
}

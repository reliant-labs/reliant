// Copyright (c) 2025 Reliant Labs

/**
 * /sites in the forge shell: every static site the organization hosts, where
 * it opens, and the custom domains bound to it (sites.ts). A row opens the
 * app's Domains page in its console, where domains are added and bound.
 */

import { useMemo } from "react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import PageHeader from "@/components/forge-ui/page_header";
import Tag from "@/components/forge-ui/tag";
import { useApps } from "@/hooks/apps-queries";
import { useForgeDomains } from "@/hooks/forge-domain-queries";
import { DOMAIN_STATE_LABELS } from "@/services/forge/domains";

import { LiveLink } from "../Apps/appParts";
import { consolePath, withSearch } from "../Forge/Console/paths";
import { RunFromTerminal } from "./RunFromTerminal";
import { sitesOf, type SiteRow } from "./sites";

export function ForgeSitesPage() {
  const { apps, loading, availability, detail } = useApps();
  const domains = useForgeDomains();
  const rows = useMemo(() => sitesOf(apps, domains.data?.domains ?? []), [apps, domains.data]);
  const statusesLoading = apps.some((app) => app.envs.some((env) => env.loading));

  const columns: DataTableColumn<SiteRow>[] = [
    {
      key: "site",
      header: "Site",
      className: "truncate font-medium",
      render: (_, row) => (
        <span title={`${row.app} · ${row.site}`}>
          {row.app}
          <span className="text-ink-muted"> / {row.site}</span>
        </span>
      ),
    },
    { key: "env", header: "Environment", width: "9rem", className: "truncate text-ink-muted" },
    {
      key: "url",
      header: "Opens at",
      width: "30%",
      interactive: true,
      className: "truncate",
      render: (_, row) => <LiveLink link={{ workload: row.site, url: row.url, host: row.host, custom: row.custom }} />,
    },
    {
      key: "domains",
      header: "Custom domains",
      width: "30%",
      className: "truncate",
      render: (_, row) =>
        row.domains.length === 0 ? (
          <span className="text-ink-muted">None</span>
        ) : (
          <span className="inline-flex flex-wrap items-center gap-1.5">
            {row.domains.map((domain) => (
              <span key={domain.hostname} className="inline-flex items-center gap-1" title={DOMAIN_STATE_LABELS[domain.state]}>
                <span className="truncate">{domain.hostname}</span>
                {domain.state !== "live" && <Tag tone="warning">{DOMAIN_STATE_LABELS[domain.state]}</Tag>}
              </span>
            ))}
          </span>
        ),
    },
  ];

  return (
    <div className="space-y-5" data-testid="forge-sites-page">
      <PageHeader
        title="Sites"
        meta={<span>The static sites your organization hosts, and the domains that serve them.</span>}
        className="mb-0"
      />
      {availability !== "available" && availability !== "no-control-plane" && !loading && (
        <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-3 text-sm text-ink-muted" role="status">
          {availability === "no-access"
            ? "Your role in this organization cannot read deployments, so its sites cannot be listed. An org admin can see them."
            : `The control plane could not be reached, so sites are missing from this list.${detail ? ` ${detail}` : ""}`}
        </p>
      )}
      {!loading && !statusesLoading && rows.length === 0 ? (
        <RunFromTerminal
          title="No sites yet"
          description="A forge project with a frontend gets a site on its first deploy. Add a custom domain to it from the app's Domains page."
          commands={[{ label: "Deploy a project's frontend", command: "forge env deploy prod" }]}
          testId="forge-sites-empty"
        />
      ) : (
        <div className="forge-ui">
          <DataTable<SiteRow>
            ariaLabel="Sites"
            columns={columns}
            data={rows}
            loading={loading || (statusesLoading && rows.length === 0)}
            layout="fixed"
            getRowKey={(row) => `${row.envId}/${row.site}`}
            getRowHref={(row) => withSearch(consolePath(row.app, { kind: "domains" }), { env: row.env })}
            getRowProps={(row) => ({ "data-testid": `site-row-${row.app}-${row.env}-${row.site}` })}
          />
        </div>
      )}
    </div>
  );
}

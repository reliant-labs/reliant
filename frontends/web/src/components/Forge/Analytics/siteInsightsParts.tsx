// Copyright (c) 2026 Reliant Labs

/**
 * What every Site insights view shares (SiteInsightsPage.tsx): the header,
 * with the page's title, the view's controls and the Speed / SEO /
 * Accessibility tabs, and the site picker.
 *
 * THE SITE is the URL's `?site=`: one site's telemetry `service.name`, absent
 * for every site of the project. The tabs carry it, so moving between views
 * keeps the site.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import Link from "@/components/forge-ui/link";
import { ConsolePageHeader } from "../Console/severalSectionContext";
import { usePanelQuery } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { useForgeConsole } from "../Console/consoleContext";
import { PAGE_SUBTITLES } from "../Console/consoleCrumbs";
import { SITE_INSIGHTS_VIEW_LABELS, SITE_INSIGHTS_VIEWS, type SiteInsightsView } from "../Console/paths";
import { VariableSelect } from "../Observability/Dashboards/parts";
import { formatCount } from "../Observability/format";
import { siteRows, sitesPanel } from "./speedInsightsModel";

/** The page's chosen site, read from and written to `?site=`. */
export function useSiteChoice(): { site: string | undefined; services: string[]; setSite: (site: string | undefined) => void } {
  const search = useSearch({ strict: false }) as { site?: unknown };
  const navigate = useNavigate();
  const site = typeof search.site === "string" && search.site !== "" ? search.site : undefined;
  const services = useMemo(() => (site ? [site] : []), [site]);
  const setSite = (next: string | undefined) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, site: next || undefined }), replace: true } as never);
  return { site, services, setSite };
}

/** The title, the view's controls, then the tabs. */
export function SiteInsightsHeader({ view, controls }: { view: SiteInsightsView; controls?: ReactNode }) {
  const { href } = useForgeConsole();
  const { site } = useSiteChoice();
  return (
    <>
      <ConsolePageHeader density="compact" className="mb-0" title="Site insights" subtitle={PAGE_SUBTITLES["site-insights"]} controls={controls} />
      <nav aria-label="Site insights" className="-mt-1 flex gap-1 overflow-x-auto border-b border-border" data-testid="site-insights-tabs">
        {SITE_INSIGHTS_VIEWS.map((candidate) => (
          <Link
            key={candidate}
            href={href({ kind: "site-insights", view: candidate }, site ? { site } : undefined)}
            aria-current={candidate === view ? "page" : undefined}
            data-testid={`site-insights-tab-${candidate}`}
            className={cn(
              "no-color -mb-px inline-flex h-9 shrink-0 items-center gap-1.5 border-b-2 px-3 text-ui transition-colors",
              candidate === view ? "border-primary font-medium text-ink" : "border-transparent text-ink-muted hover:text-ink"
            )}
          >
            {SITE_INSIGHTS_VIEW_LABELS[candidate]}
          </Link>
        ))}
      </nav>
    </>
  );
}

const ALL_SITES = "";

/**
 * Which site the page reads: every site that sent web vitals in the window,
 * busiest first. Drawn only when there is a choice (two sites or more), or
 * when one is chosen (so it can be cleared, even after its data aged out).
 */
export function SitePicker({ scope, range }: { scope: ObsScope | null; range: RangePreset }) {
  const { site, setSite } = useSiteChoice();
  const project = scope?.projectId ?? "";
  const panel = useMemo(() => sitesPanel(project), [project]);
  const sites = siteRows(usePanelQuery(scope, range, panel, []).data);
  if (sites.length < 2 && !site) return null;
  const options = [
    { value: ALL_SITES, label: "All sites" },
    ...sites.map((row) => ({ value: row.service, label: `${row.service} · ${formatCount(row.views)}` })),
    ...(site && !sites.some((row) => row.service === site) ? [{ value: site, label: site }] : []),
  ];
  return <VariableSelect id="site-insights-site" label="site" value={site ?? ALL_SITES} options={options} onChange={(value) => setSite(value || undefined)} />;
}

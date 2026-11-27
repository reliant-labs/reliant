// Copyright (c) 2026 Reliant Labs

import { fireEvent, render, renderHook, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AuditSitePicker, RerunButton } from "./siteAuditParts";
import { auditTargetOf, headOf, type AuditSite, type SiteAudit } from "./siteAuditModel";
import { indexabilityOf, siteChecks } from "./seoInsightsModel";
import { useAuditSites } from "./useSiteAudit";

const mocks = vi.hoisted(() => ({
  statuses: new Map(),
  live: { envs: [{ id: "env", name: "prod", kind: "hosted", release: "v1" }] },
  orgId: "selected-org" as string | undefined,
  mutate: vi.fn(),
}));
vi.mock("@/hooks/forge-queries", () => ({
  useLiveView: () => ({ data: mocks.live, isLoading: false }),
  useCloudEnvStatuses: () => mocks.statuses,
}));
vi.mock("@/hooks/observability-queries", () => ({ useObservabilityOrg: () => ({ orgId: mocks.orgId }) }));
vi.mock("@tanstack/react-query", () => ({ useQueryClient: () => ({ invalidateQueries: vi.fn() }) }));
vi.mock("@/hooks/controlplane/site-audit-service-hooks_gen", () => ({
  siteAuditServiceKeys: { getSiteAudit: { all: [] } },
  useGetSiteAudit: vi.fn(),
  useRunSiteAudit: () => ({ mutate: mocks.mutate, isPending: false, error: null }),
}));
vi.mock("../Observability/obsReadOnly", () => ({ useObsReadOnly: () => ({ readOnly: false }) }));
vi.mock("../Observability/Dashboards/parts", () => ({
  VariableSelect: ({ value, options, onChange }: { value: string; options: { value: string; label: string }[]; onChange: (value: string) => void }) => (
    <select aria-label="site" value={value} onChange={(event) => onChange(event.target.value)}>
      {options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
    </select>
  ),
}));

describe("site audit regressions", () => {
  beforeEach(() => {
    mocks.orgId = "selected-org";
    mocks.mutate.mockClear();
    mocks.statuses = new Map([["env", { data: { workloads: [{ name: "old", tier: "static" }] }, isLoading: false }]]);
  });

  it("replaces already-loaded workloads on a status refresh", () => {
    const { result, rerender } = renderHook(() => useAuditSites("project"));
    expect(result.current.sites.map((site) => site.frontend)).toEqual(["old"]);
    mocks.statuses = new Map([["env", { data: { workloads: [{ name: "new", tier: "static" }] }, isLoading: false }]]);
    rerender();
    expect(result.current.sites.map((site) => site.frontend)).toEqual(["new"]);
    expect(auditTargetOf({ project: "project", site: "old", env: "prod", sites: result.current.sites }).kind).toBe("not-static");
  });

  it("recovers a Speed app selection with only one static site", () => {
    const sites: AuditSite[] = [{ frontend: "docs", envs: [{ id: "env", name: "prod", preview: false }] }];
    const target = auditTargetOf({ project: "project", site: "project-app", env: "prod", sites });
    expect(target.kind).toBe("not-static");
    const onChange = vi.fn();
    render(<AuditSitePicker sites={sites} target={target} onChange={onChange} />);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "docs" } });
    expect(onChange).toHaveBeenLastCalledWith("docs");
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "" } });
    expect(onChange).toHaveBeenLastCalledWith(undefined);
  });

  it("labels a preview blocked solely by robots.txt as intentionally not indexed", () => {
    const audit: SiteAudit = {
      audit: null, previous: null, inProgress: null, quota: null, findings: [],
      pages: [{ route: "/", status: 200, rendered: false, seo: 100, a11y: null, thumbnailUrl: "", head: headOf({ indexable: false }) }],
    };
    expect(indexabilityOf(audit, true)).toMatchObject({ expected: "noindex", actual: "noindex", label: "Not indexed (preview)", problem: false });
    expect(siteChecks(audit, true).find((check) => check.id === "indexing")).toMatchObject({ state: "pass", value: "Not indexed (preview)" });
  });

  it("re-runs in the selected non-default organization and waits for it to resolve", () => {
    const { rerender } = render(<RerunButton audit={undefined} envId="env" frontend="docs" />);
    fireEvent.click(screen.getByRole("button", { name: "Re-run" }));
    expect(mocks.mutate).toHaveBeenCalledWith({ environmentId: "env", frontend: "docs", orgId: "selected-org" });
    mocks.orgId = undefined;
    rerender(<RerunButton audit={undefined} envId="env" frontend="docs" />);
    expect((screen.getByRole("button", { name: "Re-run" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

// Copyright (c) 2025 Reliant Labs

/**
 * Save the explorer's query to a dashboard. The query IS a dashspec panel
 * (DESIGN §3.1), so this is lossless: the panel is appended as it is.
 *
 *   admins      pick a workspace dashboard, or name a new one. The panel is
 *               appended to the dashboard's current version — GetDashboard →
 *               upsertPanel → SaveDashboard{baseVersion} — and when someone
 *               saved in between (Aborted, version_conflict) it is read again
 *               and appended once more; a second conflict is shown, not looped.
 *               From there the dashboard's own Promote to code makes it a file.
 *   everyone    else gets "Copy panel JSON": the same panel, for a
 *               dashboards-as-code file or an admin to add.
 *
 * Release dashboards are read-only, so they are not offered.
 */

import { useId, useState } from "react";
import { Check, Copy, LayoutDashboard } from "lucide-react";
import { Code, ConnectError } from "@connectrpc/connect";

import Link from "@/components/forge-ui/link";
import Modal from "@/components/forge-ui/modal";
import { useDashboardMutation } from "@/hooks/dashboard-queries";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { DashboardRef, DashboardSummary, SavedDashboard } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { newPanelId, slugify, SLUG_PATTERN } from "../Dashboards/dashboardModel";
import { envScopeLabel, envScopeOf, writeErrorMessage } from "../Dashboards/dialogs";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL } from "../Dashboards/parts";
import { upsertPanel } from "../Dashboards/panelBuilder";
import type { ObsEnvironment } from "../obsPlace";

function isConflict(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Aborted;
}

/**
 * Append `panel` to the workspace dashboard `slug` as its next version: read
 * the current version, add the panel under an id the dashboard does not use,
 * save against that version. A conflict (someone saved in between) is retried
 * once against the version that won; a second one is the caller's to show.
 */
export async function appendPanel(api: ObservabilityApi, input: { orgId: string; projectId: string; slug: string; panel: Panel; message: string }): Promise<SavedDashboard> {
  const ref: DashboardRef = { kind: "workspace", projectId: input.projectId, key: input.slug };
  for (let attempt = 0; ; attempt++) {
    const doc = await api.getDashboard({ orgId: input.orgId, ref });
    const base = slugify(input.panel.title) || "panel";
    const panel = { ...input.panel, id: doc.spec.panels.some((p) => p.id === base) ? newPanelId(doc.spec, base) : base };
    try {
      return await api.saveDashboard({
        orgId: input.orgId,
        projectId: input.projectId,
        slug: input.slug,
        spec: upsertPanel(doc.spec, panel),
        baseVersion: doc.version,
        message: input.message,
      });
    } catch (error) {
      if (attempt === 0 && isConflict(error)) continue;
      throw error;
    }
  }
}

/** A new workspace dashboard holding `panel`, release markers on. */
export function newDashboardSpec(slug: string, title: string, panel: Panel): Dashboard {
  return { id: slug, title, filters: [], annotations: [{ source: "releases" }], panels: [{ ...panel, id: slugify(panel.title) || "panel-1" }] };
}

const INPUT =
  "h-8 w-full rounded-md border border-border bg-surface px-2.5 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-accent";

export interface SaveToDashboardProps {
  orgId: string | null;
  project: string;
  env: string;
  environments: readonly ObsEnvironment[];
  /** The panel to save, titled. */
  panel: Panel;
  /** The caller may author dashboards (an org admin). */
  canWrite: boolean;
  /** The index's dashboards; the workspace ones are offered. */
  dashboards: readonly DashboardSummary[];
  dashboardHref: (ref: DashboardRef) => string;
  disabled?: boolean;
  disabledReason?: string;
}

/** The toolbar action: "Save to dashboard" for an admin, "Copy panel JSON" for everyone else. */
export function SaveToDashboard(props: SaveToDashboardProps) {
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  if (!props.canWrite) {
    return (
      <button
        type="button"
        className={BUTTON_SMALL}
        disabled={props.disabled}
        title={props.disabled ? props.disabledReason : "Only organization admins can save to a dashboard: copy the panel for a dashboards-as-code file instead."}
        onClick={() =>
          void navigator.clipboard?.writeText(JSON.stringify(props.panel, null, 2)).then(() => {
            setCopied(true);
            window.setTimeout(() => setCopied(false), 1500);
          })
        }
        data-testid="metrics-copy-panel-json"
      >
        {copied ? <Check className="h-3 w-3" aria-hidden="true" /> : <Copy className="h-3 w-3" aria-hidden="true" />}
        {copied ? "Copied" : "Copy panel JSON"}
      </button>
    );
  }
  return (
    <>
      <button
        type="button"
        className={BUTTON_SMALL}
        disabled={props.disabled}
        title={props.disabled ? props.disabledReason : undefined}
        onClick={() => setOpen(true)}
        data-testid="metrics-save-open"
      >
        <LayoutDashboard className="h-3 w-3" aria-hidden="true" />
        Save to dashboard
      </button>
      {open && <SaveToDashboardDialog {...props} onClose={() => setOpen(false)} />}
    </>
  );
}

export function SaveToDashboardDialog({ orgId, project, env, environments, panel, dashboards, dashboardHref, onClose }: SaveToDashboardProps & { onClose: () => void }) {
  const workspace = dashboards.filter((d) => d.ref.kind === "workspace" && !d.readOnly);
  const [target, setTarget] = useState<string>(workspace[0]?.ref.key ?? "new");
  const [panelTitle, setPanelTitle] = useState(panel.title);
  const [newTitle, setNewTitle] = useState("");
  const [scope, setScope] = useState<"scope" | "all">("scope");
  const ids = { panel: useId(), title: useId(), target: useId() };
  const slug = slugify(newTitle);
  const creating = target === "new";
  const titled = { ...panel, title: panelTitle.trim() || panel.title };

  const save = useDashboardMutation(orgId, (api, org, _input: void) => {
    if (creating) {
      return api.saveDashboard({
        orgId: org,
        projectId: project,
        slug,
        spec: newDashboardSpec(slug, newTitle.trim(), titled),
        envScope: envScopeOf(scope, env, (name) => environments.find((candidate) => candidate.name === name)?.id),
        baseVersion: 0,
        message: `Created from the metrics explorer with ${titled.title}`,
      });
    }
    return appendPanel(api, { orgId: org, projectId: project, slug: target, panel: titled, message: `Added ${titled.title} from the metrics explorer` });
  });

  const done = save.data;
  const invalid = creating ? !newTitle.trim() || !SLUG_PATTERN.test(slug) : !target;
  return (
    <Modal
      open
      onClose={onClose}
      title="Save to a dashboard"
      description="The query is added as a panel, exactly as you see it here. The dashboard keeps a version history, and you can promote it to code from there."
      footer={
        done ? (
          <div className="flex justify-end gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={onClose}>
              Close
            </button>
            <Link href={dashboardHref(done.summary.ref)} className={`no-color ${BUTTON_PRIMARY}`} data-testid="metrics-save-open-dashboard">
              Open {done.summary.title}
            </Link>
          </div>
        ) : (
          <div className="flex justify-end gap-2">
            <button type="button" className={BUTTON_SECONDARY} onClick={onClose}>
              Cancel
            </button>
            <button type="button" className={BUTTON_PRIMARY} disabled={save.isPending || invalid} onClick={() => save.mutate()} data-testid="metrics-save-confirm">
              {save.isPending ? "Saving…" : creating ? `Create for ${envScopeLabel(scope, env)}` : "Add panel"}
            </button>
          </div>
        )
      }
    >
      {done ? (
        <p className="flex items-start gap-2 text-sm text-ink" role="status" data-testid="metrics-save-done">
          <Check className="mt-0.5 h-4 w-4 shrink-0 text-success" aria-hidden="true" />
          <span>
            Added <span className="font-medium">{titled.title}</span> to {done.summary.title} as v{done.version.version}.
          </span>
        </p>
      ) : (
        <div className="space-y-4" data-testid="metrics-save-dialog">
          <div className="space-y-1.5">
            <label htmlFor={ids.panel} className="block text-xs font-medium text-ink">
              Panel title
            </label>
            <input id={ids.panel} value={panelTitle} onChange={(e) => setPanelTitle(e.target.value)} className={INPUT} data-testid="metrics-save-panel-title" />
          </div>
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs font-medium text-ink">Dashboard</legend>
            {workspace.length === 0 && <p className="text-xs text-ink-muted">This project has no workspace dashboard yet.</p>}
            <div className="max-h-48 space-y-1 overflow-y-auto">
              {workspace.map((d) => (
                <Choice key={d.ref.key} name={ids.target} checked={target === d.ref.key} onChange={() => setTarget(d.ref.key)} label={d.title} detail={`${d.panelCount} panels · v${d.workspace?.version ?? 0}`} testId={`metrics-save-target-${d.ref.key}`} />
              ))}
              <Choice name={ids.target} checked={creating} onChange={() => setTarget("new")} label="New dashboard…" detail="A workspace dashboard with this panel" testId="metrics-save-target-new" />
            </div>
          </fieldset>
          {creating && (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <label htmlFor={ids.title} className="block text-xs font-medium text-ink">
                  Dashboard title
                </label>
                <input id={ids.title} value={newTitle} onChange={(e) => setNewTitle(e.target.value)} placeholder="Checkout health" className={INPUT} data-testid="metrics-save-new-title" />
                {newTitle.trim() && (
                  <p className="text-xs text-ink-muted">
                    Its URL, and its file if you promote it: <span className="font-mono">deploy/observability/dashboards/{slug}.json</span>
                  </p>
                )}
              </div>
              {env !== ENV_ALL && (
                <fieldset className="space-y-1">
                  <legend className="mb-1 text-xs font-medium text-ink">Environments</legend>
                  <Choice name={`${ids.target}-env`} checked={scope === "scope"} onChange={() => setScope("scope")} label={`Only ${env}`} detail={`Listed when the scope is ${env}.`} />
                  <Choice name={`${ids.target}-env`} checked={scope === "all"} onChange={() => setScope("all")} label="All environments" detail="Listed in every environment." />
                </fieldset>
              )}
            </div>
          )}
          {!!save.error && (
            <p role="alert" className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-danger-ink" data-testid="metrics-save-error">
              {isConflict(save.error)
                ? "Someone else saved this dashboard twice while you were adding the panel. Open it to see their changes, then try again."
                : writeErrorMessage(save.error)}
            </p>
          )}
        </div>
      )}
    </Modal>
  );
}

function Choice({ name, checked, onChange, label, detail, testId }: { name: string; checked: boolean; onChange: () => void; label: string; detail: string; testId?: string }) {
  return (
    <label className={cn("flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2 text-sm", checked ? "border-ink/40 bg-ink/5" : "border-border hover:bg-ink/5")}>
      <input type="radio" name={name} checked={checked} onChange={onChange} className="mt-0.5 accent-current" data-testid={testId} />
      <span className="min-w-0">
        <span className="block truncate font-medium text-ink">{label}</span>
        <span className="block text-xs text-ink-muted">{detail}</span>
      </span>
    </label>
  );
}

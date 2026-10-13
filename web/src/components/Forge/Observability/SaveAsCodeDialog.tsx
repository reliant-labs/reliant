// Copyright (c) 2025 Reliant Labs

/**
 * "Save as code": the dashboard on screen — panel edits, filters, group-by —
 * as forge's dashboard spec, for the user to commit at
 * `deploy/observability/dashboards/<name>.json`.
 *
 * Copy or download; nothing is stored server-side (owner decision 2). The
 * file ships with the user's next release (contract K4) and from then on is
 * the dashboard every environment that release reaches shows. A file named
 * like a generated dashboard replaces it (decision 5), which is the default
 * here — saving an edited generated dashboard means "this is mine now".
 */

import { useId, useMemo, useState } from "react";
import { Check, Copy, Download } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { Modal } from "@/components/ui/Modal";
import type { AttributeFilter, Dashboard } from "@/services/controlPlane/observability/types";

import { attributeLabel } from "./attributes";
import { buildDashboardFile, dashboardFileJson, dashboardFileName, dashboardFileProblems, DASHBOARDS_DIR, defaultDashboardName } from "./obsdashSpec";

export interface SaveAsCodeDialogProps {
  open: boolean;
  onClose: () => void;
  project: string;
  /** As on screen, edits applied. */
  dashboard: Dashboard;
  filters: AttributeFilter[];
  groupBy: string[];
}

export function SaveAsCodeDialog({ open, onClose, project, dashboard, filters, groupBy }: SaveAsCodeDialogProps) {
  const [rawName, setRawName] = useState(() => defaultDashboardName(project, dashboard.id));
  const [title, setTitle] = useState(dashboard.title);
  const [copied, setCopied] = useState(false);
  const nameId = useId();
  const titleId = useId();

  const name = dashboardFileName(rawName);
  const path = `${DASHBOARDS_DIR}/${name}.json`;
  const file = useMemo(
    () => buildDashboardFile({ dashboard, name, title: title.trim() || dashboard.title, filters, groupBy }),
    [dashboard, name, title, filters, groupBy]
  );
  const json = useMemo(() => dashboardFileJson(file), [file]);
  const problems = useMemo(() => dashboardFileProblems(file), [file]);
  // Saved under its own name, the file takes the place of what is served now:
  // forge's generated dashboard, or the project's own file, which it updates.
  const sameName = name === defaultDashboardName(project, dashboard.id);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(json);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      setCopied(false);
    }
  };

  const download = () => {
    const url = URL.createObjectURL(new Blob([json], { type: "application/json" }));
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `${name}.json`;
    anchor.click();
    // Revoked a tick later: revoking in the same task can cancel the download in some browsers.
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  return (
    <Modal isOpen={open} onClose={onClose} title="Save as code" size="lg">
      <div className="space-y-4" data-testid="obs-save-as-code">
        <p className="text-sm text-foreground">
          Dashboards live in your repository, next to the code they watch. Commit this file and it ships with your next
          release; every environment that release is promoted to shows it here. Check it locally with{" "}
          <code className="font-mono text-xs">forge observe dashboards check</code>.
        </p>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <label htmlFor={nameId} className="mb-1 block text-xs font-medium text-muted-foreground">
              File name
            </label>
            <input
              id={nameId}
              value={rawName}
              onChange={(event) => setRawName(event.target.value)}
              className="h-8 w-full rounded-md border border-border bg-background px-2 font-mono text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-save-name"
            />
          </div>
          <div>
            <label htmlFor={titleId} className="mb-1 block text-xs font-medium text-muted-foreground">
              Title
            </label>
            <input
              id={titleId}
              value={title}
              onChange={(event) => setTitle(event.target.value)}
              className="h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-save-title"
            />
          </div>
        </div>
        <div className="space-y-1 rounded-md border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground">
          <p>
            Save it as <code className="font-mono text-foreground" data-testid="obs-save-path">{path}</code>
            {!sameName
              ? "."
              : dashboard.source === "user"
                ? ", replacing your project's file of that name."
                : ", where it replaces the dashboard forge generates under that name. Rename it to keep both."}
          </p>
          <p>
            {filters.length > 0 || groupBy.length > 0 ? (
              <>
                Built in:{" "}
                {[
                  ...filters.map((f) => `${attributeLabel(f.key)} ${f.op === "in" ? `in ${(f.values ?? []).join(", ")}` : f.op === "exists" ? "exists" : `${f.op} ${f.value}`}`),
                  ...(groupBy.length ? [`grouped by ${groupBy.map(attributeLabel).join(", ")}`] : []),
                ].join("; ")}
                .{" "}
              </>
            ) : null}
            Not saved: the environments, service and time range you are looking at — the dashboard keeps its own
            environment and service filters.
          </p>
        </div>
        {problems.length > 0 && (
          <ul role="alert" className="space-y-0.5 rounded-md border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive" data-testid="obs-save-problems">
            {problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        )}
        <pre
          className="max-h-80 overflow-auto rounded-md border border-border/60 bg-background p-3 font-mono text-2xs leading-relaxed text-foreground"
          data-testid="obs-save-json"
          tabIndex={0}
          aria-label={`Contents of ${name}.json`}
        >
          {json}
        </pre>
        <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border/60 pt-4">
          <span className="mr-auto text-xs text-muted-foreground" aria-live="polite">
            {copied ? "Copied to the clipboard." : ""}
          </span>
          <Button
            variant="outline"
            size="sm"
            onClick={() => void copy()}
            disabled={problems.length > 0}
            leftIcon={copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
            data-testid="obs-save-copy"
          >
            Copy
          </Button>
          <Button
            variant="primary"
            size="sm"
            onClick={download}
            disabled={problems.length > 0}
            leftIcon={<Download className="h-3.5 w-3.5" />}
            data-testid="obs-save-download"
          >
            Download {name}.json
          </Button>
        </div>
      </div>
    </Modal>
  );
}

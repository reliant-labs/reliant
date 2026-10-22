// Copyright (c) 2025 Reliant Labs

/**
 * The dashboards' write dialogs: create (blank or from a JSON file), save a
 * copy (of a release dashboard, a template, or another workspace dashboard),
 * and promote to code.
 *
 * Each one that writes says which environments the result is about, in its
 * confirm button (decision D1: every mutating action names its environment):
 * a workspace dashboard is scoped to one environment or to all of them, and
 * the console's current scope is the default.
 */

import { useEffect, useId, useState, type ReactNode } from "react";
import { ConnectError, Code } from "@connectrpc/connect";
import { Copy, Download, ExternalLink, GitPullRequest } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import type { EnvScopeInput, PromotedDashboard } from "@/services/controlPlane/observability/dashboardTypes";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { Dashboard } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, INK_LINK } from "./parts";
import { SLUG_PATTERN, slugify } from "./dashboardModel";

/** Which environments a new dashboard is about: the scope's one, or every one. */
export function envScopeOf(choice: "scope" | "all", env: string, envIdOf: (name: string) => string | undefined): EnvScopeInput {
  if (choice === "all" || env === ENV_ALL) return { envIds: [] };
  const id = envIdOf(env);
  return { envIds: id ? [id] : [] };
}

export function envScopeLabel(choice: "scope" | "all", env: string): string {
  return choice === "all" || env === ENV_ALL ? "all environments" : env;
}

/** What a failed write should tell the reader, in a sentence. */
export function writeErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) {
    if (error.code === Code.AlreadyExists) return "A dashboard with that name already exists in this project. Pick another name.";
    if (error.code === Code.PermissionDenied) return "Only organization admins can create or change dashboards.";
    if (error.code === Code.Aborted) return "Someone saved a newer version while you were editing. Reload to see it, then make your change again.";
    if (error.code === Code.InvalidArgument) return `The dashboard isn't valid: ${error.rawMessage}`;
  }
  return obsErrorMessage(error) || "The control plane didn't answer. Try again.";
}

function Field({ label, hint, children, htmlFor }: { label: string; hint?: ReactNode; children: ReactNode; htmlFor: string }) {
  return (
    <div className="space-y-1.5">
      <label htmlFor={htmlFor} className="block text-xs font-medium text-ink">
        {label}
      </label>
      {children}
      {hint && <p className="text-xs text-ink-muted">{hint}</p>}
    </div>
  );
}

const INPUT =
  "h-8 w-full rounded-md border border-border bg-surface px-2.5 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-accent";

function EnvChoice({ env, value, onChange }: { env: string; value: "scope" | "all"; onChange: (value: "scope" | "all") => void }) {
  const name = useId();
  if (env === ENV_ALL) {
    return <p className="text-xs text-ink-muted">It will show in every environment of this project.</p>;
  }
  const option = (choice: "scope" | "all", label: string, detail: string) => (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2 text-sm",
        value === choice ? "border-ink/40 bg-ink/5" : "border-border hover:bg-ink/5"
      )}
    >
      <input type="radio" name={name} checked={value === choice} onChange={() => onChange(choice)} className="mt-0.5 accent-current" />
      <span>
        <span className="block font-medium text-ink">{label}</span>
        <span className="block text-xs text-ink-muted">{detail}</span>
      </span>
    </label>
  );
  return (
    <fieldset className="space-y-2">
      <legend className="mb-1.5 text-xs font-medium text-ink">Environments</legend>
      {option("scope", `Only ${env}`, `Listed when the scope is ${env}.`)}
      {option("all", "All environments", "Listed in every environment; the env variable picks which one it reads.")}
    </fieldset>
  );
}

function ErrorLine({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <p role="alert" className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-danger-ink" data-testid="dashboard-write-error">
      {writeErrorMessage(error)}
    </p>
  );
}

// ── Save a copy ─────────────────────────────────────────────────────────────

export function SaveCopyDialog({
  sourceTitle,
  what,
  env,
  pending,
  error,
  onCancel,
  onSave,
}: {
  sourceTitle: string;
  /** "release dashboard", "template"… */
  what: string;
  env: string;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSave: (input: { title: string; scope: "scope" | "all" }) => void;
}) {
  const [title, setTitle] = useState(what === "template" ? sourceTitle : `${sourceTitle} (copy)`);
  const [scope, setScope] = useState<"scope" | "all">("scope");
  const id = useId();
  return (
    <Modal
      open
      onClose={onCancel}
      title={what === "template" ? `Use the ${sourceTitle} template` : `Save a copy of ${sourceTitle}`}
      description={
        what === "template"
          ? "Creates a workspace dashboard from the template, filled with this project's services. You can edit it, and promote it to code later."
          : `The ${what} stays as it is. The copy is a workspace dashboard: you can edit it, it keeps a version history, and you can promote it to code later.`
      }
      footer={
        <div className="flex justify-end gap-2">
          <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className={BUTTON_PRIMARY}
            disabled={pending || !title.trim()}
            onClick={() => onSave({ title: title.trim(), scope })}
            data-testid="dashboard-copy-confirm"
          >
            {pending ? "Saving…" : `Save copy for ${envScopeLabel(scope, env)}`}
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <Field label="Title" htmlFor={id}>
          <input id={id} value={title} onChange={(e) => setTitle(e.target.value)} className={INPUT} data-testid="dashboard-copy-title" />
        </Field>
        <EnvChoice env={env} value={scope} onChange={setScope} />
        <ErrorLine error={error} />
      </div>
    </Modal>
  );
}

// ── New dashboard ───────────────────────────────────────────────────────────

/** The panel a blank dashboard starts with: forge requires one, and requests per service is the first question. */
export function starterDashboard(slug: string, title: string): Dashboard {
  return {
    id: slug,
    title,
    filters: [{ id: "service", label: "Service", field: "service.name" }],
    annotations: [{ source: "releases", label: "Releases" }],
    panels: [
      {
        id: "requests",
        title: "Requests",
        kind: "line",
        signal: "traces",
        where: [{ field: "span.kind", op: "eq", value: "server" }],
        series: [{ label: "requests", agg: "count" }],
        groupBy: ["service.name"],
        unit: "count",
        width: 24,
        height: 7,
      },
    ],
  };
}

/**
 * A dashboard file a reader imports: one forge dashboard object, as a repo
 * holds it (a leading `"version"` is dropped; the server stores canonical
 * form). Returns the problem in a sentence when it is not one.
 */
export function parseDashboardFile(text: string): { dashboard: Dashboard } | { problem: string } {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch {
    return { problem: "That file isn't JSON." };
  }
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return { problem: "That file isn't a dashboard: expected one JSON object." };
  const doc = { ...(raw as Record<string, unknown>) };
  if ("dashboards" in doc) return { problem: "That is a whole dashboards spec. Import one dashboard file (deploy/observability/dashboards/<name>.json)." };
  if (doc.version !== undefined && doc.version !== "forge.dashboards/v1") return { problem: `Unknown spec version "${String(doc.version)}".` };
  delete doc.version;
  if (!Array.isArray(doc.panels)) return { problem: "That file has no panels." };
  return {
    dashboard: {
      ...(doc as unknown as Dashboard),
      id: String(doc.id ?? ""),
      title: String(doc.title ?? ""),
      filters: Array.isArray(doc.filters) ? (doc.filters as Dashboard["filters"]) : [],
      panels: doc.panels as Dashboard["panels"],
    },
  };
}

export function NewDashboardDialog({
  mode,
  env,
  pending,
  error,
  onCancel,
  onCreate,
}: {
  mode: "blank" | "import";
  env: string;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onCreate: (input: { slug: string; spec: Dashboard; scope: "scope" | "all" }) => void;
}) {
  const [title, setTitle] = useState("");
  const [slug, setSlug] = useState("");
  const [slugEdited, setSlugEdited] = useState(false);
  const [scope, setScope] = useState<"scope" | "all">("scope");
  const [imported, setImported] = useState<Dashboard | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const titleId = useId();
  const slugId = useId();
  const fileId = useId();

  useEffect(() => {
    if (!slugEdited) setSlug(slugify(title));
  }, [title, slugEdited]);

  const slugOk = SLUG_PATTERN.test(slug);
  const ready = slugOk && !!title.trim() && (mode === "blank" || !!imported);

  return (
    <Modal
      open
      onClose={onCancel}
      title={mode === "blank" ? "New dashboard" : "Import a dashboard"}
      description={
        mode === "blank"
          ? "A workspace dashboard, saved in the console and versioned. It starts with one panel — requests per service — for you to change."
          : "A forge dashboard file (deploy/observability/dashboards/<name>.json). It is saved as a workspace dashboard; the file itself is not changed."
      }
      footer={
        <div className="flex justify-end gap-2">
          <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className={BUTTON_PRIMARY}
            disabled={pending || !ready}
            onClick={() => {
              const spec = mode === "blank" || !imported ? starterDashboard(slug, title.trim()) : { ...imported, id: slug, title: title.trim() };
              onCreate({ slug, spec, scope });
            }}
            data-testid="dashboard-create-confirm"
          >
            {pending ? "Creating…" : `Create for ${envScopeLabel(scope, env)}`}
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        {mode === "import" && (
          <Field label="File" htmlFor={fileId} hint={problem ? <span className="text-danger-ink">{problem}</span> : imported ? `${imported.panels.length} panels` : undefined}>
            <input
              id={fileId}
              type="file"
              accept="application/json,.json"
              className="block w-full text-sm text-ink file:mr-3 file:h-8 file:rounded-md file:border file:border-border file:bg-surface file:px-3 file:text-sm file:text-ink"
              data-testid="dashboard-import-file"
              onChange={async (event) => {
                const file = event.target.files?.[0];
                if (!file) return;
                const parsed = parseDashboardFile(await file.text());
                if ("problem" in parsed) {
                  setImported(null);
                  setProblem(parsed.problem);
                  return;
                }
                setProblem(null);
                setImported(parsed.dashboard);
                if (!title) setTitle(parsed.dashboard.title);
                if (parsed.dashboard.id && SLUG_PATTERN.test(parsed.dashboard.id)) {
                  setSlug(parsed.dashboard.id);
                  setSlugEdited(true);
                }
              }}
            />
          </Field>
        )}
        <Field label="Title" htmlFor={titleId}>
          <input id={titleId} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Checkout latency" className={INPUT} data-testid="dashboard-new-title" />
        </Field>
        <Field
          label="Name"
          htmlFor={slugId}
          hint={
            slugOk || !slug ? (
              <>
                The URL, and the file name if you promote it to code: <span className="font-mono">deploy/observability/dashboards/{slug || "…"}.json</span>
              </>
            ) : (
              <span className="text-danger-ink">Lowercase letters, digits and dashes, starting with a letter or digit.</span>
            )
          }
        >
          <input
            id={slugId}
            value={slug}
            onChange={(e) => {
              setSlug(e.target.value);
              setSlugEdited(true);
            }}
            className={cn(INPUT, "font-mono")}
            data-testid="dashboard-new-slug"
          />
        </Field>
        <EnvChoice env={env} value={scope} onChange={setScope} />
        <ErrorLine error={error} />
      </div>
    </Modal>
  );
}

// ── Promote to code ─────────────────────────────────────────────────────────

export function PromoteDialog({
  title,
  result,
  pending,
  error,
  onClose,
  onRetry,
}: {
  title: string;
  result: PromotedDashboard | undefined;
  pending: boolean;
  error: unknown;
  onClose: () => void;
  onRetry: () => void;
}) {
  const [copied, setCopied] = useState(false);
  const fileName = result?.path.split("/").pop() || "dashboard.json";
  const download = () => {
    if (!result) return;
    const url = URL.createObjectURL(new Blob([result.content], { type: "application/json" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = fileName;
    a.click();
    URL.revokeObjectURL(url);
  };
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={`Promote ${title} to code`}
      description="Declared in the repo, a dashboard ships with every release to every environment, and becomes read-only here once that release is promoted."
      footer={
        <div className="flex flex-wrap justify-end gap-2">
          <button type="button" className={BUTTON_SECONDARY} onClick={onClose}>
            Close
          </button>
          {result && (
            <button
              type="button"
              className={BUTTON_SECONDARY}
              onClick={() =>
                void navigator.clipboard?.writeText(result.content).then(() => {
                  setCopied(true);
                  window.setTimeout(() => setCopied(false), 2000);
                })
              }
              data-testid="dashboard-promote-copy"
            >
              <Copy className="h-3.5 w-3.5" aria-hidden="true" />
              {copied ? "Copied" : "Copy"}
            </button>
          )}
          {result?.prUrl ? (
            <a href={result.prUrl} target="_blank" rel="noreferrer" className={`no-color ${BUTTON_PRIMARY}`} data-testid="dashboard-promote-pr">
              <GitPullRequest className="h-3.5 w-3.5" aria-hidden="true" />
              Open pull request
              <ExternalLink className="h-3 w-3" aria-hidden="true" />
            </a>
          ) : (
            result && (
              <button type="button" className={BUTTON_PRIMARY} onClick={download} data-testid="dashboard-promote-download">
                <Download className="h-3.5 w-3.5" aria-hidden="true" />
                Download {fileName}
              </button>
            )
          )}
        </div>
      }
    >
      <div className="space-y-3" data-testid="dashboard-promote">
        {pending && !result && <div className="h-40 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Preparing the file" />}
        {!!error && !result && (
          <div className="space-y-2">
            <ErrorLine error={error} />
            <button type="button" className={INK_LINK} onClick={onRetry}>
              Try again
            </button>
          </div>
        )}
        {result && (
          <>
            {!result.prUrl && (
              <p className="text-sm text-ink-muted" data-testid="dashboard-promote-no-pr">
                Opening the pull request needs the GitHub integration for dashboards, which isn&apos;t connected yet. Download the file and commit it at{" "}
                <span className="font-mono text-ink">{result.path}</span>.
              </p>
            )}
            {result.warnings.map((warning) => (
              <p key={warning} role="status" className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-warning-ink" data-testid="dashboard-promote-warning">
                {warning}
              </p>
            ))}
            <pre className="max-h-72 overflow-auto rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs leading-relaxed text-ink">{result.content}</pre>
          </>
        )}
      </div>
    </Modal>
  );
}

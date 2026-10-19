// Copyright (c) 2025 Reliant Labs

/**
 * The small pieces both Releases pages are built from: the env-cell mark, the
 * checks figure, a release's tags, a menu, a panel, the promotion chain and
 * the promote-target dialog. forge-ui primitives underneath (Status, Tag,
 * Card); the page-specific composition lives here so the list and the detail
 * page render a release identically.
 */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { MoreHorizontal } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import Modal from "@/components/forge-ui/modal";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { cn } from "@/lib/utils";

import { formatAgo } from "../Observability/format";
import { shortVersion, type ChecksSummary, type EnvCell, type ReleaseEnv, type ReleaseRow, type TimelineEntry } from "./releaseModel";

// ── Buttons ─────────────────────────────────────────────────────────────────

const BUTTON =
  "inline-flex h-8 items-center gap-1.5 whitespace-nowrap rounded-md px-3 text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50";
export const BUTTON_PRIMARY = `${BUTTON} border border-accent bg-accent text-on-accent hover:bg-accent-hover`;
export const BUTTON_SECONDARY = `${BUTTON} border border-border bg-surface text-ink hover:bg-ink/5`;
/**
 * A link in running text: ink with a quiet underline, never the accent (D8 —
 * the accent marks the primary action, focus and selection only). `no-color`
 * opts out of index.css's global `a:not(.no-color)` accent rule, which sits
 * in the utilities layer and would otherwise beat `text-ink`.
 */
export const INK_LINK =
  "no-color text-ink underline decoration-ink-muted/50 underline-offset-2 transition-colors hover:decoration-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";
/** An inline text button styled as INK_LINK. */
export const INK_BUTTON = "text-ink underline decoration-ink-muted/50 underline-offset-2 hover:decoration-ink";
export const ICON_BUTTON =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";

// ── Time ────────────────────────────────────────────────────────────────────

/** "5h ago" with the full time on hover, or nothing for an unknown time. */
export function Ago({ at, className }: { at?: string; className?: string }) {
  if (!at) return null;
  const ms = Date.parse(at);
  if (!Number.isFinite(ms)) return null;
  return (
    <time dateTime={at} title={new Date(ms).toLocaleString()} className={className}>
      {formatAgo(ms)}
    </time>
  );
}

// ── Env cells ───────────────────────────────────────────────────────────────

const CELL_WORDS: Record<EnvCell["state"], string> = {
  live: "live",
  partial: "live for some components",
  ran: "ran before",
  never: "never deployed",
};

/** ● live · ◐ partly live (scoped) · ○ ran before · · never — with the words for screen readers and on hover. */
export function EnvMark({ env, cell }: { env: string; cell: EnvCell }) {
  const title =
    cell.state === "partial" ? `Live in ${env} for ${cell.targets.join(", ")} only (scoped deploy)` : `${CELL_WORDS[cell.state]} in ${env}`;
  return (
    <span title={title} className={cn("text-sm", cell.state === "live" || cell.state === "partial" ? "text-ink" : "text-ink-muted")}>
      <span aria-hidden="true">{cell.state === "live" ? "●" : cell.state === "partial" ? "◐" : cell.state === "ran" ? "○" : "·"}</span>
      <span className="sr-only">{title}</span>
    </span>
  );
}

export function PreviewsMark({ names }: { names: string[] }) {
  if (names.length === 0) {
    return (
      <span className="text-ink-muted" title="Not running in any preview">
        <span aria-hidden="true">·</span>
        <span className="sr-only">Not running in any preview</span>
      </span>
    );
  }
  const title = `Live in ${names.join(", ")}`;
  return (
    <span className="text-sm text-ink" title={title}>
      <span aria-hidden="true">● +{names.length}</span>
      <span className="sr-only">{title}</span>
    </span>
  );
}

// ── Checks ──────────────────────────────────────────────────────────────────

/** "26/26" as a status, tinted only when something failed; a dash when nothing was recorded. */
export function ChecksMark({ checks }: { checks: ChecksSummary | null }) {
  if (!checks) {
    return (
      <span className="text-ink-muted" title="No checks were recorded on this release's promotions">
        —
      </span>
    );
  }
  const title = checks.gates.map((gate) => `${gate.name}: ${gate.status}${gate.summary ? ` (${gate.summary})` : ""}`).join("\n");
  const tone = checks.failed > 0 ? "danger" : checks.passed === checks.total ? "success" : "neutral";
  return (
    <span title={title}>
      <Status tone={tone} label={<span className="tabular-nums">{`${checks.passed}/${checks.total}`}</span>} />
    </span>
  );
}

// ── Tags ────────────────────────────────────────────────────────────────────

/** "admin-web", "admin-web +1": a scoped deploy's components, short enough for a tag (the full list is its title). */
export function scopedLabel(targets: string[]): string {
  return targets.length > 1 ? `${targets[0]} +${targets.length - 1}` : (targets[0] ?? "");
}

export function ReleaseTags({ row }: { row: Pick<ReleaseRow, "scoped" | "rolledBack"> }) {
  return (
    <>
      {row.scoped.length > 0 && (
        <Tag title={`A scoped deploy moved only ${row.scoped.join(", ")}`}>scoped: {scopedLabel(row.scoped)}</Tag>
      )}
      {row.rolledBack.map((env) => (
        <Tag key={env} tone="danger" title={`An older release replaced this one in ${env}`}>
          rolled back · {env}
        </Tag>
      ))}
    </>
  );
}

// ── Panels ──────────────────────────────────────────────────────────────────

/** A titled panel: the title is part of the panel (no rule under it); `flush` lets a table run to the edges. */
export function Panel({
  title,
  meta,
  actions,
  flush,
  children,
  testId,
  className,
}: {
  title?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  flush?: boolean;
  children: ReactNode;
  testId?: string;
  className?: string;
}) {
  return (
    <Card padding="none" data-testid={testId} className={cn("min-w-0", className)}>
      {(title || actions) && (
        <div className="flex items-center justify-between gap-3 px-4 pt-3">
          <h2 className="min-w-0 text-sm font-medium text-ink">
            {title}
            {meta && <span className="ml-2 text-xs font-normal text-ink-muted">{meta}</span>}
          </h2>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={flush ? (title || actions ? "mt-2" : "") : "p-4"}>{children}</div>
    </Card>
  );
}

/** A one-line note: a dot, a sentence, an optional action. */
export function Note({ tone = "neutral", children, testId }: { tone?: "neutral" | "warning" | "danger"; children: ReactNode; testId?: string }) {
  return (
    <div
      role={tone === "neutral" ? undefined : "status"}
      data-testid={testId}
      className="flex items-start gap-2 rounded-lg border border-border bg-surface px-3 py-2 text-sm text-ink-muted"
    >
      {tone !== "neutral" && <Status tone={tone} className="mt-1.5" />}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

/** Label / value pairs in a two-column grid (the detail rail). */
export function Facts({ items }: { items: [string, ReactNode][] }) {
  return (
    <dl className="grid grid-cols-[7rem_minmax(0,1fr)] gap-x-3 gap-y-2 text-xs">
      {items.map(([label, value]) => (
        <div key={label} className="contents">
          <dt className="text-ink-muted">{label}</dt>
          <dd className="min-w-0 text-ink">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

// ── Chain (timeline) ────────────────────────────────────────────────────────

const CHAIN_DOT: Record<TimelineEntry["tone"], string> = {
  ok: "border-success bg-success",
  bad: "border-danger bg-danger",
  neutral: "border-ink-muted bg-surface",
};

export function Chain({ entries, testId }: { entries: TimelineEntry[]; testId?: string }) {
  return (
    <ol className="space-y-3.5" data-testid={testId}>
      {entries.map((entry, index) => (
        <li key={entry.key} className="relative pl-6 text-xs" data-tone={entry.tone}>
          <span aria-hidden="true" className={cn("absolute left-1 top-1.5 h-2 w-2 rounded-full border", CHAIN_DOT[entry.tone])} />
          {index < entries.length - 1 && <span aria-hidden="true" className="absolute -bottom-3.5 left-2 top-4 w-px bg-border" />}
          <span data-part="title" className={cn("block", entry.tone === "bad" ? "text-danger-ink" : "text-ink")}>
            {entry.title}
          </span>
          <span className="block text-xs text-ink-muted">
            {entry.detail}
            {entry.detail && entry.at ? " · " : ""}
            <Ago at={entry.at} />
          </span>
        </li>
      ))}
    </ol>
  );
}

// ── Menu ────────────────────────────────────────────────────────────────────

export interface MenuItem {
  label: string;
  /** A navigation: rendered as a link. */
  href?: string;
  onSelect?: () => void;
  detail?: string;
  /** Shown, but cannot run; `hint` says why. */
  disabled?: boolean;
  hint?: string;
  testId?: string;
}

/** A "…" button and its menu (the DROPDOWN_STANDARDS outside-click pattern; Escape closes it). */
export function RowMenu({ label, items, testId }: { label: string; items: MenuItem[]; testId?: string }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    return () => document.removeEventListener("mousedown", onPointerDown);
  }, [open]);
  return (
    <div ref={ref} className="relative inline-flex">
      <button
        type="button"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
        className={ICON_BUTTON}
        data-testid={testId}
      >
        <MoreHorizontal className="h-4 w-4" aria-hidden="true" />
      </button>
      {open && (
        <div
          role="menu"
          {...escapeLayer}
          className="absolute right-0 top-8 z-50 min-w-52 rounded-lg border border-border bg-surface p-1 text-left shadow-md"
        >
          {items.map((item) => {
            const cls =
              "flex min-h-8 w-full items-center justify-between gap-3 rounded-md px-2 text-left text-sm text-ink transition-colors hover:bg-ink/5 focus-visible:bg-ink/5 focus-visible:outline-none disabled:cursor-not-allowed disabled:text-ink-muted disabled:hover:bg-transparent";
            const body = (
              <>
                <span>{item.label}</span>
                {item.detail && <span className="text-xs text-ink-muted">{item.detail}</span>}
              </>
            );
            return item.href && !item.disabled ? (
              <Link key={item.label} href={item.href} role="menuitem" data-testid={item.testId} onClick={() => setOpen(false)} className={`no-color ${cls}`}>
                {body}
              </Link>
            ) : (
              <button
                key={item.label}
                type="button"
                role="menuitem"
                data-testid={item.testId}
                disabled={item.disabled}
                aria-disabled={item.disabled}
                title={item.hint}
                onClick={() => {
                  setOpen(false);
                  item.onSelect?.();
                }}
                className={cls}
              >
                {body}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

// ── Promote target ──────────────────────────────────────────────────────────

/**
 * Where to promote a release: one radio per long-lived environment, each with
 * what it runs now. The confirm button names the environment (decision D1) —
 * and nothing is written here: it opens the plan, which shows the diff and
 * labels a move to an older release as a rollback before anything applies.
 */
export function PromoteTargetDialog({
  version,
  envs,
  bases,
  initialEnv,
  onCancel,
  onConfirm,
}: {
  version: string;
  envs: ReleaseEnv[];
  /** env name → the release live there now. */
  bases: ReadonlyMap<string, string | null>;
  initialEnv?: string;
  onCancel: () => void;
  onConfirm: (env: string) => void;
}) {
  const candidates = envs.filter((env) => env.kind === "long");
  const runsIt = (name: string) => bases.get(name) === version;
  // Only an environment that does not already run the release can be picked:
  // when every one does, nothing is, and the confirm stays disabled.
  const [picked, setPicked] = useState(() => {
    const preferred = candidates.find((env) => env.name === initialEnv && !runsIt(env.name));
    return (preferred ?? candidates.find((env) => !runsIt(env.name)))?.name ?? "";
  });
  const confirmable = picked !== "" && !runsIt(picked);
  const everywhere = candidates.length > 0 && candidates.every((env) => runsIt(env.name));
  return (
    <Modal
      open
      onClose={onCancel}
      size="sm"
      title={`Promote ${shortVersion(version)}`}
      description="Choose the environment. The plan is shown before anything applies."
      footer={
        <>
          <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
            Cancel
          </button>
          <button
            type="button"
            className={BUTTON_PRIMARY}
            disabled={!confirmable}
            onClick={() => confirmable && onConfirm(picked)}
            data-testid="promote-target-confirm"
          >
            {confirmable ? `Promote to ${picked}…` : "Promote…"}
          </button>
        </>
      }
    >
      <div className="space-y-2" data-testid="promote-target">
        <div role="radiogroup" aria-label="Environment" className="space-y-2">
          {candidates.map((env) => {
            const base = bases.get(env.name) ?? null;
            const here = runsIt(env.name);
            return (
              <label
                key={env.name}
                className={cn(
                  "flex h-10 cursor-pointer items-center gap-3 rounded-md border px-3 text-sm transition-colors",
                  picked === env.name ? "border-ink" : "border-border hover:border-ink-subtle",
                  here && "cursor-not-allowed opacity-60"
                )}
              >
                <input
                  type="radio"
                  name="promote-env"
                  value={env.name}
                  checked={picked === env.name}
                  disabled={here}
                  onChange={() => setPicked(env.name)}
                  className="h-3.5 w-3.5 accent-current"
                />
                <span className="w-16 font-medium text-ink">{env.name}</span>
                <span className="truncate text-xs text-ink-muted">
                  {here ? "already runs this release" : base ? `now ${shortVersion(base)}` : "nothing deployed yet"}
                </span>
              </label>
            );
          })}
        </div>
        {everywhere && (
          <p className="pt-1 text-xs text-ink-muted" data-testid="promote-target-everywhere">
            Every environment already runs this release.
          </p>
        )}
      </div>
    </Modal>
  );
}

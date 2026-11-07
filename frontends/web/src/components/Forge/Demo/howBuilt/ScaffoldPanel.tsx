// Copyright (c) 2025 Reliant Labs

/**
 * "What forge generated": the recorded run's manifest (fixtures.ts) as a
 * panel. The totals, the files by category (each expands to its list), a
 * click-through of one entity from the proto you write to the test forge
 * scaffolds, and a paragraph each on the contract, the schema and the
 * instrumentation. Every number on it is read from the manifest or the cast.
 */

import { useMemo, useState } from "react";
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp } from "lucide-react";

import { LightweightCodeViewer } from "@/components/Chat/LightweightCodeViewer";
import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";

import { CATEGORY_COPY, CHAIN_COPY, NAMED_PATHS } from "./copy";
import type { CategoryId, ManifestCategory, ScaffoldCast, ScaffoldManifest } from "./fixtures";

const n = (value: number) => value.toLocaleString("en-US");

/** The panel's numbers, all from the fixtures. */
export function summarize(manifest: ScaffoldManifest, cast: ScaffoldCast) {
  const t = manifest.totals;
  const cat = (id: CategoryId) => manifest.categories.find((c) => c.id === id);
  const lint = cast.steps.find((s) => s.kind === "command" && s.command.startsWith("forge lint"));
  const lintText = lint?.kind === "command" ? lint.events.map((e) => e[1]).join("") : "";
  const gating = /All (\d+) gating linters passed/.exec(lintText);
  const scaffold = manifest.by_step["forge scaffold"] ?? {};
  return {
    youLines: t.you_wrote_lines,
    youFiles: t.you_wrote_files,
    forgeLines: t.forge_lines,
    files: t.files,
    regenLines: t.regen_lines,
    machineLines: t.machine_lines,
    onceLines: t.forge_lines - t.regen_lines,
    newFiles: manifest.by_step.project_new?.files ?? 0,
    scaffoldAdded: scaffold.added ?? 0,
    scaffoldChanged: scaffold.changed ?? 0,
    lockfiles: manifest.excluded.find((e) => e.reason === "lockfile")?.files ?? 0,
    storeFiles: cat("store")?.files ?? 0,
    observabilityFiles: cat("observability")?.files ?? 0,
    gatingLinters: gating ? Number(gating[1]) : null,
  };
}

export function ScaffoldPanel({ manifest, cast }: { manifest: ScaffoldManifest; cast: ScaffoldCast }) {
  const s = useMemo(() => summarize(manifest, cast), [manifest, cast]);
  return (
    <section className="space-y-6" data-testid="scaffold-panel" aria-labelledby="scaffold-panel-title">
      <div className="space-y-2" data-testid="scaffold-totals">
        <h3 id="scaffold-panel-title" className="text-base font-semibold text-ink">
          What forge generated
        </h3>
        <p className="text-lg text-ink" data-testid="scaffold-headline">
          You wrote <strong>{n(s.youLines)}</strong> lines of proto in {s.youFiles} files. forge generated{" "}
          <strong>{n(s.forgeLines)}</strong> lines across <strong>{n(s.files)}</strong> files.
        </p>
        <p className="text-sm text-ink-muted">
          <code>forge project new</code> wrote {n(s.newFiles)} files; <code>forge scaffold</code> then added {n(s.scaffoldAdded)} and changed{" "}
          {n(s.scaffoldChanged)}. Of forge&apos;s lines, {n(s.regenLines)} are in files it rewrites on every <code>forge generate</code>,{" "}
          {n(s.machineLines)} of them the protobuf and Connect stubs buf compiles. It wrote the other {n(s.onceLines)} once: they are
          starting points, and yours to change. Not counted: {s.lockfiles} lockfiles, and forge&apos;s own state in <code>.forge/</code>.
        </p>
      </div>

      <CategoryBreakdown categories={manifest.categories} />
      <EntityChain manifest={manifest} />

      <div className="grid gap-4 md:grid-cols-3" data-testid="scaffold-explainers">
        <Explainer title="The contract">
          The protos are the wire truth. Each service is a Connect RPC service, so the same handler answers Connect, gRPC and gRPC-Web
          clients. buf compiles the protos into Go and TypeScript ({n(s.machineLines)} lines under <code>gen/</code>), and the
          handlers, the React hooks and the mocks are typed from that code. Change a field and the compiler shows you every place that
          needs to follow.
        </Explainer>
        <Explainer title="The schema">
          The SQL migrations are the schema truth. forge writes a table&apos;s first migration from its entity message and never edits
          it again. On every generate it applies all the migrations to a throwaway Postgres and reads the result back, so the ORM (
          {s.storeFiles} files in <code>internal/db</code>) is a projection of a schema that exists. The wire and the table change on
          their own schedules, on purpose.
        </Explainer>
        <Explainer title="The instrumentation">
          The server&apos;s start-up (<code>{NAMED_PATHS.serve}</code>) sets up OpenTelemetry traces and metrics. forge&apos;s Connect
          interceptors put a span on every rpc and carry the trace to the next service, the database driver adds one per query, and the
          browser app (<code>{NAMED_PATHS.browserTelemetry}</code>) sends its own spans and errors. <code>deploy/observability</code>{" "}
          holds {s.observabilityFiles} files of dashboards, default alerts and collector config.
          <span className="mt-2 block">
            That matters to an LLM as much as to you. Your agent reads the same telemetry from a terminal with <code>forge obs</code>.{" "}
            <code>{NAMED_PATHS.agents}</code>, which forge wrote into the project, points it at forge&apos;s skills before it guesses.{" "}
            {s.gatingLinters !== null ? (
              <>
                <code>forge lint</code> ran {s.gatingLinters} gating linters on this app before it built, and <code>forge lint --tracing</code>{" "}
                points out where a trace would break.
              </>
            ) : (
              <>
                <code>forge lint --tracing</code> points out where a trace would break.
              </>
            )}
          </span>
        </Explainer>
      </div>
    </section>
  );
}

function Explainer({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-border p-3">
      <h4 className="mb-1 text-sm font-semibold text-ink">{title}</h4>
      <p className="text-sm text-ink-muted">{children}</p>
    </div>
  );
}

function CategoryBreakdown({ categories }: { categories: ManifestCategory[] }) {
  const [open, setOpen] = useState<CategoryId | null>(null);
  const rows = CATEGORY_COPY.flatMap((copy) => {
    const cat = categories.find((c) => c.id === copy.id);
    return cat ? [{ ...copy, cat }] : [];
  });
  const widest = Math.max(...rows.map((r) => r.cat.lines));
  return (
    <div data-testid="scaffold-categories">
      <h4 className="mb-2 text-sm font-semibold text-ink">By category</h4>
      <ul className="divide-y divide-border rounded-lg border border-border">
        {rows.map(({ id, label, detail, cat }) => {
          const expanded = open === id;
          return (
            <li key={id} data-testid={`scaffold-category-${id}`}>
              <button
                type="button"
                className="grid w-full grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 px-3 py-2 text-left hover:bg-surface-muted md:grid-cols-[14rem_minmax(0,1fr)_auto_auto]"
                aria-expanded={expanded}
                onClick={() => setOpen(expanded ? null : id)}
              >
                <span className="min-w-0">
                  <span className="block text-sm font-medium text-ink">{label}</span>
                  <span className="block truncate text-xs text-ink-muted">{detail}</span>
                </span>
                <span className="hidden h-2 rounded-full bg-surface-sunken md:block" aria-hidden="true">
                  <span className="block h-2 rounded-full bg-primary" style={{ width: `${Math.max(2, (cat.lines / widest) * 100)}%` }} />
                </span>
                <span className="whitespace-nowrap text-right font-mono text-xs text-ink-muted" data-testid="scaffold-category-count">
                  {n(cat.lines)} lines · {cat.files} {cat.files === 1 ? "file" : "files"}
                </span>
                {expanded ? <ChevronUp className="h-4 w-4 text-ink-muted" aria-hidden="true" /> : <ChevronDown className="h-4 w-4 text-ink-muted" aria-hidden="true" />}
              </button>
              {expanded && (
                <ul className="max-h-64 overflow-y-auto border-t border-border bg-surface-sunken px-3 py-1 font-mono text-xs" data-testid="scaffold-category-files">
                  {[...cat.entries]
                    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
                    .map(([path, lines, yours, own]) => (
                      <li key={path} className="flex items-center gap-2 py-0.5">
                        <span className="min-w-0 flex-1 truncate text-ink" title={path}>
                          {path}
                        </span>
                        {yours > 0 && <span className="text-primary-ink">{yours} yours</span>}
                        <span className="text-ink-muted">{own === "regen" ? "regenerated" : "written once"}</span>
                        <span className="w-12 text-right text-ink-muted">{n(lines)}</span>
                      </li>
                    ))}
                </ul>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** The code viewer's names for the excerpts' languages (it has no proto grammar). */
const VIEWER_LANG: Record<string, string> = { protobuf: "plaintext", sql: "sql", go: "go", typescript: "typescript" };

function EntityChain({ manifest }: { manifest: ScaffoldManifest }) {
  const chain = manifest.chain;
  const [index, setIndex] = useState(0);
  const excerpt = chain[index];
  const copy = CHAIN_COPY[excerpt.id];
  return (
    <div data-testid="scaffold-chain">
      <h4 className="mb-1 text-sm font-semibold text-ink">One entity, end to end: Order</h4>
      <p className="mb-2 text-sm text-ink-muted">From the proto you write to the test forge scaffolds, all from the same run.</p>
      <div role="tablist" aria-label="The Order entity, step by step" className="mb-2 flex flex-wrap gap-1">
        {chain.map((step, i) => (
          <button
            key={step.id}
            type="button"
            role="tab"
            aria-selected={i === index}
            onClick={() => setIndex(i)}
            className={cn(
              "rounded-md px-2 py-1 text-xs font-medium",
              // The app colours [aria-selected] with primary-ink (index.css); the selected tab is a primary tint to match.
              i === index ? "bg-primary/10 text-primary-ink ring-1 ring-primary/30" : "bg-surface-muted text-ink-muted hover:text-ink"
            )}
            data-testid={`scaffold-chain-tab-${step.id}`}
          >
            {i + 1}. {CHAIN_COPY[step.id].title}
          </button>
        ))}
      </div>
      <div role="tabpanel" className="rounded-lg border border-border" data-testid="scaffold-chain-panel" data-excerpt={excerpt.id}>
        <div className="flex flex-wrap items-baseline gap-x-3 border-b border-border px-3 py-2">
          <span className="text-sm font-medium text-ink">{copy.title}</span>
          <code className="text-xs text-ink-muted" data-testid="scaffold-chain-path">
            {excerpt.path}:{excerpt.start_line}–{excerpt.end_line}
            {excerpt.truncated ? ` (its first ${excerpt.end_line - excerpt.start_line + 1} lines)` : ""}
          </code>
        </div>
        <p className="px-3 pt-2 text-sm text-ink-muted">{copy.body}</p>
        <div className="p-2">
          <LightweightCodeViewer content={excerpt.code} language={VIEWER_LANG[excerpt.lang] ?? "plaintext"} maxHeight={360} noBorder />
        </div>
        <div className="flex items-center justify-between border-t border-border px-3 py-2">
          <Button size="sm" variant="ghost" leftIcon={<ChevronLeft />} disabled={index === 0} onClick={() => setIndex(index - 1)} data-testid="scaffold-chain-prev">
            Back
          </Button>
          <span className="text-xs text-ink-muted">
            {index + 1} of {chain.length}
          </span>
          <Button
            size="sm"
            variant="ghost"
            rightIcon={<ChevronRight />}
            disabled={index === chain.length - 1}
            onClick={() => setIndex(index + 1)}
            data-testid="scaffold-chain-next"
          >
            Next
          </Button>
        </div>
      </div>
    </div>
  );
}

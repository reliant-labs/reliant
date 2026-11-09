// Copyright (c) 2025 Reliant Labs

/**
 * "What forge generated": the recorded run's manifest (fixtures.ts) as a
 * panel. The totals, the files by category (each expands to its list), a
 * click-through of one entity from the proto you write to the test forge
 * scaffolds, and a paragraph each on the contract, the schema and the
 * instrumentation. Every number on it is read from the manifest or the cast.
 */

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronLeft, ChevronRight, ChevronUp } from "lucide-react";

import { LightweightCodeViewer } from "@/components/shared/LightweightCodeViewer";
import Button from "@/components/forge-ui/button";
import { cn } from "@/lib/utils";

import { CATEGORY_COPY, CHAIN_COPY, NAMED_PATHS } from "./copy";
import type { CategoryId, ManifestCategory, ScaffoldCast, ScaffoldManifest } from "./fixtures";
import { categoryOwnership, recordedOwnership, OwnershipBadge, OwnershipGuide } from "./ownership";

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
    <section className="space-y-10" data-testid="scaffold-panel" aria-labelledby="scaffold-panel-title">
      <div className="space-y-4 rounded-xl border border-border bg-surface p-4 sm:p-6" data-testid="scaffold-totals">
        <h2 id="scaffold-panel-title" className="text-xl font-semibold text-ink sm:text-2xl">
          What Forge generated
        </h2>
        <p className="text-lg text-ink" data-testid="scaffold-headline">
          <strong>{n(s.youLines)}</strong> lines of API definitions in {s.youFiles} files. Forge generated{" "}
          <strong>{n(s.forgeLines)}</strong> lines across <strong>{n(s.files)}</strong> files.
        </p>
        <p className="text-base leading-relaxed text-ink">Your agent customizes the starting files and implements your app&apos;s rules. Regenerated files come from their inputs instead of direct edits. The project and its code remain yours.</p>
        <div className="grid gap-4 rounded-lg border border-border bg-surface-sunken p-4 sm:grid-cols-2" aria-label="Code ownership in this recording">
          <div className="space-y-2"><OwnershipBadge ownership="regen" /><p className="text-sm text-ink">Change the source inputs, then regenerate. Direct edits are overwritten.</p></div>
          <div className="space-y-2"><OwnershipBadge ownership="yours" /><p className="text-sm text-ink">Starting code your agent can edit directly. You own and control the project.</p></div>
        </div>
        <details className="border-t border-border pt-2">
          <summary className="min-h-[44px] cursor-pointer py-3 text-sm font-medium text-ink">How these files are counted and maintained</summary>
        <p className="text-sm leading-relaxed text-ink-muted">
          <code>forge project new</code> wrote {n(s.newFiles)} files; <code>forge scaffold</code> then added {n(s.scaffoldAdded)} and changed{" "}
          {n(s.scaffoldChanged)}. Of Forge&apos;s lines, {n(s.regenLines)} are in files it rewrites on every <code>forge generate</code>:
          change their inputs rather than editing those files. These include {n(s.machineLines)} lines of generated Go and TypeScript API code.
          It wrote the other {n(s.onceLines)} once: starting code and pages your agent can customize, not a finished product.
          Not counted: {s.lockfiles} dependency lockfiles, and Forge&apos;s own working files in <code>.forge/</code>.
        </p>
        </details>
      </div>

      <CategoryBreakdown categories={manifest.categories} />
      <EntityChain manifest={manifest} />

      <div className="grid gap-4 md:grid-cols-3" data-testid="scaffold-explainers">
        <Explainer title="One API definition">
          The API files describe the requests your app accepts and the responses it returns. Forge uses them to generate matching Go and
          TypeScript code ({n(s.machineLines)} lines under <code>gen/</code>) for the server, browser and tests. When a definition changes,
          type checks help find code that no longer matches.
        </Explainer>
        <Explainer title="Database changes you control">
          Forge creates the first database setup file from your data definition. Later changes go in new SQL migration files:
          instructions for updating the database. Forge applies those files to a temporary Postgres database and generates matching Go
          database code ({s.storeFiles} files in <code>internal/db</code>). Editing an API definition alone does not update an existing table.
        </Explainer>
        <Explainer title="Monitoring built in">
          Forge sets up request timelines, performance measurements and error reporting across the server (<code>{NAMED_PATHS.serve}</code>),
          database and browser (<code>{NAMED_PATHS.browserTelemetry}</code>). <code>deploy/observability</code> holds {s.observabilityFiles}{" "}
          files of dashboards, default alerts and settings for collecting this information.
          <span className="mt-2 block">
            On your running app, you and your agent can inspect the same health information with <code>forge obs</code>.{" "}
            <code>{NAMED_PATHS.agents}</code> points your agent to Forge&apos;s instructions.{" "}
            {s.gatingLinters !== null ? (
              <>
                In this recording, <code>forge lint</code> passed {s.gatingLinters} required checks before the Go build.{" "}
              </>
            ) : null}
            <code>forge lint --tracing</code> checks for common gaps in request tracking.
          </span>
        </Explainer>
      </div>
    </section>
  );
}

function Explainer({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-xl border border-border bg-surface p-4 sm:p-5">
      <h3 className="mb-3 text-lg font-semibold text-ink">{title}</h3>
      <p className="text-sm leading-relaxed text-ink">{children}</p>
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
    <div className="space-y-4 border-t border-border pt-8" data-testid="scaffold-categories">
      <h2 className="text-xl font-semibold text-ink sm:text-2xl">Explore the generated files</h2>
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
                  <span className="mt-1 block"><OwnershipBadge ownership={categoryOwnership(cat)} /></span>
                  <span className="mt-1 block text-xs text-ink-muted">{detail}</span>
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
                      <li key={path} className="flex flex-wrap items-center gap-2 border-b border-border py-2 last:border-0">
                        <span className="min-w-0 flex-1 truncate text-ink" title={path}>
                          {path}
                        </span>
                        {yours > 0 && <span className="text-primary-ink">{yours} yours</span>}
                        <OwnershipBadge ownership={own} />
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
  const id = useId();
  const tabs = useRef<(HTMLButtonElement | null)[]>([]);
  const rail = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const selected = tabs.current[index];
    const host = rail.current;
    if (!selected || !host) return;
    // Reveal only within this horizontal rail; never move the page vertically.
    const item = selected.getBoundingClientRect();
    const viewport = host.getBoundingClientRect();
    if (item.left < viewport.left) host.scrollLeft -= viewport.left - item.left + 12;
    else if (item.right > viewport.right) host.scrollLeft += item.right - viewport.right + 12;
  }, [index]);
  const excerpt = chain[index];
  const copy = CHAIN_COPY[excerpt.id];
  const ownership = recordedOwnership(manifest, excerpt.path);
  return (
    <div className="space-y-4 border-t border-border pt-8" data-testid="scaffold-chain">
      <h2 className="text-xl font-semibold text-ink sm:text-2xl">Follow an order through the generated code</h2>
      <p className="text-sm leading-relaxed text-ink">From the API&apos;s order data to database setup, server and browser code, and a test—all from this recording.</p>
      <p className="text-sm font-medium text-ink" aria-live="polite">Step {index + 1} of {chain.length}: {copy.title}</p>
      <div ref={rail} role="tablist" aria-label="The Order example, step by step" className="flex gap-2 overflow-x-auto rounded-xl border border-border bg-surface-sunken p-3 xl:grid xl:grid-cols-6" onKeyDown={(event) => {
        let next = index;
        if (event.key === "ArrowRight") next = (index + 1) % chain.length;
        else if (event.key === "ArrowLeft") next = (index - 1 + chain.length) % chain.length;
        else if (event.key === "Home") next = 0;
        else if (event.key === "End") next = chain.length - 1;
        else return;
        event.preventDefault(); setIndex(next); tabs.current[next]?.focus();
      }}>
        {chain.map((step, i) => (
          <button
            key={step.id}
            type="button"
            role="tab"
            id={`${id}-step-${i}`}
            aria-controls={`${id}-panel`}
            aria-selected={i === index}
            tabIndex={i === index ? 0 : -1}
            ref={(el) => { tabs.current[i] = el; }}
            onClick={() => setIndex(i)}
            className={cn(
              "flex min-h-[56px] min-w-44 shrink-0 items-center gap-3 rounded-lg border px-3 py-3 text-left text-sm font-semibold focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring xl:min-w-0 xl:flex-col xl:items-start xl:gap-2",
              i === index ? "border-primary bg-surface text-ink" : "border-border bg-surface text-ink hover:bg-ink/5"
            )}
            data-testid={`scaffold-chain-tab-${step.id}`}
          >
            <span aria-hidden="true" className={cn("flex h-8 w-8 shrink-0 items-center justify-center rounded-full border text-sm", i === index ? "border-primary bg-primary text-primary-foreground" : "border-border text-ink")}>{i + 1}</span>
            <span>{CHAIN_COPY[step.id].title}<span className="mt-1 block text-xs font-normal text-ink-muted">{i === index ? "Current step" : i < index ? "Previous step" : i === index + 1 ? "Next step" : "Upcoming"}</span></span>
            {i < chain.length - 1 && <ChevronRight className="ml-auto h-4 w-4 shrink-0 text-ink-muted xl:hidden" aria-hidden="true" />}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={`${id}-panel`} aria-labelledby={`${id}-step-${index}`} tabIndex={0} className="rounded-xl border border-border bg-surface" data-testid="scaffold-chain-panel" data-excerpt={excerpt.id}>
        <div className="flex flex-wrap items-baseline gap-x-3 border-b border-border px-3 py-2">
          <span className="text-sm font-medium text-ink">{copy.title}</span>
          <OwnershipBadge ownership={ownership} />
          <code className="break-all text-xs text-ink-muted" data-testid="scaffold-chain-path">
            {excerpt.path}:{excerpt.start_line}–{excerpt.end_line}
            {excerpt.truncated ? ` (its first ${excerpt.end_line - excerpt.start_line + 1} lines)` : ""}
          </code>
        </div>
        <p className="px-4 py-4 text-base leading-relaxed text-ink">{copy.body}</p>
        <div className="border-y border-border bg-surface-sunken px-4 py-3"><OwnershipGuide ownership={ownership} /></div>
        <div className="p-2">
          <LightweightCodeViewer content={excerpt.code} language={VIEWER_LANG[excerpt.lang] ?? "plaintext"} maxHeight={360} noBorder />
        </div>
        <div className="flex items-center justify-between border-t border-border px-3 py-2">
          <Button className="min-h-[44px]" variant="outline" leftIcon={<ChevronLeft />} disabled={index === 0} onClick={() => setIndex(index - 1)} data-testid="scaffold-chain-prev">
            Back
          </Button>
          <span className="text-xs text-ink-muted">
            {index + 1} of {chain.length}
          </span>
          <Button
            className="min-h-[44px]"
            variant="primary"
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

// Copyright (c) 2026 Reliant Labs

/**
 * The lineage around a subject (GetMlLineage): inputs → run phases →
 * checkpoints → model versions → releases → services, left to right. Every
 * node is a link to its page with its status; an edge names its relation on
 * hover; the subject the page is about is outlined. It reads its own data,
 * so any page can drop it in (a model, a version, a run's Config & lineage
 * tab, an inference service).
 */

import { useMemo, useState } from "react";

import Link from "@/components/forge-ui/link";
import type { MlSubject } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlRead } from "../MlParts";
import { EmptyNote, StatusDot, subjectHref, useMl } from "../kit";
import { edgeText, focusIds, layoutLineage, NODE_H, NODE_W, statusTone, type LineageLayout } from "./lineageModel";

function Graph({ layout, focus }: { layout: LineageLayout; focus: Set<string> }) {
  const [hover, setHover] = useState<string | null>(null);
  const hovered = layout.edges.find((e) => e.key === hover);
  // A hovered node lights its edges.
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  return (
    <div className="min-w-0 overflow-x-auto pb-2" data-testid="ml-lineage">
      <div className="relative" style={{ width: layout.width, height: layout.height + 8 }}>
        {layout.headers.map((h) => (
          <div key={h.label} className="absolute top-0 border-b border-border-subtle pb-1 text-meta font-medium text-ink-muted" style={{ left: h.x, width: h.width }}>
            {h.label}
          </div>
        ))}
        <svg className="absolute inset-0 overflow-visible" width={layout.width} height={layout.height + 8} aria-hidden="true">
          {layout.edges.map((e) => {
            const lit = e.key === hover || (hoverNode !== null && (e.edge.from === hoverNode || e.edge.to === hoverNode)) || (focus.has(e.edge.from) && focus.has(e.edge.to));
            return (
              <g key={e.key}>
                <path d={e.path} fill="none" stroke={lit ? "hsl(var(--primary))" : "hsl(var(--border))"} strokeWidth={lit ? 1.75 : 1.25} />
                {/* A wide transparent twin is what the pointer finds. */}
                <path d={e.path} fill="none" stroke="transparent" strokeWidth={10} className="pointer-events-auto cursor-help" onMouseEnter={() => setHover(e.key)} onMouseLeave={() => setHover(null)}>
                  <title>{edgeText(e.edge)}</title>
                </path>
              </g>
            );
          })}
        </svg>
        {layout.nodes.map((p) => {
          const href = p.node.subject ? subjectHref(p.node.subject) : null;
          const focused = focus.has(p.id);
          const body = (
            <>
              <span className="flex min-w-0 items-center gap-1.5">
                {p.node.status && <StatusDot tone={statusTone(p.node.status)} pulse={p.node.status === "running"} />}
                <span className="truncate text-ui font-medium text-ink">{p.node.title}</span>
              </span>
              <span className="block truncate text-meta text-ink-muted">{p.node.subtitle}</span>
            </>
          );
          const cls = cn(
            "absolute flex flex-col justify-center rounded-md border bg-surface px-2.5 py-1",
            focused ? "border-primary ring-1 ring-primary" : "border-border",
            href && "hover:border-ink-muted",
          );
          const style = { left: p.x, top: p.y, width: NODE_W, height: NODE_H };
          const events = { onMouseEnter: () => setHoverNode(p.id), onMouseLeave: () => setHoverNode(null) };
          return href ? (
            <Link key={p.id} href={href} className={cls} style={style} title={`${p.node.title} — ${p.node.subtitle}`} data-testid={`ml-lineage-node-${p.id}`} {...events}>
              {body}
            </Link>
          ) : (
            <div key={p.id} className={cls} style={style} title={`${p.node.title} — ${p.node.subtitle}`} data-testid={`ml-lineage-node-${p.id}`} {...events}>
              {body}
            </div>
          );
        })}
        {hovered && (
          <div
            className="pointer-events-none absolute -translate-x-1/2 -translate-y-full whitespace-nowrap rounded-md border border-border bg-surface px-2 py-0.5 text-meta text-ink shadow-sm"
            style={{ left: hovered.mid.x, top: hovered.mid.y - 4 }}
            data-testid="ml-lineage-edge-label"
          >
            {edgeText(hovered.edge)}
          </div>
        )}
      </div>
    </div>
  );
}

export function LineageGraph({ subject }: { subject: Pick<MlSubject, "kind" | "id"> }) {
  const q = useMl("getMlLineage", { subject: { kind: subject.kind, id: subject.id } });
  return (
    <MlRead query={q} what="lineage">
      {(d) => <LineageBody nodes={d.nodes} edges={d.edges} subject={subject} />}
    </MlRead>
  );
}

function LineageBody({ nodes, edges, subject }: { nodes: Parameters<typeof layoutLineage>[0]; edges: Parameters<typeof layoutLineage>[1]; subject: Pick<MlSubject, "kind" | "id"> }) {
  const layout = useMemo(() => layoutLineage(nodes, edges), [nodes, edges]);
  const focus = useMemo(() => focusIds(subject, nodes), [subject, nodes]);
  if (nodes.length === 0) return <EmptyNote>No lineage recorded for this subject: nothing it was trained from or became has been reported.</EmptyNote>;
  return <Graph layout={layout} focus={focus} />;
}

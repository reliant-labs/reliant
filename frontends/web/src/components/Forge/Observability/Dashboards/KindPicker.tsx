// Copyright (c) 2025 Reliant Labs

/**
 * The panel builder's "what is this panel" picker: every kind forge's spec
 * has, each a card with a thumbnail of what it draws, its name and a line on
 * what it is for. One radio group, so arrow keys move between cards.
 *
 * The thumbnails are schematic on purpose: a few primitive shapes in the
 * text colour, so they read as "a line chart", not as data.
 */

import { useId, useRef, type KeyboardEvent, type ReactNode } from "react";
import { Bug, Code2, Database, List, MonitorPlay, Type } from "lucide-react";

import { cn } from "@/lib/utils";

import { VISUALIZATIONS, type Visualization } from "./panelBuilder";

const W = 48;
const H = 28;

function Chart({ children }: { children: ReactNode }) {
  return (
    <svg viewBox={`0 0 ${W} ${H}`} width={W} height={H} aria-hidden="true" className="shrink-0 overflow-visible">
      {children}
    </svg>
  );
}

function Icon({ icon: Glyph }: { icon: typeof Bug }) {
  return (
    <span className="flex h-7 w-12 shrink-0 items-center justify-center" aria-hidden="true">
      <Glyph className="h-4 w-4" />
    </span>
  );
}

/** A schematic of what `kind` draws. */
export function KindThumbnail({ kind }: { kind: string }) {
  switch (kind) {
    case "line":
      return (
        <Chart>
          <polyline points="2,22 10,15 18,18 26,9 34,13 46,4" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" />
          <line x1="2" y1="26" x2="46" y2="26" stroke="currentColor" strokeOpacity="0.3" />
        </Chart>
      );
    case "number":
      return (
        <Chart>
          <text x="24" y="20" textAnchor="middle" fontSize="15" fontWeight="600" fill="currentColor" className="font-mono">
            42
          </text>
        </Chart>
      );
    case "bar":
      return (
        <Chart>
          {[16, 11, 8, 5].map((width, i) => (
            <rect key={i} x="4" y={3 + i * 6} width={width * 2.5} height="4" rx="1" fill="currentColor" fillOpacity={1 - i * 0.18} />
          ))}
        </Chart>
      );
    case "table":
      return (
        <Chart>
          <rect x="4" y="3" width="40" height="4" rx="1" fill="currentColor" fillOpacity="0.55" />
          {[11, 17, 23].map((y) => (
            <line key={y} x1="4" y1={y} x2="44" y2={y} stroke="currentColor" strokeOpacity="0.5" />
          ))}
        </Chart>
      );
    case "heatmap":
      return (
        <Chart>
          {Array.from({ length: 18 }, (_, i) => {
            const col = i % 6;
            const row = Math.floor(i / 6);
            const heat = [0.2, 0.5, 0.9, 0.35, 0.7, 0.15][(col + row * 2) % 6];
            return <rect key={i} x={3 + col * 7} y={3 + row * 8} width="6" height="7" rx="1" fill="currentColor" fillOpacity={heat} />;
          })}
        </Chart>
      );
    case "funnel":
      return (
        <Chart>
          {[40, 28, 18, 10].map((width, i) => (
            <rect key={i} x={24 - width / 2} y={2 + i * 6.5} width={width} height="5" rx="1" fill="currentColor" fillOpacity={1 - i * 0.18} />
          ))}
        </Chart>
      );
    case "search":
      return <Icon icon={List} />;
    case "sql":
      return <Icon icon={Code2} />;
    case "postgres":
      return <Icon icon={Database} />;
    case "text":
      return <Icon icon={Type} />;
    case "issues":
      return <Icon icon={Bug} />;
    case "sessions":
      return <Icon icon={MonitorPlay} />;
    default:
      return <Icon icon={Code2} />;
  }
}

export function KindPicker({ value, onChange, testId = "panel-builder-kind" }: { value: string; onChange: (kind: Visualization) => void; testId?: string }) {
  const id = useId();
  const group = useRef<HTMLDivElement>(null);
  const index = Math.max(0, VISUALIZATIONS.findIndex((v) => v.kind === value));
  const move = (event: KeyboardEvent<HTMLButtonElement>) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[event.key];
    if (!step) return;
    event.preventDefault();
    const next = VISUALIZATIONS[(index + step + VISUALIZATIONS.length) % VISUALIZATIONS.length];
    onChange(next.kind);
    group.current?.querySelector<HTMLButtonElement>(`[data-value="${next.kind}"]`)?.focus();
  };
  return (
    <div ref={group} role="radiogroup" aria-label="Visualization" className="grid grid-cols-1 gap-1.5 sm:grid-cols-3" data-testid={testId}>
      {VISUALIZATIONS.map((v) => {
        const selected = v.kind === value;
        return (
          <button
            key={v.kind}
            type="button"
            role="radio"
            aria-checked={selected}
            aria-label={v.label}
            aria-describedby={`${id}-${v.kind}`}
            tabIndex={v === VISUALIZATIONS[index] ? 0 : -1}
            onClick={() => onChange(v.kind)}
            onKeyDown={move}
            data-value={v.kind}
            data-testid={`panel-kind-${v.kind}`}
            className={cn(
              "flex min-w-0 items-start gap-2 rounded-md border p-2 text-left transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
              selected ? "border-ink/40 bg-ink/8 text-ink" : "border-border bg-surface text-ink-muted hover:border-ink/25 hover:text-ink"
            )}
          >
            <KindThumbnail kind={v.kind} />
            <span className="min-w-0">
              <span className={cn("block text-xs leading-snug", selected ? "font-medium text-ink" : "text-ink")}>{v.label}</span>
              <span id={`${id}-${v.kind}`} className="mt-0.5 block break-words text-2xs leading-snug text-ink-muted">
                {v.description}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

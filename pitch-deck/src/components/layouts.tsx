import { Check, X } from "lucide-react";
import type { SlideData } from "../types";
import { Eyebrow, Title, Footnote, GradientRule } from "./primitives";

/**
 * One component per layout archetype. Each reads the fields of SlideData that
 * its layout needs. All share the fade-up entrance and reference brand tokens.
 */

function Lead({ text }: { text: string }) {
  return (
    <p className="text-2xl font-medium leading-snug text-muted-strong">
      {text}
    </p>
  );
}

// --- centered-hero ----------------------------------------------------------
export function CenteredHero({ data }: { data: SlideData }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-6 px-24 text-center">
      {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
      <Title className="text-6xl leading-[1.05]">{data.title}</Title>
      <GradientRule className="my-2" />
      {data.subtitle && (
        <p className="max-w-3xl text-2xl leading-relaxed text-muted-strong">
          {data.subtitle}
        </p>
      )}
      {data.footnote && (
        <div className="absolute bottom-10 left-0 right-0 flex justify-center px-24">
          <Footnote>{data.footnote}</Footnote>
        </div>
      )}
    </div>
  );
}

// --- two-column -------------------------------------------------------------
export function TwoColumn({ data }: { data: SlideData }) {
  const [left, right] = data.columns ?? [];
  return (
    <div className="flex h-full flex-col justify-center gap-10 px-24">
      <div className="flex flex-col gap-4">
        {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
        <Title className="text-5xl leading-tight">{data.title}</Title>
      </div>
      <div className="grid grid-cols-2 gap-8">
        {[left, right].map((col, i) =>
          col ? (
            <div
              key={i}
              className="flex flex-col gap-3 rounded-xl border border-border bg-surface/60 p-8"
            >
              {col.eyebrow && (
                <p className="text-sm font-semibold uppercase tracking-wide text-brand-purple">
                  {col.eyebrow}
                </p>
              )}
              {col.heading && (
                <h2 className="text-2xl font-semibold text-foreground">
                  {col.heading}
                </h2>
              )}
              {col.body && (
                <p className="text-lg leading-relaxed text-muted-strong">
                  {col.body}
                </p>
              )}
            </div>
          ) : null,
        )}
      </div>
      {data.lead && (
        <p className="text-2xl font-medium text-gradient">{data.lead}</p>
      )}
      {data.footnote && <Footnote>{data.footnote}</Footnote>}
    </div>
  );
}

// --- card-grid --------------------------------------------------------------
export function CardGrid({ data }: { data: SlideData }) {
  const cards = data.cards ?? [];
  // 2 or 4 cards -> 2 cols; 3 cards -> 3 cols.
  const cols = cards.length === 3 ? "grid-cols-3" : "grid-cols-2";
  return (
    <div className="flex h-full flex-col justify-center gap-10 px-24">
      <div className="flex flex-col gap-4">
        {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
        <Title className="text-5xl leading-tight">{data.title}</Title>
      </div>
      <div className={`grid gap-6 ${cols}`}>
        {cards.map((card, i) => {
          const Icon = card.icon;
          return (
            <div
              key={i}
              className="flex flex-col gap-4 rounded-xl border border-border bg-surface/60 p-7"
            >
              {Icon && (
                <div className="flex h-12 w-12 items-center justify-center rounded-lg bg-brand-gradient">
                  <Icon
                    className="h-6 w-6 text-white"
                    aria-hidden="true"
                    strokeWidth={2}
                  />
                </div>
              )}
              <h2 className="text-xl font-semibold text-foreground">
                {card.title}
              </h2>
              <p className="text-base leading-relaxed text-muted-strong">
                {card.body}
              </p>
            </div>
          );
        })}
      </div>
      {data.footnote && <Footnote>{data.footnote}</Footnote>}
    </div>
  );
}

// --- full-bleed-stat --------------------------------------------------------
export function FullBleedStat({ data }: { data: SlideData }) {
  const stat = data.stat;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-6 px-24 text-center">
      {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
      <h1 className="max-w-4xl whitespace-pre-line text-4xl font-bold leading-tight text-foreground">
        {data.title}
      </h1>
      {stat && (
        <>
          <p className="text-gradient text-[10rem] font-extrabold leading-none">
            {stat.value}
          </p>
          <p className="max-w-2xl text-2xl font-medium text-muted-strong">
            {stat.label}
          </p>
          {stat.source && (
            <p className="max-w-2xl text-sm leading-relaxed text-muted">
              {stat.source}
            </p>
          )}
        </>
      )}
    </div>
  );
}

// --- comparison-table -------------------------------------------------------
export function ComparisonTableLayout({ data }: { data: SlideData }) {
  const table = data.table;
  return (
    <div className="flex h-full flex-col justify-center gap-8 px-24">
      <div className="flex flex-col gap-4">
        {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
        <Title className="text-4xl leading-tight">{data.title}</Title>
      </div>
      {table && (
        <table className="w-full border-collapse text-left">
          <thead>
            <tr className="border-b border-border-strong">
              <th className="py-4 pr-6" />
              {table.columns.map((col, i) => (
                <th
                  key={i}
                  className={`py-4 pl-6 text-xl font-semibold ${
                    i === table.columns.length - 1
                      ? "text-gradient"
                      : "text-muted"
                  }`}
                >
                  {col}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {table.rows.map((row, r) => (
              <tr key={r} className="border-b border-border">
                <th
                  scope="row"
                  className="py-4 pr-6 text-lg font-medium text-muted-strong"
                >
                  {row.label}
                </th>
                {row.cells.map((cell, c) => (
                  <td key={c} className="py-4 pl-6 text-lg">
                    {typeof cell === "boolean" ? (
                      cell ? (
                        <Check
                          className="h-6 w-6 text-success"
                          aria-label="yes"
                        />
                      ) : (
                        <X
                          className="h-6 w-6 text-muted"
                          aria-label="no"
                        />
                      )
                    ) : (
                      <span
                        className={
                          c === row.cells.length - 1
                            ? "font-semibold text-foreground"
                            : "text-muted"
                        }
                      >
                        {cell}
                      </span>
                    )}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {data.footnote && <Footnote>{data.footnote}</Footnote>}
    </div>
  );
}

// --- timeline ---------------------------------------------------------------
export function Timeline({ data }: { data: SlideData }) {
  const items = data.timeline ?? [];
  return (
    <div className="flex h-full flex-col justify-center gap-12 px-24">
      <div className="flex flex-col gap-4">
        {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
        <Title className="text-5xl leading-tight">{data.title}</Title>
      </div>
      <div className="relative flex items-start justify-between">
        {/* Connecting rail behind the markers. */}
        <div
          className="absolute left-0 right-0 top-3 h-0.5 bg-border-strong"
          aria-hidden="true"
        />
        {items.map((item, i) => (
          <div
            key={i}
            className="relative flex w-1/5 flex-col items-center gap-4 px-2 text-center"
          >
            <div
              className={`h-6 w-6 rounded-full border-2 ${
                item.emphasis
                  ? "border-transparent bg-brand-gradient"
                  : "border-border-strong bg-background"
              }`}
            />
            <p
              className={`text-lg font-bold ${
                item.emphasis ? "text-gradient" : "text-foreground"
              }`}
            >
              {item.marker}
            </p>
            <p className="text-sm leading-snug text-muted-strong">
              {item.label}
            </p>
          </div>
        ))}
      </div>
      {data.footnote && <Footnote>{data.footnote}</Footnote>}
    </div>
  );
}

// --- quote ------------------------------------------------------------------
export function Quote({ data }: { data: SlideData }) {
  return (
    <div className="flex h-full flex-col justify-center gap-8 px-24">
      {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
      <blockquote className="text-5xl font-semibold leading-tight text-foreground">
        “{data.quote?.text}”
      </blockquote>
      {data.quote?.attribution && (
        <cite className="text-xl not-italic text-muted">
          — {data.quote.attribution}
        </cite>
      )}
    </div>
  );
}

// --- stacked ----------------------------------------------------------------
export function Stacked({ data }: { data: SlideData }) {
  return (
    <div className="flex h-full flex-col justify-center gap-8 px-24">
      <div className="flex flex-col gap-4">
        {data.eyebrow && <Eyebrow>{data.eyebrow}</Eyebrow>}
        <Title className="text-5xl leading-tight">{data.title}</Title>
      </div>
      {data.lead && <Lead text={data.lead} />}
      <div className="flex flex-col gap-5">
        {(data.sections ?? []).map((s, i) => (
          <div key={i} className="flex gap-5">
            <GradientRule className="mt-3 w-2 shrink-0 self-stretch" />
            <div className="flex flex-col gap-1">
              <h2 className="text-2xl font-semibold text-foreground">
                {s.heading}
              </h2>
              <p className="text-lg leading-relaxed text-muted-strong">
                {s.body}
              </p>
            </div>
          </div>
        ))}
      </div>
      {data.footnote && <Footnote>{data.footnote}</Footnote>}
    </div>
  );
}

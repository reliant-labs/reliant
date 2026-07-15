import type { LucideIcon } from "lucide-react";

/** The eight supported layout archetypes. */
export type LayoutType =
  | "centered-hero"
  | "two-column"
  | "card-grid"
  | "full-bleed-stat"
  | "comparison-table"
  | "timeline"
  | "quote"
  | "stacked";

/** A single stat/number callout. */
export interface Stat {
  value: string;
  label: string;
  /** Optional caveat/source shown in small print. */
  source?: string;
}

/** A card in a card-grid layout. */
export interface Card {
  icon?: LucideIcon;
  title: string;
  body: string;
}

/** A column in a two-column layout. */
export interface Column {
  eyebrow?: string;
  heading?: string;
  body?: string;
  /** Optional bullet list rendered under the body. */
  bullets?: string[];
}

/** A moment on a horizontal timeline. */
export interface TimelineItem {
  marker: string;
  label: string;
  /** Highlight the final/"now" item with the brand accent. */
  emphasis?: boolean;
}

/** A comparison table: header row + labelled body rows. */
export interface ComparisonTable {
  /** Column headers, excluding the leading row-label column. */
  columns: string[];
  rows: {
    label: string;
    /** One cell per column; `true`/`false` render as check/cross icons. */
    cells: (string | boolean)[];
  }[];
}

/** Everything a slide can carry. Layout selects which fields it reads. */
export interface SlideData {
  id: string;
  /** Sequence number shown in the deck (1-based). */
  number: number;
  /** Short nav/menu label. */
  navLabel: string;
  layout: LayoutType;
  eyebrow?: string;
  title: string;
  subtitle?: string;
  /** Free-form lead paragraph for hero/stacked layouts. */
  lead?: string;
  stat?: Stat;
  cards?: Card[];
  columns?: [Column, Column];
  timeline?: TimelineItem[];
  table?: ComparisonTable;
  quote?: { text: string; attribution?: string };
  /** Generic stacked sections (heading + body). */
  sections?: { heading: string; body: string }[];
  /** Small-print footnote (caveats, "needs verification", etc.). */
  footnote?: string;
}

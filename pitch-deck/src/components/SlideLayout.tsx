import type { SlideData } from "../types";
import {
  CenteredHero,
  TwoColumn,
  CardGrid,
  FullBleedStat,
  ComparisonTableLayout,
  Timeline,
  Quote,
  Stacked,
} from "./layouts";

/**
 * SlideLayout is the single dispatch point for the eight layout archetypes.
 * A slide component is a thin wrapper: pass a SlideData object, get the right
 * layout. New layouts are added here and in the LayoutType union.
 */
export function SlideLayout({ data }: { data: SlideData }) {
  switch (data.layout) {
    case "centered-hero":
      return <CenteredHero data={data} />;
    case "two-column":
      return <TwoColumn data={data} />;
    case "card-grid":
      return <CardGrid data={data} />;
    case "full-bleed-stat":
      return <FullBleedStat data={data} />;
    case "comparison-table":
      return <ComparisonTableLayout data={data} />;
    case "timeline":
      return <Timeline data={data} />;
    case "quote":
      return <Quote data={data} />;
    case "stacked":
      return <Stacked data={data} />;
    default: {
      // Exhaustiveness guard — a new LayoutType without a case is a type error.
      const _exhaustive: never = data.layout;
      return _exhaustive;
    }
  }
}

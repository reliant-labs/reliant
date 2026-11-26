// Copyright (c) 2026 Reliant Labs

/**
 * How a link to one route previews where people share it: Slack's unfurl,
 * X's card and LinkedIn's, drawn from what their scrapers read in the served
 * HTML (seoInsightsModel.ts socialCardOf). The layouts are approximations of
 * each, in the console's own tokens, not the services' brand colours.
 *
 * THE IMAGE is the site auditor's stored thumbnail of the page's og:image,
 * behind a short-lived signed URL (design O11). The console never loads the
 * og:image URL itself: a tenant's URL is not fetched from a signed-in
 * console, and the thumbnail also proves a scraper could reach the image.
 */

import { useState } from "react";
import { ImageOff } from "lucide-react";

import { cn } from "@/lib/utils";

import { Segmented } from "../Observability/Dashboards/parts";
import type { SocialCard } from "./seoInsightsModel";

export type SocialLayout = "slack" | "x" | "linkedin";
const LAYOUTS: { value: SocialLayout; label: string }[] = [
  { value: "slack", label: "Slack" },
  { value: "x", label: "X" },
  { value: "linkedin", label: "LinkedIn" },
];

function Thumbnail({ card, className }: { card: SocialCard; className?: string }) {
  if (card.imageUrl) return <img src={card.imageUrl} alt="" className={cn("block w-full bg-surface-sunken object-cover", className)} data-testid="social-card-image" />;
  return (
    <div className={cn("flex w-full flex-col items-center justify-center gap-1 bg-surface-sunken text-meta text-ink-muted", className)} data-testid="social-card-no-image">
      <ImageOff className="size-5" aria-hidden="true" />
      {card.declaresImage ? "The og:image could not be read" : "No og:image"}
    </div>
  );
}

const Missing = ({ what }: { what: string }) => <span className="italic text-ink-muted">{what}</span>;

function SlackCard({ card }: { card: SocialCard }) {
  return (
    <div className="flex gap-3 text-ui" data-testid="social-card-slack">
      <span className="w-1 shrink-0 rounded-full bg-ink/20" aria-hidden="true" />
      <div className="min-w-0 flex-1 space-y-1">
        <p className="text-meta font-medium text-ink">{card.domain || <Missing what="No domain" />}</p>
        <p className="font-semibold text-primary-ink">{card.title || <Missing what="No title" />}</p>
        <p className="line-clamp-3 text-ink">{card.description || <Missing what="No description" />}</p>
        <Thumbnail card={card} className="mt-2 aspect-[1.91/1] max-w-[22rem] rounded-md" />
      </div>
    </div>
  );
}

function XCard({ card }: { card: SocialCard }) {
  const large = card.twitterCard === "summary_large_image";
  if (!large) {
    return (
      <div className="flex overflow-hidden rounded-xl border border-border text-ui" data-testid="social-card-x">
        <Thumbnail card={card} className="aspect-square w-32 shrink-0" />
        <div className="min-w-0 space-y-0.5 border-l border-border px-3 py-2">
          <p className="text-meta text-ink-muted">{card.domain}</p>
          <p className="truncate text-ink">{card.title || <Missing what="No title" />}</p>
          <p className="line-clamp-2 text-meta text-ink-muted">{card.description}</p>
        </div>
      </div>
    );
  }
  return (
    <div className="max-w-[30rem] space-y-1" data-testid="social-card-x">
      <div className="relative overflow-hidden rounded-xl border border-border">
        <Thumbnail card={card} className="aspect-[1.91/1]" />
        <span className="absolute bottom-2 left-2 max-w-[85%] truncate rounded-md bg-surface/90 px-1.5 py-0.5 text-meta text-ink">{card.title || "No title"}</span>
      </div>
      <p className="text-meta text-ink-muted">From {card.domain || "an unknown domain"}</p>
    </div>
  );
}

function LinkedInCard({ card }: { card: SocialCard }) {
  return (
    <div className="max-w-[30rem] overflow-hidden rounded-xl border border-border text-ui" data-testid="social-card-linkedin">
      <Thumbnail card={card} className="aspect-[1.91/1]" />
      <div className="space-y-0.5 border-t border-border px-3 py-2">
        <p className="line-clamp-2 font-semibold text-ink">{card.title || <Missing what="No title" />}</p>
        <p className="text-meta text-ink-muted">{card.domain}</p>
      </div>
    </div>
  );
}

/** One route's social card, in the layout picked (Slack by default). */
export function SocialCardPreview({ card, initial = "slack" }: { card: SocialCard; initial?: SocialLayout }) {
  const [layout, setLayout] = useState<SocialLayout>(initial);
  return (
    <section className="space-y-3" aria-labelledby="social-card-title" data-testid="social-card-preview">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 id="social-card-title" className="text-section text-ink">
          Link preview
        </h3>
        <Segmented<SocialLayout> label="Where it is shared" testId="social-card-layout" value={layout} options={LAYOUTS} onChange={setLayout} />
      </div>
      <div className="rounded-xl border border-border/60 bg-surface-sunken p-4">
        {layout === "slack" ? <SlackCard card={card} /> : layout === "x" ? <XCard card={card} /> : <LinkedInCard card={card} />}
      </div>
      {card.missing.length > 0 && (
        <p className="text-meta text-ink-muted" data-testid="social-card-missing">
          Missing from the served HTML: {card.missing.join(", ")}. Scrapers do not run JavaScript, so these tags must be in the page as served.
        </p>
      )}
    </section>
  );
}

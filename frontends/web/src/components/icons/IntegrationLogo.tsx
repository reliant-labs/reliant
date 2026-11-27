// Copyright (c) 2025 Reliant Labs

/**
 * An integration's logo, keyed off its manifest's `icon:` field
 * (IntegrationManifest.icon: "github", "slack", "gmail", "google_calendar", "google_sheets", "twilio", "globe").
 * One component everywhere an integration appears — the step palette, catalog
 * results, the action node and its config header, connections — so a new
 * integration gets its mark in one place.
 *
 * The marks are embedded path data, not a dependency or a CDN fetch:
 *
 *  - GitHub, Gmail, Google Calendar, Google Sheets: Simple Icons v16.34.0
 *    (https://simpleicons.org), CC0 1.0.
 *  - Slack, Twilio: Simple Icons v11.15.0, CC0 1.0. Later Simple Icons releases
 *    no longer ship these two, so the path data is pinned to the last one that
 *    did.
 *
 * CC0 covers the path data. The marks themselves are their owners'
 * trademarks, used here only to name the integration they belong to (GitHub:
 * https://github.com/logos, Slack: https://slack.com/brand-guidelines,
 * Twilio: https://www.twilio.com/company/brand, Gmail, Google Calendar, Google Sheets: Google's product logos).
 *
 * Colour: each mark renders in its brand colour where that colour reads on
 * both the light and the dark card; GitHub's near-black (#181717) does not on
 * dark, so it uses `currentColor` like GitHub's own monochrome mark. Brand
 * colours are fixed, not theme tokens, which is why they are literal fills.
 * An unknown icon name falls back to a neutral plug, never an empty square.
 */

import { Globe, Mail, MessageSquare, Plug, Webhook, type LucideIcon } from "lucide-react";

import { cn } from "../../lib/utils";

export type IntegrationLogoSize = "xs" | "sm" | "md" | "lg";

export interface IntegrationLogoProps {
  /** The manifest's icon name, e.g. "github". Unknown or empty renders the fallback. */
  icon?: string;
  size?: IntegrationLogoSize;
  className?: string;
}

interface BrandMark {
  kind: "brand";
  paths: ReadonlyArray<{ d: string; fill: string }>;
}

interface GlyphMark {
  kind: "glyph";
  icon: LucideIcon;
}

type Mark = BrandMark | GlyphMark;

const SLACK_RED = "#E01E5A";
const SLACK_BLUE = "#36C5F0";
const SLACK_GREEN = "#2EB67D";
const SLACK_YELLOW = "#ECB22E";

const MARKS: Record<string, Mark> = {
  github: {
    kind: "brand",
    paths: [
      {
        fill: "currentColor",
        d: "M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12",
      },
    ],
  },
  gmail: {
    kind: "brand",
    paths: [
      {
        fill: "#EA4335",
        d: "M24 5.457v13.909c0 .904-.732 1.636-1.636 1.636h-3.819V11.73L12 16.64l-6.545-4.91v9.273H1.636A1.636 1.636 0 0 1 0 19.366V5.457c0-2.023 2.309-3.178 3.927-1.964L5.455 4.64 12 9.548l6.545-4.91 1.528-1.145C21.69 2.28 24 3.434 24 5.457z",
      },
    ],
  },
  google_calendar: {
    kind: "brand",
    paths: [
      {
        fill: "#4285F4",
        d: "M18.316 5.684H24v12.632h-5.684V5.684zM5.684 24h12.632v-5.684H5.684V24zM18.316 5.684V0H1.895A1.894 1.894 0 0 0 0 1.895v16.421h5.684V5.684h12.632zm-7.207 6.25v-.065c.272-.144.5-.349.687-.617s.279-.595.279-.982c0-.379-.099-.72-.3-1.025a2.05 2.05 0 0 0-.832-.714 2.703 2.703 0 0 0-1.197-.257c-.6 0-1.094.156-1.481.467-.386.311-.65.671-.793 1.078l1.085.452c.086-.249.224-.461.413-.633.189-.172.445-.257.767-.257.33 0 .602.088.816.264a.86.86 0 0 1 .322.703c0 .33-.12.589-.36.778-.24.19-.535.284-.886.284h-.567v1.085h.633c.407 0 .748.109 1.02.327.272.218.407.499.407.843 0 .336-.129.614-.387.832s-.565.327-.924.327c-.351 0-.651-.103-.897-.311-.248-.208-.422-.502-.521-.881l-1.096.452c.178.616.505 1.082.977 1.401.472.319.984.478 1.538.477a2.84 2.84 0 0 0 1.293-.291c.382-.193.684-.458.902-.794.218-.336.327-.72.327-1.149 0-.429-.115-.797-.344-1.105a2.067 2.067 0 0 0-.881-.689zm2.093-1.931l.602.913L15 10.045v5.744h1.187V8.446h-.827l-2.158 1.557zM22.105 0h-3.289v5.184H24V1.895A1.894 1.894 0 0 0 22.105 0zm-3.289 23.5l4.684-4.684h-4.684V23.5zM0 22.105C0 23.152.848 24 1.895 24h3.289v-5.184H0v3.289z",
      },
    ],
  },
  google_sheets: {
    kind: "brand",
    paths: [
      {
        fill: "#34A853",
        d: "M11.318 12.545H7.91v-1.909h3.41v1.91zM14.728 0v6h6l-6-6zm1.363 10.636h-3.41v1.91h3.41v-1.91zm0 3.273h-3.41v1.91h3.41v-1.91zM20.727 6.5v15.864c0 .904-.732 1.636-1.636 1.636H4.909a1.636 1.636 0 0 1-1.636-1.636V1.636C3.273.732 4.005 0 4.909 0h9.318v6.5h6.5zm-3.273 2.773H6.545v7.909h10.91v-7.91zm-6.136 4.636H7.91v1.91h3.41v-1.91z",
      },
    ],
  },
  // Slack's four-colour mark: Simple Icons ships it as one path, split here
  // into its eight pieces so each pair takes its brand colour.
  slack: {
    kind: "brand",
    paths: [
      { fill: SLACK_RED, d: "M5.042 15.165a2.528 2.528 0 0 1-2.52 2.523A2.528 2.528 0 0 1 0 15.165a2.527 2.527 0 0 1 2.522-2.52h2.52v2.52z" },
      { fill: SLACK_RED, d: "M6.313 15.165a2.527 2.527 0 0 1 2.521-2.52 2.527 2.527 0 0 1 2.521 2.52v6.313A2.528 2.528 0 0 1 8.834 24a2.528 2.528 0 0 1-2.521-2.522v-6.313z" },
      { fill: SLACK_BLUE, d: "M8.834 5.042a2.528 2.528 0 0 1-2.521-2.52A2.528 2.528 0 0 1 8.834 0a2.528 2.528 0 0 1 2.521 2.522v2.52H8.834z" },
      { fill: SLACK_BLUE, d: "M8.834 6.313a2.528 2.528 0 0 1 2.521 2.521 2.528 2.528 0 0 1-2.521 2.521H2.522A2.528 2.528 0 0 1 0 8.834a2.528 2.528 0 0 1 2.522-2.521h6.312z" },
      { fill: SLACK_GREEN, d: "M18.956 8.834a2.528 2.528 0 0 1 2.522-2.521A2.528 2.528 0 0 1 24 8.834a2.528 2.528 0 0 1-2.522 2.521h-2.522V8.834z" },
      { fill: SLACK_GREEN, d: "M17.688 8.834a2.528 2.528 0 0 1-2.523 2.521 2.527 2.527 0 0 1-2.52-2.521V2.522A2.527 2.527 0 0 1 15.165 0a2.528 2.528 0 0 1 2.523 2.522v6.312z" },
      { fill: SLACK_YELLOW, d: "M15.165 18.956a2.528 2.528 0 0 1 2.523 2.522A2.528 2.528 0 0 1 15.165 24a2.527 2.527 0 0 1-2.52-2.522v-2.522h2.52z" },
      { fill: SLACK_YELLOW, d: "M15.165 17.688a2.527 2.527 0 0 1-2.52-2.523 2.526 2.526 0 0 1 2.52-2.52h6.313A2.527 2.527 0 0 1 24 15.165a2.528 2.528 0 0 1-2.522 2.523h-6.313z" },
    ],
  },
  twilio: {
    kind: "brand",
    paths: [
      {
        fill: "#F22F46",
        d: "M12 0C5.381-.008.008 5.352 0 11.971V12c0 6.64 5.359 12 12 12 6.64 0 12-5.36 12-12 0-6.641-5.36-12-12-12zm0 20.801c-4.846.015-8.786-3.904-8.801-8.75V12c-.014-4.846 3.904-8.786 8.75-8.801H12c4.847-.014 8.786 3.904 8.801 8.75V12c.015 4.847-3.904 8.786-8.75 8.801H12zm5.44-11.76c0 1.359-1.12 2.479-2.481 2.479-1.366-.007-2.472-1.113-2.479-2.479 0-1.361 1.12-2.481 2.479-2.481 1.361 0 2.481 1.12 2.481 2.481zm0 5.919c0 1.36-1.12 2.48-2.481 2.48-1.367-.008-2.473-1.114-2.479-2.48 0-1.359 1.12-2.479 2.479-2.479 1.361-.001 2.481 1.12 2.481 2.479zm-5.919 0c0 1.36-1.12 2.48-2.479 2.48-1.368-.007-2.475-1.113-2.481-2.48 0-1.359 1.12-2.479 2.481-2.479 1.358-.001 2.479 1.12 2.479 2.479zm0-5.919c0 1.359-1.12 2.479-2.479 2.479-1.367-.007-2.475-1.112-2.481-2.479 0-1.361 1.12-2.481 2.481-2.481 1.358 0 2.479 1.12 2.479 2.481z",
      },
    ],
  },
  // Generic marks for integrations that are a protocol, not a brand.
  globe: { kind: "glyph", icon: Globe },
  webhook: { kind: "glyph", icon: Webhook },
  mail: { kind: "glyph", icon: Mail },
  sms: { kind: "glyph", icon: MessageSquare },
};

/** Other names a manifest or a ref might use for the same mark. */
const ALIASES: Record<string, string> = {
  http: "globe",
  "google-mail": "gmail",
  googlemail: "gmail",
  "google-calendar": "google_calendar",
  "google-sheets": "google_sheets",
};

const FALLBACK: GlyphMark = { kind: "glyph", icon: Plug };

/** The mark key an icon name resolves to, or undefined for the fallback. */
export function integrationLogoKey(icon: string | undefined): string | undefined {
  const name = icon?.trim().toLowerCase();
  if (!name) return undefined;
  const key = ALIASES[name] ?? name;
  return key in MARKS ? key : undefined;
}

/** Whether the name has a brand mark (not a generic glyph or the fallback). */
export function hasBrandLogo(icon: string | undefined): boolean {
  const key = integrationLogoKey(icon);
  return key !== undefined && MARKS[key]?.kind === "brand";
}

const MARK_SIZE: Record<IntegrationLogoSize, string> = {
  xs: "h-3 w-3",
  sm: "h-3.5 w-3.5",
  md: "h-4 w-4",
  lg: "h-5 w-5",
};

const TILE_SIZE: Record<IntegrationLogoSize, string> = {
  xs: "h-5 w-5 rounded-md",
  sm: "h-6 w-6 rounded-md",
  md: "h-8 w-8 rounded-lg",
  lg: "h-10 w-10 rounded-lg",
};

/** The bare mark, with no frame: for inline use beside text. */
export function IntegrationLogo({ icon, size = "md", className }: IntegrationLogoProps) {
  const key = integrationLogoKey(icon);
  const mark = (key && MARKS[key]) || FALLBACK;
  const sizeClass = MARK_SIZE[size];
  const markName = key ?? "fallback";
  if (mark.kind === "glyph") {
    const Icon = mark.icon;
    return (
      <Icon
        aria-hidden
        data-integration-logo={markName}
        className={cn(sizeClass, "flex-shrink-0", key ? "text-foreground" : "text-muted-foreground", className)}
      />
    );
  }
  return (
    <svg
      aria-hidden
      focusable="false"
      viewBox="0 0 24 24"
      data-integration-logo={markName}
      className={cn(sizeClass, "flex-shrink-0 text-foreground", className)}
    >
      {mark.paths.map((path, index) => (
        <path key={index} d={path.d} fill={path.fill} />
      ))}
    </svg>
  );
}

/**
 * The mark on a neutral inset tile: the shape every integration row and
 * header uses. The tile is `bg-background` with a soft border so it recesses
 * on a card in both light and dark (see web/src/components/forge-ui/card.tsx);
 * the mark is never tinted to match the product.
 */
export function IntegrationLogoTile({ icon, size = "md", className }: IntegrationLogoProps) {
  return (
    <span
      aria-hidden
      className={cn(
        "flex flex-shrink-0 items-center justify-center border border-border/60 bg-background",
        TILE_SIZE[size],
        className,
      )}
    >
      <IntegrationLogo icon={icon} size={size} />
    </span>
  );
}

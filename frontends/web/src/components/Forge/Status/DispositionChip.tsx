// Copyright (c) 2025 Reliant Labs

/**
 * The chip a check's disposition renders as, in the rows and in the legend.
 *
 * It is not a library Badge on purpose. Every axis of the five-level
 * vocabulary — fill, border style, opacity, ring, hue — comes from
 * DISPOSITION_STYLES (checkVocabulary.ts), and the two unmeasured levels are
 * defined by having NO fill and a DASHED border, which forge's Badge (always
 * filled, always a solid ring) cannot express. Layering the vocabulary over a
 * Badge only worked while the old components/ui Badge merged classes through
 * cn(); every class its variant contributed was overridden, so the variant
 * carried nothing. This chip is the shape that remained.
 *
 * `data-*`, `title` and the rest pass through to the root, which is what the
 * contract test and the legend's hover blurb hang on.
 */

import type React from "react";

import { cn } from "@/lib/utils";
import type { CheckDisposition } from "@/services/forge/status";

import { DISPOSITION_STYLES } from "./checkVocabulary";

export interface DispositionChipProps extends React.HTMLAttributes<HTMLSpanElement> {
  disposition: CheckDisposition;
}

export function DispositionChip({ disposition, className, children, ...rest }: DispositionChipProps) {
  const style = DISPOSITION_STYLES[disposition];
  return (
    <span
      className={cn(
        // The label is prose ("Could not measure"), not an identifier, so it is
        // not monospaced, and the chip is squared off to match the table.
        "inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium",
        style.container,
        style.foreground,
        className
      )}
      {...rest}
    >
      {children}
    </span>
  );
}

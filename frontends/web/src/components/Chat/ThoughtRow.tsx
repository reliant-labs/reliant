/**
 * The model's reasoning, as a quiet "Thought" row: collapsed by default, and
 * opened, the thinking reads in muted copy beneath it.
 *
 * Only readable thinking reaches here (a `thinking` segment). Redacted
 * thinking never does: messageProcessor gives it no segment, because its
 * content is an encrypted payload.
 */

import { memo, useState } from "react";
import { Lightbulb } from "lucide-react";
import { QuietRow } from "./QuietRow";

export const ThoughtRow = memo(function ThoughtRow({ text }: { text: string }) {
  const [expanded, setExpanded] = useState(false);
  return (
    <QuietRow
      data-testid="thought-row"
      icon={<Lightbulb className="size-4" />}
      expanded={expanded}
      onToggle={() => setExpanded((open) => !open)}
      ariaLabel={expanded ? "Hide thinking" : "Show thinking"}
      detail={
        <div className="whitespace-pre-wrap break-words text-ui leading-relaxed text-muted-foreground">
          {text}
        </div>
      }
    >
      <span>Thought</span>
    </QuietRow>
  );
});

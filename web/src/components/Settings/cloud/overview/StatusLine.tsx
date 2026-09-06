import { CheckCircle2 } from "lucide-react";

/**
 * One sentence, in plain language, above both bands.
 *
 * The cross-product question a user actually arrives with is "am I about to be
 * charged for anything?", and answering it needs BOTH products in view — which
 * is the reason the redesign kept one Overview instead of splitting AI and
 * compute into per-product tabs. This line is that answer before any bar is
 * read.
 *
 * It renders only when there is something true to say. A status line that
 * appears with "—" in it is worse than no status line: it occupies the most
 * prominent position on the page to announce that we do not know.
 */
export function StatusLine({ parts }: { parts: string[] }) {
  if (parts.length === 0) return null;

  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-muted/40 px-4 py-3">
      <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
      <p className="text-sm text-foreground">{parts.join(" · ")}</p>
    </div>
  );
}

// Copyright (c) 2026 Reliant Labs

/** A command in a well with a Copy button (a fix, a discovery run). Copying is a read: the demo allows it. */

import { useState } from "react";
import { Check, Copy } from "lucide-react";

import Button from "@/components/forge-ui/button";

export function CopyButton({ text, label = "Copy command", testId }: { text: string; label?: string; testId?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      variant="outline"
      size="sm"
      leftIcon={copied ? <Check /> : <Copy />}
      data-testid={testId}
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(
          () => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          },
          () => {},
        );
      }}
    >
      {copied ? "Copied" : label}
    </Button>
  );
}

export function CopyCommand({ command, label, testId }: { command: string; label: string; testId?: string }) {
  return (
    <figure className="min-w-0 space-y-1" data-testid={testId}>
      <figcaption className="flex items-center justify-between gap-2 text-meta font-medium text-ink-muted">
        {label}
        <CopyButton text={command} label="Copy" />
      </figcaption>
      <pre className="overflow-x-auto whitespace-pre rounded-md border border-border/60 bg-surface-sunken px-3 py-2 font-mono text-code text-ink">{command}</pre>
    </figure>
  );
}

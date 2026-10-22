/**
 * The builder header's one status chip: where the workflow is in its
 * lifecycle (Draft / Published) and what is true of the canvas right now
 * (unsaved changes, problems, ready to publish). It replaces a "Valid" badge
 * that described the last save — not the canvas — and a separate Draft badge.
 *
 * With problems, the chip opens a list of every one. Each names its step and
 * field in words and prints its remedy once; picking one selects the step,
 * opens its panel and focuses the field.
 */
import { useEffect, useRef, useState } from "react";
import { AlertTriangle, CheckCircle2, ChevronDown, Circle, Loader2 } from "lucide-react";

import { cn } from "../../lib/utils";
import { humanizeField, type LocatedFinding, type WorkflowStatusSummary } from "./workflowFindings";

interface WorkflowStatusChipProps {
  summary: WorkflowStatusSummary;
  findings: LocatedFinding[];
  /** A step's name for the list ("Call LLM · summarize"). */
  describeNode: (nodeId: string) => string;
  /** Whether a finding's step is on the canvas, so picking it can go there. */
  canSelect: (finding: LocatedFinding) => boolean;
  onSelect: (finding: LocatedFinding) => void;
  className?: string;
}

const TONE_CLASSES: Record<WorkflowStatusSummary["tone"], string> = {
  neutral: "border-border bg-muted/40 text-muted-foreground",
  info: "border-border bg-card text-foreground",
  success: "border-success/30 bg-success/10 text-success-ink",
  warning: "border-warning/40 bg-warning/10 text-warning-ink",
};

function ToneIcon({ tone, checking }: { tone: WorkflowStatusSummary["tone"]; checking: boolean }) {
  if (checking) return <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden />;
  if (tone === "warning") return <AlertTriangle className="h-3.5 w-3.5" aria-hidden />;
  if (tone === "success") return <CheckCircle2 className="h-3.5 w-3.5" aria-hidden />;
  return <Circle className="h-2.5 w-2.5 fill-current" aria-hidden />;
}

export function WorkflowStatusChip({
  summary,
  findings,
  describeNode,
  canSelect,
  onSelect,
  className,
}: WorkflowStatusChipProps) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);
  const isOpen = open && summary.hasProblemList;

  // Outside click and Escape close the list. Escape is consumed here, so the
  // builder (which never navigates on Escape anyway) does not also close a panel.
  useEffect(() => {
    if (!isOpen) return;
    const onPointerDown = (event: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      setOpen(false);
    };
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [isOpen]);

  const checking = summary.detail.toLowerCase().includes("checking");
  const errors = findings.filter((f) => !f.warning);
  const warnings = findings.filter((f) => f.warning);

  const renderFinding = (finding: LocatedFinding, index: number) => {
    const where = [
      finding.nodeId ? describeNode(finding.nodeId) : finding.triggerIndex !== undefined ? "Trigger" : "Workflow",
      humanizeField(finding.field),
    ]
      .filter(Boolean)
      .join(" · ");
    const content = (
      <>
        <span className="block text-xs font-medium text-muted-foreground">{where}</span>
        <span className="block text-sm text-foreground">{finding.text}</span>
        {finding.suggestion && <span className="mt-0.5 block text-xs text-muted-foreground">{finding.suggestion}</span>}
      </>
    );
    return (
      <li key={index}>
        {canSelect(finding) ? (
          <button
            type="button"
            onClick={() => {
              setOpen(false);
              onSelect(finding);
            }}
            className="w-full rounded-md px-2 py-1.5 text-left hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            {content}
          </button>
        ) : (
          <div className="px-2 py-1.5">{content}</div>
        )}
      </li>
    );
  };

  return (
    <div ref={rootRef} className={cn("relative", className)}>
      <button
        type="button"
        data-testid="workflow-status-chip"
        onClick={() => summary.hasProblemList && setOpen((value) => !value)}
        aria-expanded={summary.hasProblemList ? isOpen : undefined}
        aria-haspopup={summary.hasProblemList ? "dialog" : undefined}
        className={cn(
          "inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-sm font-medium transition-colors",
          TONE_CLASSES[summary.tone],
          summary.hasProblemList ? "cursor-pointer hover:opacity-90" : "cursor-default",
        )}
      >
        <ToneIcon tone={summary.tone} checking={checking} />
        <span>{summary.lifecycle}</span>
        <span aria-hidden className="opacity-60">·</span>
        <span>{summary.detail}</span>
        {summary.hasProblemList && <ChevronDown className={cn("h-3.5 w-3.5 transition-transform", isOpen && "rotate-180")} aria-hidden />}
      </button>

      {isOpen && (
        <div
          data-escape-layer
          role="dialog"
          aria-label="Problems"
          className="absolute left-0 top-full z-50 mt-2 max-h-96 w-96 overflow-y-auto rounded-lg border border-border bg-popover p-2 shadow-lg"
        >
          {errors.length > 0 && (
            <>
              <p className="px-2 pb-1 pt-0.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Problems ({errors.length})
              </p>
              <p className="px-2 pb-2 text-xs text-muted-foreground">
                In the workflow as shown{summary.detail.startsWith("Unsaved") ? ", including unsaved changes" : ""}. Fix these to publish it.
              </p>
              <ul className="space-y-0.5">{errors.map(renderFinding)}</ul>
            </>
          )}
          {warnings.length > 0 && (
            <>
              <p className="px-2 pb-1 pt-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Warnings ({warnings.length})
              </p>
              <ul className="space-y-0.5">{warnings.map((finding, index) => renderFinding(finding, errors.length + index))}</ul>
            </>
          )}
        </div>
      )}
    </div>
  );
}

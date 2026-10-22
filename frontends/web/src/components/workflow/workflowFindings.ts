/**
 * Validation findings as the builder shows them: on the step and field they
 * are about, in words, with the remedy printed once.
 *
 * The server places each finding (ValidationError.node_id / field / detail /
 * suggestion); nothing here parses `message`. That message keeps its old
 * "workflow.nodes.[1](call_llm).model: … (suggestion)" shape for older
 * readers, and is only the fallback text when `detail` is absent.
 */
import type { ValidationError } from "../../api/workflow-grpc";
import { isWarning } from "./workflowDraftStatus";

/** A finding with the location the canvas needs. */
export interface LocatedFinding {
  finding: ValidationError;
  warning: boolean;
  /** The top-level step it is about; undefined for workflow-level findings. */
  nodeId?: string;
  /** Its location within the step ("model", "with.channel", "inline.nodes.[0](x)"). */
  field: string;
  /** The config-panel field it belongs to, when there is one ("channel" for "with.channel"). */
  fieldKey?: string;
  /** The declared trigger it is about, by index. */
  triggerIndex?: number;
  /** The finding alone: no location prefix, no "(suggestion)" suffix. */
  text: string;
  suggestion?: string;
}

const TRIGGER_SEGMENT = /triggers\[(\d+)\]/;

/**
 * The config-panel field a finding's location names. A step's own fields are
 * keyed by name; an integration action's parameters arrive as "with.<name>".
 * Anything deeper ("inline.nodes…", "thread.inject.content") has no single
 * field in the panel, so it is shown on the step instead.
 */
export function findingFieldKey(field: string): string | undefined {
  if (!field) return undefined;
  const bare = field.replace(/^(with|args)\./, "");
  if (bare === "args" || bare === "with") return undefined;
  return bare.includes(".") ? undefined : bare;
}

/** The finding's text without its location prefix or repeated suggestion. */
export function findingText(finding: ValidationError): string {
  let text = (finding.detail || finding.message || "").trim();
  if (!finding.detail && finding.path) {
    const prefix = `${finding.path}: `;
    if (text.startsWith(prefix)) text = text.slice(prefix.length);
  }
  const suffix = finding.suggestion ? ` (${finding.suggestion})` : "";
  if (suffix && text.endsWith(suffix)) text = text.slice(0, -suffix.length);
  return text;
}

export function locateFinding(finding: ValidationError): LocatedFinding {
  const field = finding.field ?? "";
  const trigger = TRIGGER_SEGMENT.exec(finding.path || finding.message || "");
  return {
    finding,
    warning: isWarning(finding),
    nodeId: finding.nodeId || undefined,
    field,
    fieldKey: finding.nodeId ? findingFieldKey(field) : undefined,
    triggerIndex: !finding.nodeId && trigger ? Number(trigger[1]) : undefined,
    text: findingText(finding),
    suggestion: finding.suggestion || undefined,
  };
}

/** Located findings, errors first (the backend already orders them so). */
export function locateFindings(findings: readonly ValidationError[]): LocatedFinding[] {
  return findings.map(locateFinding);
}

/** Findings grouped by the step they are about. */
export function findingsByNode(findings: readonly LocatedFinding[]): Map<string, LocatedFinding[]> {
  const byNode = new Map<string, LocatedFinding[]>();
  for (const f of findings) {
    if (!f.nodeId) continue;
    const list = byNode.get(f.nodeId) ?? [];
    list.push(f);
    byNode.set(f.nodeId, list);
  }
  return byNode;
}

/** A field location in words: "system_prompt" → "System prompt", "with.channel" → "Channel". */
export function humanizeField(field: string): string {
  if (!field) return "";
  if (field.startsWith("inline.")) return "Inside its steps";
  const last = field.replace(/^(with|args)\./, "").split(".").filter(Boolean).pop() ?? "";
  const words = last.replace(/_+/g, " ").trim();
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : "";
}

export type ValidationStatus = "valid" | "invalid" | "validating" | "unknown";

export interface WorkflowStatusSummary {
  /** Where the workflow is in its lifecycle: "Draft", "Published", "Built-in", "Project file". */
  lifecycle: string;
  /** What the user should know now, after the lifecycle word. */
  detail: string;
  tone: "neutral" | "success" | "warning" | "info";
  /** The detail opens the problems list. */
  hasProblemList: boolean;
}

/**
 * The header's one status chip. It never reports a verdict about anything
 * but the canvas on screen: validation runs on the unsaved canvas, so the
 * problem count is live, and while edits are unsaved it says so instead of
 * implying the stored workflow is in that state.
 */
export function summarizeWorkflowStatus(opts: {
  source: "builtin" | "user" | "project";
  draftStatus: "draft" | "complete";
  hasUnsavedChanges: boolean;
  validationStatus: ValidationStatus;
  errorCount: number;
  warningCount: number;
}): WorkflowStatusSummary {
  const lifecycle =
    opts.source === "builtin"
      ? "Built-in"
      : opts.source === "project"
        ? "Project file"
        : opts.draftStatus === "complete"
          ? "Published"
          : "Draft";
  const problems = opts.errorCount > 0;
  const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? "" : "s"}`;
  let detail: string;
  let tone: WorkflowStatusSummary["tone"] = "neutral";
  if (opts.validationStatus === "validating") {
    detail = opts.hasUnsavedChanges ? "Unsaved changes · checking…" : "Checking…";
    tone = "info";
  } else if (problems) {
    detail = `${opts.hasUnsavedChanges ? "Unsaved changes · " : ""}${plural(opts.errorCount, "problem")}`;
    tone = "warning";
  } else if (opts.hasUnsavedChanges) {
    detail = "Unsaved changes";
    tone = "info";
  } else if (opts.validationStatus === "unknown") {
    detail = "Couldn't check for problems";
  } else if (opts.source === "user" && opts.draftStatus === "draft") {
    detail = "Ready to publish";
    tone = "success";
  } else {
    detail = "No problems";
    tone = "success";
  }
  if (!problems && opts.warningCount > 0 && opts.validationStatus !== "validating") {
    detail += ` · ${plural(opts.warningCount, "warning")}`;
  }
  return {
    lifecycle,
    detail,
    tone,
    hasProblemList: opts.validationStatus !== "validating" && (problems || opts.warningCount > 0),
  };
}

/**
 * Why Publish is disabled, in the order a user should address them. Empty ⇒
 * enabled. Unsaved edits do NOT block: Publish saves them and publishes in
 * one step, and the server refuses it if they do not validate.
 */
export function publishBlockers(opts: { errorCount: number; isBusy: boolean; validating: boolean }): string[] {
  const reasons: string[] = [];
  if (opts.isBusy) reasons.push("Wait for the save to finish.");
  else if (opts.validating) reasons.push("Checking for problems…");
  if (opts.errorCount > 0) {
    reasons.push(`Fix ${opts.errorCount} problem${opts.errorCount === 1 ? "" : "s"} first.`);
  }
  return reasons;
}

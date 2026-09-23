// Copyright (c) 2025 Reliant Labs

/**
 * Forge project-audit data layer — `forge project audit --json`.
 *
 * The audit is a static-analysis roll-up over the project's own files: ~18
 * categories, each with a status, a one-line summary, and a details bag. No
 * cluster is read, so unlike env status there is no reachability dimension here
 * — this module is about SEVERITY, and about not overstating it.
 *
 * TWO RULES, BOTH LEARNED THE HARD WAY.
 *
 * 1. ONLY `error` IS SEVERE. Forge's own roll-up is "error beats warn beats ok",
 *    and control-plane's real audit today is overall_status "warn" — config_deps
 *    and migration_safety both warn on a project that is perfectly healthy and
 *    shipping. A strip that goes red on that is red permanently, which trains
 *    people to stop reading it, which is strictly worse than not having it.
 *    `warn` is INFORMATIONAL here and gets its own treatment; it never gets the
 *    destructive one.
 *
 * 2. `file_sizes` IS ADVISORY AND NEVER GATES. Forge pins its status to "ok"
 *    even when it lists oversized files, and stamps `details.advisory: true` to
 *    say so. It is a navigation hint, not a defect. Rendering its findings as a
 *    failure would invent a problem forge explicitly declined to report — and
 *    because it never gates `overall_status`, doing so would also put the strip
 *    in disagreement with the number in its own header.
 *
 * THE CATEGORY SET IS NOT ENUMERATED HERE. Forge's contract is additive: new
 * categories appear over time, and some (`ingress`, `prerequisites`,
 * `external_builds`) only exist for certain project shapes. So categories are
 * iterated from the document, an unknown key renders as itself, and an
 * unrecognised STATUS is treated as needing attention rather than as healthy —
 * a status this build cannot read must not be able to claim a pass.
 */

// ── Category status and severity ────────────────────────────────────────────

/** The three statuses forge's audittype package puts on a category. */
export type ForgeAuditStatus = "ok" | "warn" | "error";

/**
 * How much visual weight a category earns.
 *
 * `notice` and `problem` are separate so that `warn` can be visible without
 * being alarming. `unreadable` exists for a status string this build does not
 * know: it is not `clean`, because an unread status must never be able to claim
 * health, and it is not `problem` either, because forge did not say there was
 * one.
 */
export type AuditSeverity = "clean" | "notice" | "problem" | "unreadable";

const SEVERITY_BY_STATUS: Record<ForgeAuditStatus, AuditSeverity> = {
  ok: "clean",
  warn: "notice",
  error: "problem",
};

/**
 * severityOf classifies a category status. An absent or unrecognised status is
 * `unreadable` — never `clean`, and never `problem`.
 */
export function severityOf(status: string | undefined): AuditSeverity {
  if (!status) return "unreadable";
  return SEVERITY_BY_STATUS[status as ForgeAuditStatus] ?? "unreadable";
}

/**
 * isSevere is the single gate on the strip's alarming treatment, and it is true
 * for `error` ONLY. See rule 1 in the module comment: widening this to include
 * `warn` makes the strip permanently red on a healthy project.
 */
export function isSevere(status: string | undefined): boolean {
  return status === "error";
}

export function auditStatusLabel(status: string | undefined): string {
  switch (status) {
    case "ok":
      return "OK";
    case "warn":
      return "Warn";
    case "error":
      return "Error";
    default:
      return "Unrecognised";
  }
}

// ── Forge's audit document ──────────────────────────────────────────────────
//
// Typed against forge's audit.Report and audittype.Category. Additive by
// contract, so every field is optional and the category map is open.

export interface ForgeAuditCategory {
  status?: string;
  summary?: string;
  /**
   * Structured fix-up data. Shape varies per category by design, so this stays
   * an open bag: the strip shows `summary`, and a caller that wants a specific
   * key reads it deliberately rather than through a type that pretends to know
   * every category's schema.
   */
  details?: Record<string, unknown>;
}

export interface ForgeAuditReport {
  project_name?: string;
  project_kind?: string;
  binary_version?: string;
  generated_at?: string;
  categories?: Record<string, ForgeAuditCategory>;
  /** Forge's roll-up: the WORST category status. */
  overall_status?: string;
}

// ── Projection (still pure) ─────────────────────────────────────────────────

/**
 * ADVISORY_DETAIL_KEY is the flag forge stamps on a category whose findings are
 * a hint rather than a defect. Reading the flag is the point: hardcoding
 * "file_sizes" would go stale the moment forge marks a second category
 * advisory, and the whole file exists to avoid pinning forge's category list.
 */
const ADVISORY_DETAIL_KEY = "advisory";

/**
 * isAdvisory reports whether a category declared itself non-gating.
 *
 * `file_sizes` is the one that ships today, and it is the specific trap: it
 * lists oversized files while holding its status at "ok", so a reader who
 * branches on "does details contain findings" instead of on `status` invents a
 * failure forge declined to report.
 */
export function isAdvisory(category: ForgeAuditCategory | undefined): boolean {
  return category?.details?.[ADVISORY_DETAIL_KEY] === true;
}

export interface AuditCategoryRow {
  /** The map key, verbatim. An unknown key renders as itself. */
  key: string;
  /** "migration_safety" → "Migration safety", for reading. Key kept for precision. */
  label: string;
  status: string | undefined;
  severity: AuditSeverity;
  summary: string;
  advisory: boolean;
  details?: Record<string, unknown>;
}

/**
 * PRINT_ORDER mirrors forge's own `auditCategoryOrder`, and is a PREFERENCE, not
 * a filter. Any category not listed — a newer forge's addition, a shape-specific
 * one — sorts alphabetically after the known set and renders normally. Nothing
 * is ever dropped for being unrecognised.
 */
const PRINT_ORDER = [
  "version",
  "shape",
  "features",
  "ingress",
  "environments",
  "external_builds",
  "prerequisites",
  "conventions",
  "codegen",
  "migration_safety",
  "optional_deps_guard",
  "config_deps",
  "scaffold_markers",
  "crud_stubs",
  "unscoped_auth",
  "file_sizes",
  "orphan_stubs",
  "deps",
];

function humanizeKey(key: string): string {
  const spaced = key.replace(/[_-]+/g, " ").trim();
  if (spaced === "") return key;
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/**
 * auditCategories projects the open category map into ordered rows.
 *
 * Iteration is over whatever keys the document has. A malformed entry (a string
 * where an object belongs, say) still yields a row with an undefined status,
 * which lands in `unreadable` — visible, and unable to read as healthy.
 */
export function auditCategories(
  report: ForgeAuditReport | null | undefined
): AuditCategoryRow[] {
  const categories = report?.categories;
  if (!categories || typeof categories !== "object") return [];

  const rows: AuditCategoryRow[] = Object.keys(categories).map((key) => {
    const raw = categories[key];
    const category: ForgeAuditCategory =
      raw && typeof raw === "object" && !Array.isArray(raw) ? raw : {};
    return {
      key,
      label: humanizeKey(key),
      status: category.status,
      severity: severityOf(category.status),
      summary: category.summary ?? "",
      advisory: isAdvisory(category),
      details: category.details,
    };
  });

  const rank = (key: string) => {
    const index = PRINT_ORDER.indexOf(key);
    return index === -1 ? PRINT_ORDER.length : index;
  };
  return rows.sort((a, b) => {
    const byOrder = rank(a.key) - rank(b.key);
    return byOrder !== 0 ? byOrder : a.key.localeCompare(b.key);
  });
}

/**
 * severityTally counts categories per severity, summed from the rows the strip
 * renders so the counts cannot disagree with what is shown.
 */
export function severityTally(
  report: ForgeAuditReport | null | undefined
): Record<AuditSeverity, number> {
  const totals: Record<AuditSeverity, number> = {
    clean: 0,
    notice: 0,
    problem: 0,
    unreadable: 0,
  };
  for (const row of auditCategories(report)) totals[row.severity] += 1;
  return totals;
}

/**
 * The strip's one-line verdict.
 *
 * `unreadable` outranks `notice` because a status this build could not read is a
 * gap in the report, and "some warnings" would imply the rest was understood.
 * Only `clean` claims health.
 */
export type AuditVerdict = "problem" | "unreadable" | "notice" | "clean" | "no-categories";

/**
 * verdictOf derives the verdict from the CATEGORY ROWS, not from
 * `overall_status`.
 *
 * Deriving it locally is what keeps the header honest when a newer forge reports
 * a status this build cannot classify: forge's own roll-up would call that
 * category "ok" (its switch has no arm for an unknown value), while the strip
 * shows it as unreadable. Summing the rendered rows means the header agrees with
 * the list. Forge's `overall_status` is still displayed verbatim alongside —
 * it is forge's opinion, and worth seeing.
 */
export function verdictOf(report: ForgeAuditReport | null | undefined): AuditVerdict {
  const rows = auditCategories(report);
  if (rows.length === 0) return "no-categories";
  const tally = severityTally(report);
  if (tally.problem > 0) return "problem";
  if (tally.unreadable > 0) return "unreadable";
  if (tally.notice > 0) return "notice";
  return "clean";
}

/** Short verdict word for the strip's badge. */
export function verdictLabel(verdict: AuditVerdict): string {
  switch (verdict) {
    case "problem":
      return "Needs attention";
    case "unreadable":
      return "Partly unreadable";
    case "notice":
      return "Warnings";
    case "clean":
      return "Clean";
    case "no-categories":
      return "No categories";
  }
}

/**
 * verdictSentence explains the verdict. The `notice` sentence carries rule 1: it
 * states outright that warnings are not failures, which is what stops a reader
 * treating a healthy warn-only project as broken.
 */
export function verdictSentence(verdict: AuditVerdict): string {
  switch (verdict) {
    case "problem":
      return "At least one category reported an error.";
    case "unreadable":
      return "A category reported a status this build of reliant does not recognise, so the audit is not fully readable. No category reported an error.";
    case "notice":
      return "Warnings only — no errors. Warnings are informational and are normal on a healthy project.";
    case "clean":
      return "Every category reported OK.";
    case "no-categories":
      return "Forge's audit returned no categories, so nothing has been assessed.";
  }
}

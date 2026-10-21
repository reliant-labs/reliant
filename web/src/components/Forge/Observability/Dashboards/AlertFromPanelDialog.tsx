// Copyright (c) 2025 Reliant Labs

/**
 * "Create alert from this panel": a dialog that turns a panel into the KCL
 * alert rule that watches the same number, to paste into the repo.
 *
 * Alerts stay declarative (decision DB): nothing is stored. The dialog prefills
 * the rule from the panel (its query, with threshold, comparator, look-back
 * window, hold time and severity as editable fields), shows the SQL it
 * produced and the KCL around it, and says where to paste it. A panel that is
 * not a number to compare with a threshold (a heatmap, a funnel, a list) gets
 * the reason and what to build instead, not a rule.
 *
 * The snippet itself is alertSnippet.ts; this is the form over it.
 */

import { useId, useMemo, useState, type ReactNode } from "react";
import { Bell, Check, Copy } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import type { Panel } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import type { ObsEnvironment } from "../obsPlace";
import {
  buildAlertSnippet,
  COMPARATORS,
  defaultChoices,
  needsSeriesChoice,
  ruleNameFor,
  SEVERITIES,
  thresholdUnit,
  type AlertChoices,
  type AlertScope,
  type DeclaredRules,
} from "./alertSnippet";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL } from "./parts";

export interface AlertFromPanelDialogProps {
  panel: Panel;
  dashboardTitle?: string;
  /** Every environment the project records: the rule is declared in one of them. */
  environments: readonly ObsEnvironment[];
  /** The environment the dashboard is looking at ("all" when it is looking at none in particular). */
  env: string;
  /** The dashboard's service filter. */
  services: readonly string[];
  onClose: () => void;
}

const INPUT =
  "h-8 w-full min-w-0 rounded-md border border-border bg-surface px-2.5 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-accent aria-[invalid=true]:border-danger-ink";

/** What an environment's recorded shape says about the rules it already declares. */
export function declaredRulesOf(env: ObsEnvironment | undefined): DeclaredRules {
  const alerts = env?.declared?.alerts;
  if (!alerts) return { recorded: false, own: [], all: [] };
  return { recorded: true, own: alerts.filter((a) => a.source === "user").map((a) => a.alert), all: alerts.map((a) => a.alert) };
}

function Field({ label, htmlFor, problem, hint, children, className }: { label: string; htmlFor: string; problem?: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <div className={cn("min-w-0 space-y-1", className)}>
      <label htmlFor={htmlFor} className="block text-xs font-medium text-ink">
        {label}
      </label>
      {children}
      {problem ? (
        <p role="alert" className="text-xs text-danger-ink" data-testid={`alert-problem-${htmlFor.split("-").pop()}`}>
          {problem}
        </p>
      ) : (
        hint && <p className="text-xs text-ink-muted">{hint}</p>
      )}
    </div>
  );
}

/**
 * Opened from the panel menu or from inside the panel drawer. In the drawer the
 * dialog must sit above it (the drawer is z-60, Modal z-50), and Escape must
 * close only the dialog: the drawer's own Escape handler would otherwise close
 * it too and throw away the draft being edited.
 */
export function AlertFromPanelDialog(props: AlertFromPanelDialogProps) {
  return (
    <div
      className="relative z-[70]"
      onKeyDown={(event) => {
        if (event.key === "Escape") event.nativeEvent.preventDefault();
      }}
    >
      <AlertFromPanelBody {...props} />
    </div>
  );
}

function AlertFromPanelBody({ panel, dashboardTitle, environments, env, services, onClose }: AlertFromPanelDialogProps) {
  const id = useId();
  const [choices, setChoices] = useState<AlertChoices>(() => defaultChoices(panel));
  const [nameEdited, setNameEdited] = useState(false);
  // "all" looks at no environment in particular; a rule still belongs to one.
  const [envName, setEnvName] = useState(() => environments.find((e) => e.name === env)?.name ?? environments[0]?.name ?? env);
  const [wrapped, setWrapped] = useState<boolean | null>(null);
  const [copied, setCopied] = useState(false);

  const declared = useMemo(() => declaredRulesOf(environments.find((e) => e.name === envName)), [environments, envName]);
  const scope: AlertScope = useMemo(() => ({ envName, services, dashboardTitle }), [envName, services, dashboardTitle]);
  const result = useMemo(() => buildAlertSnippet(panel, choices, scope, declared), [panel, choices, scope, declared]);

  const set = (patch: Partial<AlertChoices>) => {
    setCopied(false);
    setChoices((prev) => {
      const next = { ...prev, ...patch };
      if (patch.comparator && !nameEdited) next.name = ruleNameFor(panel.title || panel.id, patch.comparator);
      return next;
    });
  };

  const title = "Create alert from this panel";
  const heading = panel.title || panel.id;

  if (result.status === "unsupported") {
    return (
      <Modal
        open
        onClose={onClose}
        size="lg"
        title={title}
        description={heading}
        footer={
          <button type="button" className={BUTTON_SECONDARY} onClick={onClose} data-testid="alert-dialog-close">
            Close
          </button>
        }
      >
        <div className="space-y-3 text-sm" data-testid="alert-unsupported">
          <p className="text-ink" data-testid="alert-unsupported-reason">
            {result.reason}
          </p>
          {result.instead && (
            <p className="text-ink-muted" data-testid="alert-unsupported-instead">
              {result.instead}
            </p>
          )}
          <p className="text-xs text-ink-muted">Alerts stay in your repo as KCL, so this console does not store one for you; it writes the rule for panels that are a single number.</p>
        </div>
      </Modal>
    );
  }

  const unit = thresholdUnit(panel);
  const showRuleOnly = wrapped === null ? declared.own.length > 0 : !wrapped;
  const shown = showRuleOnly ? result.rule : result.block;
  const sqlLines = result.expr?.split("\n").length ?? 0;
  const copy = () => {
    if (!shown || !result.ready) return;
    void navigator.clipboard?.writeText(shown).then(
      () => setCopied(true),
      () => setCopied(false)
    );
  };

  return (
    <Modal
      open
      onClose={onClose}
      size="xl"
      title={title}
      description={`${dashboardTitle ? `${dashboardTitle} › ` : ""}${heading}: a KCL alert rule you paste into your repo. Nothing is stored here.`}
      footer={
        <>
          <button type="button" className={BUTTON_SECONDARY} onClick={onClose} data-testid="alert-dialog-close">
            Close
          </button>
          <button type="button" className={BUTTON_PRIMARY} onClick={copy} disabled={!result.ready} data-testid="alert-copy">
            {copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
            {copied ? "Copied" : showRuleOnly ? "Copy rule" : "Copy block"}
          </button>
        </>
      }
    >
      <div className="max-h-[68vh] space-y-5 overflow-y-auto pr-1" data-testid="alert-dialog">
        <section aria-label="The rule" className="grid grid-cols-2 gap-x-4 gap-y-3 md:grid-cols-6">
          <Field label="Rule name" htmlFor={`${id}-name`} problem={result.fieldProblems.name} className="col-span-2 md:col-span-3">
            <input
              id={`${id}-name`}
              value={choices.name}
              onChange={(e) => {
                setNameEdited(true);
                set({ name: e.target.value });
              }}
              aria-invalid={!!result.fieldProblems.name}
              className={cn(INPUT, "font-mono")}
              spellCheck={false}
              data-testid="alert-name"
            />
          </Field>
          <Field label="Severity" htmlFor={`${id}-severity`} className="md:col-span-1">
            <select id={`${id}-severity`} value={choices.severity} onChange={(e) => set({ severity: e.target.value as AlertChoices["severity"] })} className={INPUT} data-testid="alert-severity">
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Declare it in" htmlFor={`${id}-env`} className="col-span-2 md:col-span-2">
            <select id={`${id}-env`} value={envName} onChange={(e) => setEnvName(e.target.value)} className={INPUT} disabled={environments.length < 2} data-testid="alert-env">
              {(environments.length ? environments.map((e) => e.name) : [envName]).map((name) => (
                <option key={name} value={name}>
                  {name}
                </option>
              ))}
            </select>
          </Field>

          {needsSeriesChoice(panel) && (
            <Field label="Series to watch" htmlFor={`${id}-series`} className="col-span-2 md:col-span-6" hint="The panel plots several series; a rule watches one number.">
              <select id={`${id}-series`} value={choices.series} onChange={(e) => set({ series: Number(e.target.value) })} className={INPUT} data-testid="alert-series">
                {(panel.series ?? []).map((s, index) => (
                  <option key={index} value={index}>
                    {s.label}
                  </option>
                ))}
              </select>
            </Field>
          )}

          <Field label="Fires when the value is" htmlFor={`${id}-comparator`} className="md:col-span-2">
            <select id={`${id}-comparator`} value={choices.comparator} onChange={(e) => set({ comparator: e.target.value as AlertChoices["comparator"] })} className={INPUT} data-testid="alert-comparator">
              {COMPARATORS.map((c) => (
                <option key={c.value} value={c.value}>
                  {c.label}
                </option>
              ))}
            </select>
          </Field>
          <Field
            label={unit ? `Threshold (${unit})` : "Threshold"}
            htmlFor={`${id}-threshold`}
            problem={result.fieldProblems.threshold}
            hint={panel.unit === "percent" ? "Typed as a percentage: 5 is 5%." : undefined}
            className="md:col-span-2"
          >
            <input
              id={`${id}-threshold`}
              value={choices.threshold}
              onChange={(e) => set({ threshold: e.target.value })}
              inputMode="decimal"
              aria-invalid={!!result.fieldProblems.threshold}
              className={cn(INPUT, "tabular-nums")}
              data-testid="alert-threshold"
            />
          </Field>
          <Field label="Looking at the last" htmlFor={`${id}-window`} problem={result.fieldProblems.window} hint="The query's look-back, ending now." className="md:col-span-1">
            <input
              id={`${id}-window`}
              value={choices.window}
              onChange={(e) => set({ window: e.target.value })}
              aria-invalid={!!result.fieldProblems.window}
              className={cn(INPUT, "font-mono")}
              spellCheck={false}
              data-testid="alert-window"
            />
          </Field>
          <Field label="For at least" htmlFor={`${id}-hold`} problem={result.fieldProblems.hold} hint="How long it must hold." className="md:col-span-1">
            <input
              id={`${id}-hold`}
              value={choices.hold}
              onChange={(e) => set({ hold: e.target.value })}
              aria-invalid={!!result.fieldProblems.hold}
              className={cn(INPUT, "font-mono")}
              spellCheck={false}
              data-testid="alert-hold"
            />
          </Field>
        </section>

        {result.exprProblem && (
          <p role="alert" className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-danger-ink" data-testid="alert-expr-problem">
            The control plane would refuse this query as an alert rule: {result.exprProblem}.
          </p>
        )}
        {result.warnings.map((warning) => (
          <p key={warning} role="status" className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-warning-ink" data-testid="alert-warning">
            {warning}
          </p>
        ))}

        {shown ? (
          <section aria-label="The snippet" className="space-y-2" data-testid="alert-snippet">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-xs text-ink-muted">
                {showRuleOnly ? "Add to the rules list in" : "Paste into"} <code className="font-mono text-ink">{result.path}</code>
              </p>
              <div role="group" aria-label="Snippet form" className="inline-flex items-center rounded-md border border-border bg-surface p-0.5">
                {[
                  { value: true, label: "With context" },
                  { value: false, label: "Rule only" },
                ].map((option) => (
                  <button
                    key={option.label}
                    type="button"
                    aria-pressed={!showRuleOnly === option.value}
                    onClick={() => {
                      setWrapped(option.value);
                      setCopied(false);
                    }}
                    className={cn(
                      "h-6 rounded px-2 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
                      !showRuleOnly === option.value ? "bg-ink/8 font-medium text-ink" : "text-ink-muted hover:text-ink"
                    )}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            </div>
            <pre
              className="max-h-72 overflow-auto rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs leading-relaxed text-ink"
              tabIndex={0}
              aria-label="KCL alert rule"
              data-testid="alert-kcl"
            >
              {shown}
            </pre>
            <WherePaste declared={declared} envName={envName} path={result.path} />
          </section>
        ) : (
          <p className="text-xs text-ink-muted" data-testid="alert-snippet-pending">
            Fix the highlighted fields and the rule appears here.
          </p>
        )}

        {result.notes.length > 0 && result.expr && (
          <section aria-label="What the query does" className="space-y-1.5" data-testid="alert-notes">
            <p className="text-xs font-medium text-ink">What it watches</p>
            <ul className="list-disc space-y-1 pl-4 text-xs text-ink-muted">
              {result.notes.map((note) => (
                <li key={note}>{note}</li>
              ))}
              <li>
                <span data-testid="alert-sql-size">
                  {sqlLines} lines of SQL, {result.expr.length.toLocaleString()} of the 16,384 bytes a rule may hold.
                </span>{" "}
                The same read-only guard and limits as a SQL panel: one SELECT, a 20 second timeout, and at most 200 firing instances.
              </li>
            </ul>
          </section>
        )}
      </div>
    </Modal>
  );
}

function WherePaste({ declared, envName, path }: { declared: DeclaredRules; envName: string; path: string }) {
  let body: ReactNode;
  if (!declared.recorded) {
    body = (
      <>
        {envName} has no recorded shape yet, so <code className="font-mono">{path}</code> is forge's convention for where an environment's alerts live.
      </>
    );
  } else if (declared.own.length > 0) {
    body = (
      <>
        {envName} already declares {declared.own.length} {declared.own.length === 1 ? "rule" : "rules"} of its own ({declared.own.slice(0, 3).join(", ")}
        {declared.own.length > 3 ? ", …" : ""}). Add this one to that <code className="font-mono">rules = [ … ]</code> list.
      </>
    );
  } else {
    body = (
      <>
        {envName} declares no rules of its own yet. If <code className="font-mono">{path}</code> already sets <code className="font-mono">observability = forge.Observability {"{…}"}</code>, add just the rule under{" "}
        <code className="font-mono">alerts.rules</code>.
      </>
    );
  }
  return (
    <p className="text-xs text-ink-muted" data-testid="alert-where">
      {body} It takes effect with the next deploy of {envName}.
    </p>
  );
}

/** The panel menu's entry and the editor's button open the same dialog. */
export const CREATE_ALERT_LABEL = "Create alert from this panel";

export function CreateAlertButton({ onClick, disabled, title }: { onClick: () => void; disabled?: boolean; title?: string }) {
  return (
    <button type="button" className={BUTTON_SMALL} onClick={onClick} disabled={disabled} title={title} data-testid="panel-drawer-create-alert">
      <Bell className="h-3 w-3" aria-hidden="true" />
      Create alert
    </button>
  );
}

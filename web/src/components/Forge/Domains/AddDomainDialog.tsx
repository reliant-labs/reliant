// Copyright (c) 2025 Reliant Labs

/**
 * Add a domain and say what it serves, in ONE step.
 *
 * The control plane models these as two resources for good reasons — a
 * domain outlives any deployment, and a binding is one write that can move it
 * between environments — but that is a statement about LIFETIMES, not about
 * how the work arrives. Someone adding `hounders.club` already knows it is
 * for their web workload. Splitting it into "add, then come back and bind"
 * would leave the common case half-finished on screen, in the exact state
 * (claimed, unbound) that serves nothing.
 *
 * So the target is part of this form, and it is OPTIONAL: a domain being
 * parked, or one whose target has not been deployed yet, is a real case and
 * the server accepts an unbound domain.
 *
 * ── BIND BEFORE DNS IS DELIBERATE ───────────────────────────────────────────
 *
 * The domain will sit in PENDING_DNS for as long as propagation takes, and it
 * can be bound the whole time — `CreateDomainBinding` is documented as
 * bindable in any state, serving only when live. That is what lets a tenant
 * wire the whole thing up in one sitting and have it start serving by itself.
 *
 * ── REDIRECT IS A TARGET KIND, NOT A SEPARATE FLOW ──────────────────────────
 *
 * `www.hounders.club` → `hounders.club` is the single most common second
 * domain anyone adds. Making it a mode of the same picker rather than a
 * different dialog keeps it one decision ("what should this serve?") instead
 * of a fork the user has to find.
 */

import { useEffect, useId, useMemo, useState } from "react";
import { AlertTriangle } from "lucide-react";

import Modal from "@/components/forge-ui/modal";
import { cn } from "@/lib/utils";

/** One environment the domain can be bound into, with the targets inside it. */
export interface DomainTargetEnv {
  /** The environment's name, as the user knows it ("prod"). */
  name: string;
  /** The control plane's id — what a binding is actually written against. */
  environmentId: string;
  /** Workload and static-site names declared or deployed in this environment. */
  targets: string[];
}

export interface AddDomainDialogProps {
  open: boolean;
  onClose: () => void;
  /** Environments with a control-plane id. An env without one cannot be bound. */
  envs: DomainTargetEnv[];
  /** Hostnames the org already holds, so a duplicate is refused before the round trip. */
  takenHostnames: string[];
  onSubmit: (args: {
    hostname: string;
    environmentId?: string;
    target?: string;
    redirectTo?: string;
  }) => Promise<unknown>;
  isSubmitting: boolean;
  error: Error | null;
}

/**
 * A hostname, loosely. Deliberately permissive — the server lowercases,
 * trims a trailing dot and applies the real rule, and a client-side regex
 * that is stricter than the server's is a way to refuse a name that would
 * actually have worked. This catches the typo class only: no dot, spaces, a
 * scheme or a path pasted in from a browser bar.
 */
const HOSTNAME_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i;

type TargetMode = "workload" | "redirect" | "none";

export function AddDomainDialog({
  open,
  onClose,
  envs,
  takenHostnames,
  onSubmit,
  isSubmitting,
  error,
}: AddDomainDialogProps) {
  const hostnameId = useId();
  const envId = useId();
  const targetId = useId();
  const redirectId = useId();

  const [hostname, setHostname] = useState("");
  const [mode, setMode] = useState<TargetMode>("workload");
  const [environmentId, setEnvironmentId] = useState("");
  const [target, setTarget] = useState("");
  const [redirectTo, setRedirectTo] = useState("");
  const [touched, setTouched] = useState(false);

  // Reset on open so a second use never inherits the first's answers — a
  // stale hostname in this form would be claimed against the wrong name.
  useEffect(() => {
    if (!open) return;
    setHostname("");
    setMode(envs.length > 0 ? "workload" : "none");
    setEnvironmentId(envs[0]?.environmentId ?? "");
    setTarget("");
    setRedirectTo("");
    setTouched(false);
  }, [open, envs]);

  const selectedEnv = useMemo(
    () => envs.find((env) => env.environmentId === environmentId) ?? null,
    [envs, environmentId]
  );

  const normalized = hostname.trim().toLowerCase().replace(/\.$/, "");
  const duplicate = takenHostnames.includes(normalized);
  const malformed = normalized.length > 0 && !HOSTNAME_PATTERN.test(normalized);
  const selfRedirect = mode === "redirect" && redirectTo.trim().toLowerCase() === normalized;

  const hostnameProblem = duplicate
    ? "Your organization already holds this domain."
    : malformed
      ? "That does not look like a hostname. Use the name on its own, with no scheme or path — for example hounders.club."
      : selfRedirect
        ? "A domain cannot redirect to itself."
        : null;

  const bindReady =
    mode === "none" ||
    (mode === "workload" && !!environmentId && !!target) ||
    (mode === "redirect" && !!environmentId && !!redirectTo.trim());

  const canSubmit = !!normalized && !hostnameProblem && bindReady && !isSubmitting;

  const submit = () => {
    setTouched(true);
    if (!canSubmit) return;
    void onSubmit({
      hostname: normalized,
      environmentId: mode === "none" ? undefined : environmentId,
      target: mode === "workload" ? target : undefined,
      redirectTo: mode === "redirect" ? redirectTo.trim().toLowerCase() : undefined,
    });
  };

  const fieldClass =
    "w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-1 focus:ring-ring";

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="Add a custom domain"
      description="Claim a hostname for your organization and choose what it should serve. You will get the DNS records to publish next."
      size="lg"
      footer={
        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-lg border border-border px-3 py-1.5 text-sm text-foreground transition hover:bg-muted"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={submit}
            disabled={!canSubmit}
            className="rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {isSubmitting ? "Adding…" : "Add domain"}
          </button>
        </div>
      }
    >
      <form
        className="flex flex-col gap-5"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <div className="flex flex-col gap-1.5">
          <label htmlFor={hostnameId} className="text-sm font-medium text-foreground">
            Hostname
          </label>
          <input
            id={hostnameId}
            value={hostname}
            onChange={(event) => setHostname(event.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="hounders.club"
            autoComplete="off"
            spellCheck={false}
            className={cn(fieldClass, touched && hostnameProblem && "border-destructive")}
          />
          {touched && hostnameProblem ? (
            <p className="text-xs text-destructive">{hostnameProblem}</p>
          ) : (
            <p className="text-xs text-muted-foreground">
              An apex (hounders.club) and its www are two separate domains. Add both, and bind the
              www one as a redirect.
            </p>
          )}
        </div>

        <fieldset className="flex flex-col gap-3">
          <legend className="text-sm font-medium text-foreground">What should it serve?</legend>
          <div className="flex flex-wrap gap-4 text-sm">
            <ModeRadio
              checked={mode === "workload"}
              onChange={() => setMode("workload")}
              disabled={envs.length === 0}
              label="A workload or site"
            />
            <ModeRadio
              checked={mode === "redirect"}
              onChange={() => setMode("redirect")}
              disabled={envs.length === 0}
              label="Redirect to another domain"
            />
            <ModeRadio
              checked={mode === "none"}
              onChange={() => setMode("none")}
              label="Nothing yet"
            />
          </div>

          {envs.length === 0 && (
            <p className="text-xs text-muted-foreground">
              None of this project&apos;s environments are run by Reliant cloud, so there is nothing
              to bind to yet. You can still claim the domain and bind it after your first deploy.
            </p>
          )}

          {mode !== "none" && envs.length > 0 && (
            <div className="flex flex-col gap-3 rounded-lg border border-border/60 bg-background p-3">
              <div className="flex flex-col gap-1.5">
                <label htmlFor={envId} className="text-xs font-medium text-foreground">
                  Environment
                </label>
                <select
                  id={envId}
                  value={environmentId}
                  onChange={(event) => {
                    setEnvironmentId(event.target.value);
                    setTarget("");
                  }}
                  className={fieldClass}
                >
                  {envs.map((env) => (
                    <option key={env.environmentId} value={env.environmentId}>
                      {env.name}
                    </option>
                  ))}
                </select>
              </div>

              {mode === "workload" ? (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={targetId} className="text-xs font-medium text-foreground">
                    Target
                  </label>
                  {selectedEnv && selectedEnv.targets.length > 0 ? (
                    <select
                      id={targetId}
                      value={target}
                      onChange={(event) => setTarget(event.target.value)}
                      className={fieldClass}
                    >
                      <option value="">Choose a workload or site…</option>
                      {selectedEnv.targets.map((name) => (
                        <option key={name} value={name}>
                          {name}
                        </option>
                      ))}
                    </select>
                  ) : (
                    /* A free-text fallback rather than a dead end: a binding
                       names a target that need not exist yet, so an env with
                       nothing deployed must still be bindable. */
                    <input
                      id={targetId}
                      value={target}
                      onChange={(event) => setTarget(event.target.value)}
                      placeholder="web"
                      autoComplete="off"
                      spellCheck={false}
                      className={fieldClass}
                    />
                  )}
                  <p className="text-2xs text-muted-foreground">
                    The workload or static site&apos;s name, as your project declares it. It does not
                    have to be deployed yet — the domain waits for it.
                  </p>
                </div>
              ) : (
                <div className="flex flex-col gap-1.5">
                  <label htmlFor={redirectId} className="text-xs font-medium text-foreground">
                    Redirect to
                  </label>
                  <input
                    id={redirectId}
                    value={redirectTo}
                    onChange={(event) => setRedirectTo(event.target.value)}
                    placeholder="hounders.club"
                    autoComplete="off"
                    spellCheck={false}
                    className={fieldClass}
                  />
                  <p className="text-2xs text-muted-foreground">
                    Answers a 308 to this hostname, keeping the path and query. It never dials a
                    backend, so it keeps redirecting even while the app behind the other domain is
                    down.
                  </p>
                </div>
              )}
            </div>
          )}
        </fieldset>

        {error && (
          <div
            className="flex items-start gap-2 rounded-lg border border-destructive/40 bg-destructive/10 p-3 text-xs text-destructive"
            role="alert"
          >
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>{error.message}</span>
          </div>
        )}
      </form>
    </Modal>
  );
}

function ModeRadio({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: () => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <label
      className={cn(
        "inline-flex items-center gap-2",
        disabled ? "cursor-not-allowed text-muted-foreground" : "cursor-pointer text-foreground"
      )}
    >
      <input
        type="radio"
        name="domain-target-mode"
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        className="accent-primary"
      />
      {label}
    </label>
  );
}

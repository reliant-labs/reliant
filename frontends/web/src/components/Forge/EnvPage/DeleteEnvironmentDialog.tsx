// Copyright (c) 2025 Reliant Labs

/**
 * DELETE A HOSTED ENVIRONMENT: what goes, what stays, and what happens to its
 * data (control-plane docs/design/unified-shell-and-projects.md, Lifecycle,
 * PR L1).
 *
 * The confirmation names what is deleted (workloads, namespace, generated
 * hostnames and their URLs, secret delivery) and what is kept (releases, the
 * org's custom domains), and asks one question about data: delete the
 * databases, or KEEP them (DeleteEnvironment's keep_data). Kept databases keep
 * billing as storage and are listed on the Environments page, with Purge,
 * until someone purges them.
 *
 * ── THE DATA CHOICE IS THE PERSON'S, AND IS NEVER DOWNGRADED ────────────────
 *
 * What is sent is exactly what the person chose: keep_data is true if and
 * only if they chose Keep. Nothing the dialog reads afterwards can turn a Keep
 * into a Delete. Each choice records what it was made against (the database
 * inventory, and for Keep, that keeping was available); when either changes —
 * a refetch fails, the inventory moves — the choice is VOID: the confirm is
 * disabled and the person must choose again. Destroying data always needs a
 * Delete chosen against what is on screen; there is no default. Only an
 * environment the status read shows running no database needs no choice.
 *
 * Keep is offered only once ListRetainedDatabases has answered: a kept
 * database this person cannot list could be neither confirmed nor purged
 * here. That read is also enough to know keep_data is safe to send: every
 * control plane serving it either honours keep_data (control-plane#1541 on)
 * or keeps the data unless delete_data is sent, which this never sends
 * (#652 to #1541). See the PR for the proof.
 *
 * ── A BOUND CUSTOM DOMAIN REFUSES THE DELETE ────────────────────────────────
 *
 * The control plane refuses, force or not, rather than take the org's own
 * hostname offline. The dialog lists the bound domains and sends the person to
 * the Domains page to unbind them. It does NOT unbind them itself: the backend
 * unbinds by domain id, so a domain rebound to another environment between
 * this list and the unbind would go offline there.
 *
 * Deleting needs an org owner or admin (orgAuthority.ts). The menu item is
 * disabled for a known member; a PermissionDenied is still shown in words.
 * A keep-data delete that succeeds leaves a check (Overview/keptDataChecks.ts)
 * that confirms the databases are listed as kept, and warns if they are not.
 */

import { useId, useState } from "react";

import Button from "@/components/forge-ui/button";
import { FormField } from "@/components/forge-ui/form";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import { useForgeDomains } from "@/hooks/forge-domain-queries";
import { useDeleteEnvironment } from "@/hooks/forge-queries";
import { useRetainedDatabases } from "@/hooks/forge-teardown-queries";
import { cloudErrorDetail, isDomainBoundRefusal, retainedReadFailureOf, type RetainedReadFailure } from "@/services/forge/cloudEnvs";
import { cn } from "@/lib/utils";

import { RETAINED_READ_FAILURE_COPY, startKeptDataCheck } from "../Overview/keptDataChecks";
import { DELETE_ENV_NEEDS_ADMIN, refusalText } from "../orgAuthority";

type DataKind = "delete" | "keep";
type KeepSupport = "checking" | "supported" | RetainedReadFailure;

/** A data choice and what it was made against. Void once that changes. */
interface DataChoice {
  kind: DataKind;
  basis: string;
}

/** What a choice depends on: the inventory for either, and Keep's availability for Keep. */
function basisOf(kind: DataKind, databases: string[] | null, keepSupport: KeepSupport): string {
  const inventory = databases === null ? null : [...databases].sort();
  return JSON.stringify(kind === "keep" ? { inventory, keepSupport } : { inventory });
}

export function DeleteEnvironmentDialog({
  isOpen,
  environmentId,
  envName,
  workloads = [],
  urls = [],
  databases = null,
  project = "",
  onClose,
  onDeleted,
  onManageDomains,
}: {
  isOpen: boolean;
  environmentId: string;
  envName: string;
  /** The workload names the delete stops, when the status read has them. */
  workloads?: string[];
  /** The platform URLs that stop answering, when the status read has them. */
  urls?: string[];
  /** Its database workloads; null when the status read has not said. */
  databases?: string[] | null;
  /** The forge project (app), so the keep-data check shows on its page. */
  project?: string;
  onClose: () => void;
  onDeleted?: () => void;
  /** Opens the Domains page, where a bound domain is unbound. */
  onManageDomains?: () => void;
}) {
  const del = useDeleteEnvironment(environmentId);
  const domains = useForgeDomains({ enabled: isOpen });
  // Whether this control plane can keep (and list) databases at all.
  const keepProbe = useRetainedDatabases({ enabled: isOpen });
  const keepSupport: KeepSupport = keepProbe.isSuccess
    ? "supported"
    : keepProbe.isError
      ? retainedReadFailureOf(keepProbe.error)
      : "checking";
  const [typed, setTyped] = useState("");
  const [choice, setChoice] = useState<DataChoice | null>(null);
  const matches = typed === envName;

  const needsChoice = databases === null || databases.length > 0;
  const choiceValid =
    choice !== null &&
    choice.basis === basisOf(choice.kind, databases, keepSupport) &&
    (choice.kind === "delete" || keepSupport === "supported");
  const stale = choice !== null && !choiceValid;
  // Sent exactly as chosen. A void choice sends nothing: the confirm is off.
  const keepData = choiceValid && choice.kind === "keep";
  const ready = stale ? false : needsChoice ? choiceValid : true;

  const choose = (kind: DataKind) => setChoice({ kind, basis: basisOf(kind, databases, keepSupport) });

  const bound = (domains.data?.availability === "available" ? domains.data.domains : [])
    .filter((domain) => domain.binding?.environmentId === environmentId)
    .map((domain) => domain.hostname)
    .sort((a, b) => a.localeCompare(b));
  const refusedForDomains = del.isError && isDomainBoundRefusal(del.error);
  const pending = del.isPending;

  const reset = () => {
    setTyped("");
    setChoice(null);
    del.reset();
  };
  const close = () => {
    if (pending) return;
    reset();
    onClose();
  };
  const confirm = () => {
    if (!matches || !ready || pending) return;
    const kept = keepData;
    del.mutate(
      { keepData: kept },
      {
        onSuccess: () => {
          if (kept && (databases === null || databases.length > 0)) {
            startKeptDataCheck({ environmentId, envName, project, databases: databases ?? [] });
          }
          reset();
          onClose();
          onDeleted?.();
        },
      }
    );
  };

  return (
    <Modal open={isOpen} onClose={close} size="md" title={`Delete ${envName}?`}>
      <div className="space-y-4 text-sm text-ink" data-testid="delete-environment-dialog">
        <div className="space-y-1.5" data-testid="delete-environment-removed">
          <p className="font-medium">Deleted</p>
          <ul className="list-disc space-y-1 pl-5 text-ink-muted">
            <li>
              Every workload and deployment in <span className="font-mono text-ink">{envName}</span>
              {workloads.length > 0 && (
                <>
                  : <span className="font-mono text-ink">{workloads.join(", ")}</span>
                </>
              )}
            </li>
            <li>Its namespace</li>
            <li>
              Its generated hostnames: the platform URLs stop answering
              {urls.length > 0 && (
                <>
                  : <span className="font-mono text-ink">{urls.join(", ")}</span>
                </>
              )}
            </li>
            <li>
              Secret delivery to it. A new {envName} does not get its secrets back: they have to be set again.
            </li>
          </ul>
          <p className="text-xs text-ink-muted">
            Its charges stop once the teardown finishes, which can take a few minutes after you confirm.
          </p>
        </div>
        <div className="space-y-1.5" data-testid="delete-environment-kept">
          <p className="font-medium">Kept</p>
          <ul className="list-disc space-y-1 pl-5 text-ink-muted">
            <li>
              Releases and their images. Run <code className="font-mono text-ink">reliant forge env deploy {envName}</code> or use
              Deploy to create it again, empty.
            </li>
            <li>Custom domains: they stay in your organization, unbound</li>
          </ul>
        </div>

        {!needsChoice && choice === null ? (
          <p className="text-ink-muted" data-testid="delete-environment-no-databases">
            It runs no databases, so there is no data to keep.
          </p>
        ) : (
          <DataChoiceField
            value={choiceValid ? choice.kind : null}
            onChange={choose}
            disabled={pending}
            keepSupport={keepSupport}
            keepError={cloudErrorDetail(keepProbe.error)}
            onRetryKeep={() => void keepProbe.refetch()}
            retrying={keepProbe.isFetching}
          />
        )}

        {stale && (
          <p role="alert" className="text-xs text-ink" data-testid="delete-environment-choice-stale" data-kind={choice.kind}>
            {choice.kind === "keep"
              ? keepSupport !== "supported" && keepSupport !== "checking"
                ? `You chose Keep data, but it can't be honoured now: ${RETAINED_READ_FAILURE_COPY[keepSupport]}`
                : keepSupport === "checking"
                  ? "You chose Keep data, and Reliant is checking again whether it can keep them."
                  : "You chose Keep data, but the databases this environment runs changed since you chose."
              : "You chose Delete the data, but the databases this environment runs changed since you chose."}{" "}
            Nothing has been deleted. Choose again.
          </p>
        )}

        {bound.length > 0 && (
          <div className="space-y-1.5" data-testid="delete-environment-bound-domains">
            <p className="font-medium">Custom domains bound to {envName}</p>
            <ul className="list-disc pl-5">
              {bound.map((hostname) => (
                <li key={hostname} className="font-mono">
                  {hostname}
                </li>
              ))}
            </ul>
            <DomainsFirst envName={envName} onManageDomains={onManageDomains} />
          </div>
        )}

        <FormField className="forge-ui">
          <Label>Type {envName} to confirm</Label>
          <Input
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            autoComplete="off"
            spellCheck={false}
            disabled={pending}
            data-testid="delete-environment-confirm-input"
          />
        </FormField>

        {refusedForDomains && (
          <div role="alert" className="space-y-1 text-xs" data-testid="delete-environment-domain-refusal">
            <p className="text-danger-ink">{refusalText(del.error, DELETE_ENV_NEEDS_ADMIN)}</p>
            {bound.length === 0 && <DomainsFirst envName={envName} onManageDomains={onManageDomains} />}
          </div>
        )}
        {del.isError && !refusedForDomains && (
          <p role="alert" className="text-xs text-danger-ink" data-testid="delete-environment-error">
            {refusalText(del.error, DELETE_ENV_NEEDS_ADMIN)}
          </p>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={close} disabled={pending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            size="sm"
            loading={pending}
            disabled={!matches || !ready || pending}
            onClick={confirm}
            data-testid="delete-environment-confirm"
          >
            Delete environment
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** A bound domain blocks the delete; it is unbound on the Domains page, never from here. */
function DomainsFirst({ envName, onManageDomains }: { envName: string; onManageDomains?: () => void }) {
  return (
    <div className="space-y-1.5" data-testid="delete-environment-unbind-first">
      <p className="text-ink-muted">
        An environment a custom domain points at can&apos;t be deleted. Unbind these domains on the Domains page, check
        each still points at {envName} before you do, then delete again. The domains stay in your organization.
      </p>
      {onManageDomains && (
        <Button variant="outline" size="sm" onClick={onManageDomains} data-testid="delete-environment-open-domains">
          Open Domains
        </Button>
      )}
    </div>
  );
}

function DataChoiceField({
  value,
  onChange,
  disabled,
  keepSupport,
  keepError,
  onRetryKeep,
  retrying,
}: {
  /** The valid choice, or null: nothing is preselected. */
  value: DataKind | null;
  onChange: (value: DataKind) => void;
  disabled: boolean;
  keepSupport: KeepSupport;
  keepError: string;
  onRetryKeep: () => void;
  retrying: boolean;
}) {
  const name = useId();
  const keepFailure = keepSupport !== "supported" && keepSupport !== "checking" ? keepSupport : null;
  const option = (kind: DataKind, label: string, detail: React.ReactNode, off = false) => (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2",
        value === kind ? "border-ink/40 bg-ink/5" : "border-border hover:bg-ink/5",
        (disabled || off) && "cursor-not-allowed opacity-60"
      )}
    >
      <input
        type="radio"
        name={name}
        checked={value === kind}
        onChange={() => onChange(kind)}
        disabled={disabled || off}
        className="mt-0.5 accent-current"
        data-testid={`delete-environment-data-${kind}`}
      />
      <span>
        <span className="block font-medium text-ink">{label}</span>
        <span className="block text-xs text-ink-muted">{detail}</span>
      </span>
    </label>
  );
  return (
    <fieldset className="space-y-2" data-testid="delete-environment-data-note">
      <legend className="mb-1.5 font-medium">Databases: choose one</legend>
      {option("delete", "Delete the data", "Its databases are deleted with it, data included. This cannot be undone.")}
      {option(
        "keep",
        "Keep data",
        keepSupport === "checking"
          ? "Checking whether this control plane can keep them…"
          : keepFailure
            ? "Not offered here."
            : "Its databases are kept and keep billing as storage. They are listed under Kept databases on the Environments page until you purge them. A new deploy does not reattach them.",
        keepSupport !== "supported"
      )}
      {keepFailure && (
        <div className="space-y-1 text-xs text-ink-muted" data-testid="delete-environment-keep-unavailable" data-reason={keepFailure}>
          <p>
            Keep data isn&apos;t offered: {RETAINED_READ_FAILURE_COPY[keepFailure]} A kept database could not be confirmed or
            purged here.
            {keepFailure === "unavailable" && keepError && <> ({keepError})</>}
          </p>
          {keepFailure === "unavailable" && (
            <Button
              variant="outline"
              size="sm"
              onClick={onRetryKeep}
              disabled={disabled || retrying}
              loading={retrying}
              data-testid="delete-environment-keep-retry"
            >
              Check again
            </Button>
          )}
        </div>
      )}
    </fieldset>
  );
}

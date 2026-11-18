// Copyright (c) 2025 Reliant Labs

/**
 * DELETE A HOSTED ENVIRONMENT: what goes, what stays, and what happens to its
 * data (control-plane docs/design/unified-shell-and-projects.md, Lifecycle,
 * PR L1).
 *
 * The confirmation names what is deleted (workloads, namespace, generated
 * hostnames, secret delivery) and what is kept (releases, secrets in the
 * store, the org's custom domains), and asks one question about data:
 * delete the databases, or KEEP them (DeleteEnvironment's keep_data). Kept
 * databases keep billing as storage and are listed on the Environments page,
 * with Purge, until someone purges them.
 *
 * A BOUND CUSTOM DOMAIN REFUSES THE DELETE (the control plane refuses, force
 * or not, rather than take the org's own hostname offline). When the dialog
 * can see bound domains it lists them and its action is "Unbind domains and
 * delete"; when it could not see them and the delete is refused for that
 * reason, it offers the same action then. Either way each step's progress and
 * error is shown: which domains were unbound, and whether the delete ran.
 *
 * Deleting needs an org owner or admin (orgAuthority.ts). The menu item is
 * disabled for a known member; a PermissionDenied is still shown in words.
 *
 * KEEP DATA IS OFFERED ONLY WHERE IT CAN BE CHECKED. The dialog asks
 * ListRetainedDatabases first and offers Keep data only once that read has
 * SUCCEEDED: a kept database this person cannot list could be neither
 * confirmed nor purged here. While it loads the option is disabled; any
 * failure (no rpc, no access, unavailable) hides it with the reason, and an
 * unavailable read can be asked again. A
 * keep-data delete that succeeds leaves a check (Overview/keptDataChecks.ts)
 * that confirms the databases are listed as kept, and warns if they are not.
 */

import { useId, useState } from "react";
import { Check, Circle, Loader2, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import { FormField } from "@/components/forge-ui/form";
import Input from "@/components/forge-ui/input";
import Label from "@/components/forge-ui/label";
import Modal from "@/components/forge-ui/modal";
import { useForgeDomains } from "@/hooks/forge-domain-queries";
import { useDeleteEnvironment } from "@/hooks/forge-queries";
import { useRetainedDatabases, useUnbindDomainsAndDelete, type TeardownStep } from "@/hooks/forge-teardown-queries";
import { cloudErrorDetail, isDomainBoundRefusal, retainedReadFailureOf, type RetainedReadFailure } from "@/services/forge/cloudEnvs";
import { cn } from "@/lib/utils";

import { RETAINED_READ_FAILURE_COPY, startKeptDataCheck } from "../Overview/keptDataChecks";
import { DELETE_ENV_NEEDS_ADMIN, refusalText } from "../orgAuthority";

type DataChoice = "delete" | "keep";
type KeepSupport = "checking" | "supported" | RetainedReadFailure;

export function DeleteEnvironmentDialog({
  isOpen,
  environmentId,
  envName,
  workloads = [],
  databases = null,
  project = "",
  onClose,
  onDeleted,
}: {
  isOpen: boolean;
  environmentId: string;
  envName: string;
  /** The workload names the delete stops, when the status read has them. */
  workloads?: string[];
  /** Its database workloads; null when the status read has not said. */
  databases?: string[] | null;
  /** The forge project (app), so the keep-data check shows on its page. */
  project?: string;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const del = useDeleteEnvironment(environmentId);
  const unbindAndDelete = useUnbindDomainsAndDelete(environmentId, envName);
  const domains = useForgeDomains({ enabled: isOpen });
  // Whether this control plane can keep (and list) databases at all.
  const keepProbe = useRetainedDatabases({ enabled: isOpen });
  const keepSupport: KeepSupport = keepProbe.isSuccess
    ? "supported"
    : keepProbe.isError
      ? retainedReadFailureOf(keepProbe.error)
      : "checking";
  const [typed, setTyped] = useState("");
  const [data, setData] = useState<DataChoice>("delete");
  const matches = typed === envName;

  const bound = (domains.data?.availability === "available" ? domains.data.domains : [])
    .filter((domain) => domain.binding?.environmentId === environmentId)
    .map((domain) => domain.hostname)
    .sort((a, b) => a.localeCompare(b));
  const refusedForDomains = del.isError && isDomainBoundRefusal(del.error);
  const unbindFirst = bound.length > 0 || refusedForDomains || unbindAndDelete.steps.length > 0;
  const pending = del.isPending || unbindAndDelete.isPending;

  const close = () => {
    if (pending) return;
    setTyped("");
    setData("delete");
    del.reset();
    unbindAndDelete.reset();
    onClose();
  };
  const done = (keptData: boolean) => {
    if (keptData && (databases === null || databases.length > 0)) {
      startKeptDataCheck({ environmentId, envName, project, databases: databases ?? [] });
    }
    setTyped("");
    setData("delete");
    del.reset();
    unbindAndDelete.reset();
    onClose();
    onDeleted?.();
  };
  const keepData = data === "keep" && keepSupport === "supported" && databases?.length !== 0;
  const confirm = () => {
    if (unbindFirst) {
      del.reset();
      unbindAndDelete.mutate({ keepData }, { onSuccess: () => done(keepData) });
    } else {
      del.mutate({ keepData }, { onSuccess: () => done(keepData) });
    }
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
            <li>Its generated hostnames: the platform URLs stop answering</li>
            <li>Secret delivery to it</li>
          </ul>
        </div>
        <div className="space-y-1.5" data-testid="delete-environment-kept">
          <p className="font-medium">Kept</p>
          <ul className="list-disc space-y-1 pl-5 text-ink-muted">
            <li>
              Releases and their images. Run <code className="font-mono text-ink">reliant forge env deploy {envName}</code> or use
              Deploy to create it again, empty.
            </li>
            <li>Secrets in the store: a new {envName} gets them back</li>
            <li>Custom domains: they stay in your organization, unbound</li>
          </ul>
        </div>

        {databases?.length === 0 ? (
          <p className="text-ink-muted" data-testid="delete-environment-no-databases">
            It runs no databases, so there is no data to keep.
          </p>
        ) : keepSupport !== "supported" && keepSupport !== "checking" ? (
          <div className="space-y-1" data-testid="delete-environment-keep-unavailable" data-reason={keepSupport}>
            <p className="font-medium">Databases</p>
            <p>Its databases are deleted with it, data included. This cannot be undone.</p>
            <p className="text-ink-muted">
              Keep data isn&apos;t offered: {RETAINED_READ_FAILURE_COPY[keepSupport]} A kept database could not be
              confirmed or purged here.
              {keepSupport === "unavailable" && cloudErrorDetail(keepProbe.error) && (
                <> ({cloudErrorDetail(keepProbe.error)})</>
              )}
            </p>
            {keepSupport === "unavailable" && (
              <Button
                variant="outline"
                size="sm"
                onClick={() => void keepProbe.refetch()}
                disabled={pending || keepProbe.isFetching}
                loading={keepProbe.isFetching}
                data-testid="delete-environment-keep-retry"
              >
                Check again
              </Button>
            )}
          </div>
        ) : (
          <DataChoiceField value={data} onChange={setData} disabled={pending} keepChecking={keepSupport === "checking"} />
        )}

        {bound.length > 0 && (
          <div className="space-y-1.5" data-testid="delete-environment-bound-domains">
            <p className="font-medium">Custom domains bound to {envName}</p>
            <p className="text-ink-muted">
              An environment a custom domain points at can&apos;t be deleted. These are unbound first, then {envName} is
              deleted:
            </p>
            <ul className="list-disc pl-5">
              {bound.map((hostname) => (
                <li key={hostname} className="font-mono">
                  {hostname}
                </li>
              ))}
            </ul>
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

        {refusedForDomains && unbindAndDelete.steps.length === 0 && (
          <div role="alert" className="space-y-1 text-xs" data-testid="delete-environment-domain-refusal">
            <p className="text-danger-ink">{refusalText(del.error, DELETE_ENV_NEEDS_ADMIN)}</p>
            <p className="text-ink-muted">
              Unbind domains and delete unbinds every custom domain that points at {envName}, then deletes it. The domains
              stay in your organization.
            </p>
          </div>
        )}
        {del.isError && !refusedForDomains && (
          <p role="alert" className="text-xs text-danger-ink" data-testid="delete-environment-error">
            {refusalText(del.error, DELETE_ENV_NEEDS_ADMIN)}
          </p>
        )}
        {unbindAndDelete.steps.length > 0 && (
          <TeardownProgress steps={unbindAndDelete.steps} failed={unbindAndDelete.isError} envName={envName} />
        )}

        <div className="flex justify-end gap-2">
          <Button variant="outline" size="sm" onClick={close} disabled={pending}>
            Cancel
          </Button>
          <Button
            variant="danger"
            size="sm"
            loading={pending}
            disabled={!matches || pending}
            onClick={confirm}
            data-testid="delete-environment-confirm"
          >
            {unbindFirst ? "Unbind domains and delete" : "Delete environment"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function DataChoiceField({
  value,
  onChange,
  disabled,
  keepChecking,
}: {
  value: DataChoice;
  onChange: (value: DataChoice) => void;
  disabled: boolean;
  /** Keep data waits until the control plane is known to support it. */
  keepChecking: boolean;
}) {
  const name = useId();
  const option = (choice: DataChoice, label: string, detail: string, off = false) => (
    <label
      className={cn(
        "flex cursor-pointer items-start gap-2.5 rounded-md border px-3 py-2",
        value === choice ? "border-ink/40 bg-ink/5" : "border-border hover:bg-ink/5",
        (disabled || off) && "cursor-not-allowed opacity-60"
      )}
    >
      <input
        type="radio"
        name={name}
        checked={value === choice}
        onChange={() => onChange(choice)}
        disabled={disabled || off}
        className="mt-0.5 accent-current"
        data-testid={`delete-environment-data-${choice}`}
      />
      <span>
        <span className="block font-medium text-ink">{label}</span>
        <span className="block text-xs text-ink-muted">{detail}</span>
      </span>
    </label>
  );
  return (
    <fieldset className="space-y-2" data-testid="delete-environment-data-note">
      <legend className="mb-1.5 font-medium">Databases</legend>
      {option("delete", "Delete the data", "Its databases are deleted with it, data included. This cannot be undone.")}
      {option(
        "keep",
        "Keep data",
        keepChecking
          ? "Checking whether this control plane can keep them…"
          : "Its databases are kept and keep billing as storage. They are listed under Kept databases on the Environments page until you purge them. A new deploy does not reattach them.",
        keepChecking
      )}
    </fieldset>
  );
}

function TeardownProgress({ steps, failed, envName }: { steps: TeardownStep[]; failed: boolean; envName: string }) {
  const deleteStep = steps.find((step) => step.id === "delete");
  const unbound = steps.filter((step) => step.id.startsWith("unbind:") && step.state === "done").length;
  return (
    <div className="space-y-2" data-testid="delete-environment-progress" aria-live="polite">
      <ol className="space-y-1.5">
        {steps.map((step) => (
          <li key={step.id} className="flex items-start gap-2 text-xs" data-testid={`teardown-step-${step.id}`} data-state={step.state}>
            <StepIcon state={step.state} />
            <span className="min-w-0">
              <span className={cn(step.state === "pending" ? "text-ink-muted" : "text-ink")}>{step.label}</span>
              {step.error && <span className="block text-danger-ink">{step.error}</span>}
            </span>
          </li>
        ))}
      </ol>
      {failed && (
        <p role="alert" className="text-xs text-ink" data-testid="delete-environment-progress-failed">
          {deleteStep?.state === "failed"
            ? `${unbound > 0 ? `The domains were unbound, but ${envName} was not deleted.` : `${envName} was not deleted.`} Try again, or bind the domains again on the Domains page.`
            : `${envName} was not deleted.${unbound > 0 ? " The domains already unbound stay unbound." : ""} Try again, or unbind the rest on the Domains page.`}
        </p>
      )}
    </div>
  );
}

function StepIcon({ state }: { state: TeardownStep["state"] }) {
  const cls = "mt-0.5 h-3.5 w-3.5 shrink-0";
  switch (state) {
    case "running":
      return <Loader2 className={cn(cls, "animate-spin text-ink-muted")} aria-label="In progress" />;
    case "done":
      return <Check className={cn(cls, "text-success-ink")} aria-label="Done" />;
    case "failed":
      return <X className={cn(cls, "text-danger-ink")} aria-label="Failed" />;
    default:
      return <Circle className={cn(cls, "text-ink-muted")} aria-label="Not started" />;
  }
}

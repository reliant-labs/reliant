// Copyright (c) 2025 Reliant Labs

/**
 * Settings › Members › Automations (admins and owners): the automations and
 * saved workflows of people who left the org. Their triggers were paused when
 * their creator left, because every run executes as its owner, and nobody
 * else could reach them.
 *
 * An admin adopts one (it becomes theirs, paused), or offers it to a member,
 * who must accept: an admin never makes someone else the owner of an
 * automation. Saved workflows run nothing, so they are forked, never
 * re-owned. Param values are not shown here: they were the creator's.
 * control-plane docs/design/org-sharing.md §8.
 */

import { useId, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import type { OrphanedTrigger, OrphanedWorkflow } from "@/gen/reliant/v1/automation_takeover_pb";
import {
  automationTakeoverServiceKeys,
  useAdoptTrigger,
  useListOrphanedAutomations,
  useRevokeTriggerOffer,
} from "@/hooks/automation-takeover-service-hooks_gen";
import { useProjects } from "@/hooks/project-queries";
import { useForkOrphanedWorkflow } from "@/hooks/workflow-service-hooks_gen";
import { formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import type { OrgMember } from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";
import { useAuthStore } from "@/store/authStore";

import { AdoptAutomationDialog, type AdoptTarget } from "./AdoptAutomationDialog";
import { memberLabel } from "./MemberRoster";
import { SELECT_CLASS, sourceLabel, takeoverErrorMessage } from "./takeover";

export function OrphanedAutomations({ org, members }: { org: MyOrg; members: OrgMember[] }) {
  const qc = useQueryClient();
  const orphans = useListOrphanedAutomations({ orgId: org.id }, { meta: { silenceErrorToast: true } });
  const refresh = () => qc.invalidateQueries({ queryKey: automationTakeoverServiceKeys.all });
  const revoke = useRevokeTriggerOffer({ meta: { silenceErrorToast: true } });
  const [adopting, setAdopting] = useState<AdoptTarget | null>(null);
  const [offering, setOffering] = useState<OrphanedTrigger | null>(null);
  const [forking, setForking] = useState<OrphanedWorkflow | null>(null);
  const [withdrawing, setWithdrawing] = useState<OrphanedTrigger | null>(null);
  const [error, setError] = useState<string | null>(null);

  const nameOf = (userId: string) => {
    const m = members.find((x) => x.userId === userId);
    return m ? memberLabel(m) : "a former member";
  };

  const withdraw = async () => {
    const offer = withdrawing?.pendingOffer;
    if (!offer) return;
    setError(null);
    try {
      await revoke.mutateAsync({ offerId: offer.id });
      toast.success(`Withdrew the offer to ${nameOf(offer.offereeUserId)}.`);
      await refresh();
    } catch (err) {
      setError(takeoverErrorMessage(err, "Could not withdraw the offer."));
    } finally {
      setWithdrawing(null);
    }
  };

  const triggers = orphans.data?.triggers ?? [];
  const workflows = orphans.data?.workflows ?? [];

  return (
    <div className="space-y-6" data-testid="orphaned-automations">
      <section className="rounded-lg border border-border bg-card" aria-labelledby="orphans-heading">
        <div className="border-b border-border px-4 py-3">
          <h3 id="orphans-heading" className="text-sm font-semibold text-foreground">
            Automations left by former members
          </h3>
          <p className="mt-0.5 text-xs text-muted-foreground">
            Paused when their creator left. Adopt one to run it as yourself, or offer it to a member who accepts it.
          </p>
        </div>
        {error && (
          <p role="alert" className="border-b border-border px-4 py-3 text-sm text-destructive-ink">
            {error}
          </p>
        )}
        {orphans.isPending ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
            Loading…
          </p>
        ) : orphans.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert" data-testid="orphans-error">
            {takeoverErrorMessage(orphans.error, "Could not load the automations former members left.")}
          </p>
        ) : triggers.length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" data-testid="orphans-empty">
            No automations are waiting for a new owner.
          </p>
        ) : (
          <ul className="divide-y divide-border">
            {triggers.map((o) => {
              const t = o.trigger;
              const offer = o.pendingOffer;
              return (
                <li key={t?.id} className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={`orphan-${t?.id}`}>
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-sm font-medium text-foreground">{t?.name}</p>
                    <p className="truncate text-xs text-muted-foreground">
                      {sourceLabel(t)} · made by {nameOf(o.creatorUserId)}
                      {o.paramNames.length > 0 && ` · sets ${o.paramNames.join(", ")}`}
                    </p>
                    {offer && (
                      <p className="truncate text-xs text-muted-foreground" data-testid={`orphan-offer-${t?.id}`}>
                        Offered to {nameOf(offer.offereeUserId)}
                        {offer.expiresAt && ` · expires ${formatRelativeTime(offer.expiresAt)}`}
                      </p>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    {offer ? (
                      <Button variant="ghost" size="sm" onClick={() => setWithdrawing(o)} aria-label={`Withdraw the offer of ${t?.name}`}>
                        Withdraw offer
                      </Button>
                    ) : (
                      <Button variant="ghost" size="sm" onClick={() => setOffering(o)} aria-label={`Offer ${t?.name} to a member`}>
                        Offer to…
                      </Button>
                    )}
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => t && setAdopting({ kind: "adopt", trigger: t })}
                      aria-label={`Adopt ${t?.name}`}
                      data-testid={`orphan-adopt-${t?.id}`}
                    >
                      Adopt
                    </Button>
                  </div>
                </li>
              );
            })}
          </ul>
        )}
      </section>

      {workflows.length > 0 && (
        <section className="rounded-lg border border-border bg-card" aria-labelledby="orphan-workflows-heading">
          <div className="border-b border-border px-4 py-3">
            <h3 id="orphan-workflows-heading" className="text-sm font-semibold text-foreground">
              Saved workflows left by former members
            </h3>
            <p className="mt-0.5 text-xs text-muted-foreground">A saved workflow runs nothing. Fork it to get a copy of your own.</p>
          </div>
          <ul className="divide-y divide-border">
            {workflows.map((w) => (
              <li key={w.id} className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={`orphan-workflow-${w.id}`}>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-sm font-medium text-foreground">{w.name}</p>
                  <p className="truncate text-xs text-muted-foreground">
                    {w.slug} · made by {nameOf(w.creatorUserId)}
                  </p>
                </div>
                <Button variant="outline" size="sm" onClick={() => setForking(w)} aria-label={`Fork ${w.name}`}>
                  Fork
                </Button>
              </li>
            ))}
          </ul>
        </section>
      )}

      <AdoptAutomationDialog orgId={org.id} target={adopting} onClose={() => setAdopting(null)} onDone={() => void refresh()} />
      <OfferDialog org={org} orphan={offering} members={members} onClose={() => setOffering(null)} onDone={() => void refresh()} />
      <ForkDialog org={org} workflow={forking} onClose={() => setForking(null)} />
      <ConfirmationDialog
        open={withdrawing !== null}
        title="Withdraw this offer?"
        description={withdrawing?.pendingOffer ? `${nameOf(withdrawing.pendingOffer.offereeUserId)} can no longer accept it.` : undefined}
        confirmLabel="Withdraw"
        loading={revoke.isPending}
        onConfirm={() => void withdraw()}
        onCancel={() => setWithdrawing(null)}
      />
    </div>
  );
}

/**
 * Offering an automation to a member: they see it under Members and accept it
 * with their own project and machine, or decline it. Nothing changes hands
 * until they accept.
 */
function OfferDialog({
  org,
  orphan,
  members,
  onClose,
  onDone,
}: {
  org: MyOrg;
  orphan: OrphanedTrigger | null;
  members: OrgMember[];
  onClose: () => void;
  onDone: () => void;
}) {
  const fieldId = useId();
  const me = useAuthStore((s) => s.user);
  const others = useMemo(() => members.filter((m) => !me || m.userId !== me.id), [members, me]);
  const [to, setTo] = useState("");
  const [error, setError] = useState<string | null>(null);
  const offer = useAdoptTrigger({ meta: { silenceErrorToast: true } });
  const name = orphan?.trigger?.name ?? "";
  const chosen = to || others[0]?.userId || "";

  const submit = async () => {
    if (!orphan?.trigger || !chosen) return;
    setError(null);
    try {
      await offer.mutateAsync({ triggerId: orphan.trigger.id, newOwnerUserId: chosen });
      const m = others.find((x) => x.userId === chosen);
      toast.success(`Offered “${name}” to ${m ? memberLabel(m) : "them"}. It is theirs once they accept.`);
      onDone();
      onClose();
    } catch (err) {
      setError(takeoverErrorMessage(err, `Could not offer “${name}”.`));
    }
  };

  return (
    <Modal open={orphan !== null} onClose={onClose} title={`Offer “${name}”`} size="sm">
      <div className="space-y-4" data-testid="offer-dialog">
        <p className="text-sm text-muted-foreground">
          They choose its project and machine when they accept, and its runs then act as them. The offer lasts 7 days.
        </p>
        {others.length === 0 ? (
          <p className="text-sm text-muted-foreground">There is nobody else in {org.name} to offer it to.</p>
        ) : (
          <div className="space-y-1.5">
            <label htmlFor={fieldId} className="block text-sm font-medium text-foreground">
              Member
            </label>
            <select id={fieldId} className={SELECT_CLASS} value={chosen} onChange={(e) => setTo(e.target.value)} data-testid="offer-member">
              {others.map((m) => (
                <option key={m.userId} value={m.userId}>
                  {memberLabel(m)}
                </option>
              ))}
            </select>
          </div>
        )}
        {error && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="offer-error">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={offer.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} loading={offer.isPending} disabled={!chosen || offer.isPending} data-testid="offer-submit">
            Offer
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/** Forking a former member's saved workflow into one of your own. */
function ForkDialog({ org, workflow, onClose }: { org: MyOrg; workflow: OrphanedWorkflow | null; onClose: () => void }) {
  const projectFieldId = useId();
  const nameFieldId = useId();
  const { projects } = useProjects();
  const orgProjects = useMemo(() => projects.filter((p) => p.org_id === org.id), [projects, org.id]);
  const [projectId, setProjectId] = useState("");
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const fork = useForkOrphanedWorkflow({ meta: { silenceErrorToast: true } });
  const chosenProject = projectId || orgProjects[0]?.id || "";

  const submit = async () => {
    if (!workflow || !chosenProject) return;
    setError(null);
    try {
      const resp = await fork.mutateAsync({ workflowId: workflow.id, projectId: chosenProject, newName: name.trim() || undefined });
      toast.success(`Forked “${workflow.name}” as ${resp.slug}.`);
      onClose();
    } catch (err) {
      setError(takeoverErrorMessage(err, `Could not fork “${workflow.name}”.`));
    }
  };

  return (
    <Modal open={workflow !== null} onClose={onClose} title={workflow ? `Fork “${workflow.name}”` : undefined} size="sm">
      <div className="space-y-4" data-testid="fork-dialog">
        <div className="space-y-1.5">
          <label htmlFor={projectFieldId} className="block text-sm font-medium text-foreground">
            Project
          </label>
          <select id={projectFieldId} className={SELECT_CLASS} value={chosenProject} onChange={(e) => setProjectId(e.target.value)} data-testid="fork-project">
            {orgProjects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor={nameFieldId} className="block text-sm font-medium text-foreground">
            Name <span className="font-normal text-muted-foreground">(optional)</span>
          </label>
          <Input id={nameFieldId} value={name} onChange={(e) => setName(e.target.value)} placeholder={workflow?.name} data-testid="fork-name" />
        </div>
        {error && (
          <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" onClick={onClose} disabled={fork.isPending}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} loading={fork.isPending} disabled={!chosenProject || fork.isPending} data-testid="fork-submit">
            Fork
          </Button>
        </div>
      </div>
    </Modal>
  );
}

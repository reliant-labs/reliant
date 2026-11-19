// Copyright (c) 2025 Reliant Labs

/**
 * An agent's request that the person connect an integration
 * (propose_connection, reliant #2831). The card says what and why; "Connect"
 * opens the app's own connection dialog (ConnectIntegrationDialog), where the
 * person signs in or enters the key. Making the connection IS the approval:
 * its id goes to ApprovalService.Approve, which checks it against the
 * proposal. Closing the dialog without connecting answers the agent "not
 * completed"; Decline answers "declined". The agent never sees the
 * credential, only the connection's id and name.
 *
 * Whose connection: an org-wide one is offered only to an owner or admin of
 * the chat's organization (the server refuses anyone else); everyone else
 * gets their own, or is told to ask an org admin when the agent asked for the
 * org's.
 *
 * A web sign-in leaves the page and comes back through
 * `/connections/oauth/callback`, which approves the card from the
 * `connection_proposal` parameter this card puts on the return path
 * (ConnectionOAuthCallback).
 */

import { useMemo, useRef, useState } from "react";
import { Plug } from "lucide-react";

import { ApprovalCardView } from "./ApprovalCardView";
import { ConnectionProposalScope } from "../../gen/reliant/v1/approval_pb";
import {
  ConnectIntegrationDialog,
  type ConnectIntegrationTarget,
} from "../workflow/connections/ConnectIntegrationDialog";
import { useIntegrations, type Connection } from "../../hooks/connection-queries";
import { useActiveOrg } from "../../hooks/useOrgs";
import { canAdministerMembers } from "../../services/controlPlane/members";
import { cn } from "../../lib/utils";
import { useSurface } from "../../lib/surfaceContext";
import { proposalReturnPath, type ProposalApproval } from "../../lib/connectionProposal";

type Owner = "personal" | "org";

export interface ConnectionProposalCardProps {
  approval: ProposalApproval;
  /** Approve with the connection the person made. */
  onConnected: (connectionId: string) => void;
  /** Deny: declined, or (`notCompleted`) the dialog closed without a connection. */
  onDecline: (notCompleted: boolean) => void;
  disabled?: boolean;
}

export function ConnectionProposalCard({ approval, onConnected, onDecline, disabled }: ConnectionProposalCardProps) {
  const surface = useSurface();
  const proposal = approval.connectionProposal;
  const name = proposal.integrationName || proposal.integrationId;
  const { orgs } = useActiveOrg();
  const org = proposal.orgId ? orgs.find((o) => o.id === proposal.orgId) : undefined;
  const isOrgAdmin = !!org && canAdministerMembers(org.role);

  // Which owners the person may choose. The server holds the same rule.
  const owners = useMemo<Owner[]>(() => {
    switch (proposal.scope) {
      case ConnectionProposalScope.PERSONAL:
        return ["personal"];
      case ConnectionProposalScope.ORG:
        return isOrgAdmin ? ["org"] : [];
      default:
        return isOrgAdmin ? ["personal", "org"] : ["personal"];
    }
  }, [proposal.scope, isOrgAdmin]);
  const [owner, setOwner] = useState<Owner | null>(null);
  const chosen = owner && owners.includes(owner) ? owner : owners[0];

  const integrations = useIntegrations();
  const integration = integrations.data?.find((i) => i.id === proposal.integrationId);
  const [target, setTarget] = useState<ConnectIntegrationTarget | null>(null);
  const connected = useRef(false);

  const open = () => {
    connected.current = false;
    setTarget(
      integration
        ? {
            integrationId: integration.id,
            displayName: integration.displayName,
            icon: integration.id,
            connection: { methods: integration.methods, params: integration.params },
          }
        : { integrationId: proposal.integrationId, displayName: name, icon: proposal.integrationId, ref: proposal.integrationId },
    );
  };
  const handleConnected = (connection: Connection) => {
    connected.current = true;
    onConnected(connection.id);
  };
  const handleClose = () => {
    setTarget(null);
    if (!connected.current) onDecline(true);
  };

  const orgName = org?.name ?? "your organization";
  return (
    <div className="space-y-2.5" data-testid={`connection-proposal-${approval.id}`}>
      <ApprovalCardView title={approval.title} />
      {owners.length === 0 ? (
        <p className="text-xs text-warning-ink" role="note">
          The agent asked for a connection {orgName} owns. Only an owner or admin of {orgName} can make one: ask one of them to
          connect {name} in Settings, Integrations, or decline.
        </p>
      ) : owners.length > 1 ? (
        <fieldset className="space-y-1">
          <legend className="text-xs font-medium text-muted-foreground">Whose connection</legend>
          <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-foreground">
            {owners.map((o) => (
              <label key={o} className="flex items-center gap-1.5">
                <input
                  type="radio"
                  name={`proposal-owner-${approval.id}`}
                  value={o}
                  checked={chosen === o}
                  onChange={() => setOwner(o)}
                  disabled={disabled}
                />
                {o === "personal" ? "Yours" : `${orgName}'s`}
              </label>
            ))}
          </div>
        </fieldset>
      ) : null}
      <div className={cn("flex items-center gap-2", surface === "desktop" ? "justify-end" : "w-full flex-wrap")}>
        <button
          type="button"
          onClick={() => onDecline(false)}
          disabled={disabled}
          aria-label={`Decline connecting ${name}`}
          className={cn(
            "rounded-md border border-border bg-background font-medium text-destructive-ink hover:bg-foreground/5 disabled:opacity-50",
            surface === "desktop" ? "h-7 px-2.5 text-xs" : "min-h-[44px] flex-1 px-3 text-sm",
          )}
        >
          Decline
        </button>
        {chosen && (
          <button
            type="button"
            onClick={open}
            disabled={disabled}
            className={cn(
              "inline-flex items-center justify-center gap-1.5 rounded-md bg-primary font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50",
              surface === "desktop" ? "h-7 px-2.5 text-xs" : "min-h-[44px] flex-1 px-3 text-sm",
            )}
          >
            <Plug className="h-3.5 w-3.5" aria-hidden="true" />
            Connect {name}…
          </button>
        )}
      </div>
      <ConnectIntegrationDialog
        target={target}
        onClose={handleClose}
        onConnected={handleConnected}
        redirectAfter={proposalReturnPath(approval.id)}
        owner={chosen === "org" && org ? { orgId: org.id, orgName: org.name } : undefined}
      />
    </div>
  );
}

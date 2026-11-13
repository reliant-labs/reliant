// Copyright (c) 2025 Reliant Labs

/**
 * The dialogs that take an org key: adding one, and rotating one in place.
 *
 * The key is typed into a password field, sent once, and never shown again:
 * no rpc returns an org key, to an admin or anyone else (org-provider-accounts
 * §3.5). The server checks it with the provider before it stores anything, so
 * a refused key comes back as the provider's own sentence.
 */

import { useEffect, useId, useState } from "react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import {
  useAddOrgProviderAPIKey,
  useRotateOrgProviderAPIKey,
  useUpdateOrgProviderAccount,
} from "@/hooks/org-provider-account-service-hooks_gen";
import {
  ACCESS_TO_PROTO,
  ORG_KEY_PROVIDERS,
  accessLabel,
  orgAccountErrorMessage,
  type OrgAccount,
  type OrgAccountAccess,
} from "@/types/orgProviderAccount";
import type { MyOrg } from "@/services/controlPlane/orgs";

const fieldClass =
  "block w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";

const ACCESS_HINT: Record<OrgAccountAccess, string> = {
  members: "Every member's chats in this organization may use it.",
  admins: "Only owners' and admins' chats may use it.",
  selected: "Only the people you pick on Members › Permissions may use it.",
};

/** The access choices, for a select. */
export function AccessOptions({ orgName }: { orgName: string }) {
  return (
    <>
      {(["members", "admins", "selected"] as const).map((a) => (
        <option key={a} value={a}>
          {accessLabel(a, orgName)}
        </option>
      ))}
    </>
  );
}

function ErrorLine({ error, testId }: { error: string | null; testId: string }) {
  if (!error) return null;
  return (
    <p role="alert" data-testid={testId} className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink">
      {error}
    </p>
  );
}

/** Add an API key the org owns: provider, label, key and who may use it. */
export function AddOrgKeyDialog({ open, onClose, org }: { open: boolean; onClose: () => void; org: MyOrg }) {
  const add = useAddOrgProviderAPIKey();
  const [provider, setProvider] = useState(ORG_KEY_PROVIDERS[0].id);
  const [label, setLabel] = useState("");
  const [key, setKey] = useState("");
  const [access, setAccess] = useState<OrgAccountAccess>("members");
  const [error, setError] = useState<string | null>(null);
  const ids = { provider: useId(), label: useId(), key: useId(), access: useId() };

  useEffect(() => {
    if (!open) return;
    setProvider(ORG_KEY_PROVIDERS[0].id);
    setLabel("");
    setKey("");
    setAccess("members");
    setError(null);
  }, [open]);

  const valid = label.trim() !== "" && key.trim() !== "";
  const submit = async () => {
    if (!valid || add.isPending) return;
    setError(null);
    try {
      await add.mutateAsync({ orgId: org.id, provider, apiKey: key.trim(), label: label.trim(), access: ACCESS_TO_PROTO[access] });
      setKey("");
      onClose();
    } catch (err) {
      setError(orgAccountErrorMessage(err, "Could not add the key. Try again."));
    }
  };
  const placeholder = ORG_KEY_PROVIDERS.find((p) => p.id === provider)?.placeholder;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Add an API key for ${org.name}`}
      description={`${org.name} pays for it on its own provider bill. It is checked with the provider before it is saved, and nobody can read it back.`}
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={add.isPending} disabled={!valid} onClick={() => void submit()} data-testid="org-key-add-submit">
            Add key
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor={ids.provider} className="block text-sm font-medium text-foreground">
            Provider
          </label>
          <select id={ids.provider} value={provider} onChange={(e) => setProvider(e.target.value)} className={fieldClass} data-testid="org-key-provider">
            {ORG_KEY_PROVIDERS.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.label} className="block text-sm font-medium text-foreground">
            Label
          </label>
          <Input id={ids.label} value={label} onChange={(e) => setLabel(e.target.value)} placeholder={`${org.name} prod`} maxLength={80} data-testid="org-key-label" />
          <p className="text-xs text-muted-foreground">What members see when a chat runs on it.</p>
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.key} className="block text-sm font-medium text-foreground">
            API key
          </label>
          <Input
            id={ids.key}
            type="password"
            autoComplete="off"
            spellCheck={false}
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder={placeholder}
            data-testid="org-key-secret"
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor={ids.access} className="block text-sm font-medium text-foreground">
            Who may use it
          </label>
          <select id={ids.access} value={access} onChange={(e) => setAccess(e.target.value as OrgAccountAccess)} className={fieldClass} data-testid="org-key-access">
            <AccessOptions orgName={org.name} />
          </select>
          <p className="text-xs text-muted-foreground">{ACCESS_HINT[access]}</p>
        </div>
        <ErrorLine error={error} testId="org-key-add-error" />
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}

/** Replace an org key in place: the account keeps its id, label, order, access and grants. */
export function RotateOrgKeyDialog({ account, onClose, org }: { account: OrgAccount | null; onClose: () => void; org: MyOrg }) {
  const rotate = useRotateOrgProviderAPIKey();
  const [key, setKey] = useState("");
  const [error, setError] = useState<string | null>(null);
  const keyId = useId();

  useEffect(() => {
    setKey("");
    setError(null);
  }, [account?.account.id]);

  const submit = async () => {
    if (!account || !key.trim() || rotate.isPending) return;
    setError(null);
    try {
      await rotate.mutateAsync({ orgId: org.id, accountId: account.account.id, apiKey: key.trim() });
      setKey("");
      onClose();
    } catch (err) {
      setError(orgAccountErrorMessage(err, "Could not rotate the key. Try again."));
    }
  };

  return (
    <Modal
      open={account !== null}
      onClose={onClose}
      title={account ? `Rotate ${account.account.label}` : "Rotate key"}
      description="Chats on this key move to the new one at their next call. Its label, order and who may use it stay as they are."
      size="md"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={rotate.isPending} disabled={!key.trim()} onClick={() => void submit()} data-testid="org-key-rotate-submit">
            Rotate key
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor={keyId} className="block text-sm font-medium text-foreground">
            New {account?.account.providerDisplayName ?? ""} API key
          </label>
          <Input id={keyId} type="password" autoComplete="off" spellCheck={false} value={key} onChange={(e) => setKey(e.target.value)} data-testid="org-key-rotate-secret" />
        </div>
        <ErrorLine error={error} testId="org-key-rotate-error" />
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}

/** Rename an org key: what members see when a chat runs on it. */
export function RenameOrgKeyDialog({ account, onClose, org }: { account: OrgAccount | null; onClose: () => void; org: MyOrg }) {
  const update = useUpdateOrgProviderAccount();
  const [label, setLabel] = useState("");
  const [error, setError] = useState<string | null>(null);
  const labelId = useId();

  useEffect(() => {
    setLabel(account?.account.label ?? "");
    setError(null);
  }, [account?.account.id, account?.account.label]);

  const submit = async () => {
    if (!account || !label.trim() || update.isPending) return;
    setError(null);
    try {
      await update.mutateAsync({ orgId: org.id, accountId: account.account.id, label: label.trim() });
      onClose();
    } catch (err) {
      setError(orgAccountErrorMessage(err, "Could not rename the key. Try again."));
    }
  };

  return (
    <Modal
      open={account !== null}
      onClose={onClose}
      title="Rename key"
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={update.isPending} disabled={!label.trim()} onClick={() => void submit()}>
            Save
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor={labelId} className="block text-sm font-medium text-foreground">
            Label
          </label>
          <Input id={labelId} value={label} maxLength={80} onChange={(e) => setLabel(e.target.value)} data-testid="org-key-rename-label" />
        </div>
        <ErrorLine error={error} testId="org-key-rename-error" />
        <button type="submit" hidden />
      </form>
    </Modal>
  );
}

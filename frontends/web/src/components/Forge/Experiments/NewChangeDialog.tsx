// Copyright (c) 2025 Reliant Labs

/**
 * "New change": the short form that drafts a change in one environment.
 *
 * The key comes from the project's changes.yaml. The keys this page already
 * knows a release declares are offered; any other key is accepted with a
 * note that it is not declared yet: it can be drafted and previewed now, and
 * launched once a release that declares it is deployed.
 *
 * "Who gets it?" is asked here because it is the reader's intent, but a new
 * change always starts off: the answer only opens the change on the matching
 * controls. Nobody gets anything until someone decides to on the change.
 */

import { useEffect, useId, useState } from "react";
import { useNavigate } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { useCreateChange } from "@/hooks/controlplane/changes-service-hooks_gen";
import { NO_REFETCH, useRememberChange } from "@/hooks/changes-queries";
import { createChangeRequest } from "@/services/controlPlane/changes/changesRequests";
import type { ChangeUnit } from "@/services/controlPlane/changes/changesTypes";
import { describeChangeError } from "@/services/controlPlane/changes/errors";
import { withSearch } from "../Console/paths";
import { FIELD, segmentTone, SEGMENTED } from "../controlStyles";

import { CHANGE_KEY, UNIT_LABELS, WHO_GETS_IT, type WhoGetsIt } from "./changeModel";

export interface NewChangeEnv {
  id: string;
  name: string;
}

export interface NewChangeDialogProps {
  open: boolean;
  onClose: () => void;
  envs: NewChangeEnv[];
  defaultEnvId: string;
  /** Keys a release of this project is known to declare. */
  declaredKeys: string[];
  changeHref: (changeId: string) => string;
}

export function NewChangeDialog({ open, onClose, envs, defaultEnvId, declaredKeys, changeHref }: NewChangeDialogProps) {
  const remember = useRememberChange();
  const navigate = useNavigate();
  const id = useId();
  const [envId, setEnvId] = useState(defaultEnvId);
  const [key, setKey] = useState("");
  const [description, setDescription] = useState("");
  const [who, setWho] = useState<WhoGetsIt>("some");
  const [unit, setUnit] = useState<ChangeUnit>("user");

  useEffect(() => {
    if (open) setEnvId((current) => (envs.some((e) => e.id === current) ? current : defaultEnvId));
  }, [open, envs, defaultEnvId]);

  const create = useCreateChange({
    ...NO_REFETCH,
    onSuccess: ({ change }) => {
      remember(change);
      onClose();
      setKey("");
      setDescription("");
      void navigate({ href: withSearch(changeHref(change?.id ?? ""), { who }) });
    },
  });
  const submit = () => create.mutate(createChangeRequest({ environmentId: envId, key: key.trim(), description: description.trim(), unit }));

  const trimmed = key.trim();
  const keyValid = CHANGE_KEY.test(trimmed);
  const undeclared = trimmed !== "" && keyValid && !declaredKeys.includes(trimmed);
  const canSubmit = keyValid && !!envId && !create.isPending;

  return (
    <Modal
      open={open}
      onClose={onClose}
      title="New change"
      description="A change starts off. You decide who gets it on the next screen."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" onClick={submit} disabled={!canSubmit} loading={create.isPending} data-testid="new-change-submit">
            Create change
          </Button>
        </>
      }
    >
      <form
        className="space-y-4"
        onSubmit={(event) => {
          event.preventDefault();
          if (canSubmit) submit();
        }}
      >
        <div className="space-y-1.5">
          <label htmlFor={`${id}-key`} className="block text-ui font-medium text-ink">
            Key
          </label>
          <input
            id={`${id}-key`}
            list={`${id}-keys`}
            value={key}
            onChange={(event) => setKey(event.target.value)}
            placeholder="new-checkout"
            autoComplete="off"
            spellCheck={false}
            className={`${FIELD} w-full font-mono`}
            aria-invalid={trimmed !== "" && !keyValid}
            data-testid="new-change-key"
          />
          <datalist id={`${id}-keys`}>
            {declaredKeys.map((k) => (
              <option key={k} value={k} />
            ))}
          </datalist>
          {trimmed !== "" && !keyValid ? (
            <p className="text-meta text-danger-ink">Use lowercase letters, digits, dots, dashes and underscores, starting with a letter.</p>
          ) : undeclared ? (
            <p className="text-meta text-ink-muted" data-testid="new-change-undeclared">
              Not declared yet. You can draft and preview it now; launching it waits for a release whose changes.yaml declares <code className="font-mono">{trimmed}</code>.
            </p>
          ) : (
            <p className="text-meta text-ink-muted">The key your app reads, as changes.yaml declares it.</p>
          )}
        </div>

        <div className="space-y-1.5">
          <label htmlFor={`${id}-env`} className="block text-ui font-medium text-ink">
            Environment
          </label>
          <select id={`${id}-env`} value={envId} onChange={(event) => setEnvId(event.target.value)} className={`${FIELD} w-full`} data-testid="new-change-env">
            {envs.map((e) => (
              <option key={e.id} value={e.id}>
                {e.name}
              </option>
            ))}
          </select>
        </div>

        <div className="space-y-1.5">
          <label htmlFor={`${id}-desc`} className="block text-ui font-medium text-ink">
            Description <span className="font-normal text-ink-muted">(optional)</span>
          </label>
          <textarea
            id={`${id}-desc`}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
            rows={2}
            maxLength={2000}
            placeholder="What this changes, and why"
            className={`${FIELD} h-auto w-full py-1.5`}
          />
        </div>

        <fieldset className="space-y-1.5">
          <legend className="text-ui font-medium text-ink">Who gets it?</legend>
          <div className={SEGMENTED} role="radiogroup" aria-label="Who gets it?">
            {WHO_GETS_IT.map((option) => (
              <button
                key={option.value}
                type="button"
                role="radio"
                aria-checked={who === option.value}
                onClick={() => setWho(option.value)}
                className={`rounded-md px-3 py-1 text-sm ${segmentTone(who === option.value)}`}
              >
                {option.label}
              </button>
            ))}
          </div>
          <p className="text-meta text-ink-muted">{WHO_GETS_IT.find((option) => option.value === who)?.hint}</p>
        </fieldset>

        {who !== "everyone" && (
          <div className="space-y-1.5">
            <label htmlFor={`${id}-unit`} className="block text-ui font-medium text-ink">
              What stays together
            </label>
            <select id={`${id}-unit`} value={unit} onChange={(event) => setUnit(event.target.value as ChangeUnit)} className={`${FIELD} w-full`}>
              {(Object.keys(UNIT_LABELS) as ChangeUnit[]).map((u) => (
                <option key={u} value={u}>
                  {UNIT_LABELS[u].label}
                </option>
              ))}
            </select>
            <p className="text-meta text-ink-muted">{UNIT_LABELS[unit].hint}</p>
          </div>
        )}

        {create.error && (
          <p role="alert" className="text-meta text-danger-ink">
            {describeChangeError(create.error)}
          </p>
        )}
      </form>
    </Modal>
  );
}

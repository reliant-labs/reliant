// Copyright (c) 2025 Reliant Labs

/**
 * "Continue on your own account?" — the chat's one question before it moves
 * from its org's AI accounts to the person's own (control-plane
 * docs/design/org-provider-accounts.md §10.2). The server parked the run and
 * asked; this is where the person answers.
 *
 *   limited    Acme's Anthropic key is rate-limited until 3:40 PM. Continue
 *              on your own Claude account (alice@example.com) until then?
 *              [Use my account] [Keep waiting]
 *   unusable   Acme's accounts can't serve this chat right now. Continue on
 *              your own … until they're back?
 *              [Use my account] [Keep waiting] [Ask an admin]
 *   unserved   None of Acme's accounts serve Claude Opus. Continue on your
 *              own … while this chat uses it?
 *              [Use my account] [Keep waiting] [Choose another model]
 *
 *   [ ] Don't ask again in Acme        (sets the preference for the org)
 *
 * It opens by itself when the chat is open and the question is asked, or the
 * next time the person opens the chat; closing it without answering leaves the
 * chat waiting, and "Review" on the waiting card reopens it. Nothing here can
 * spend the person's money without their click: the server never moves a chat
 * on its own, and closing the popup is not an answer.
 */

import { useEffect, useId, useState } from "react";
import { toast } from "sonner";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { driverDisplayName } from "@/hooks/catalog-models";
import { useOrgProviderAccounts } from "@/hooks/org-provider-account-queries";
import {
  usePersonalFundingPrompt,
  usePersonalFundingQuestion,
  useResolvePersonalFunding,
  type FundingChoice,
  type FundingQuestion,
} from "@/hooks/personal-funding-queries";
import { useMyOrgs } from "@/hooks/useOrgs";
import { baseModelOf, driverOf } from "@/lib/providerPolicy";
import { formatResumeTime } from "@/lib/usageLimitWait";
import { useModels } from "@/store/globalDataStore";

/** The words of each case: the question, and who it is about. */
export function fundingQuestionText(q: FundingQuestion, orgName: string, modelName: string, now: number): string {
  // "your own Claude account (alice@example.com)": the driver's name without "subscription" / "API".
  const provider = driverDisplayName(q.personalProvider).replace(/ (subscription|API)$/, "");
  const yours = `your own ${provider || "AI"} account${q.personalAccountLabel ? ` (${q.personalAccountLabel})` : ""}`;
  switch (q.reason) {
    case "limited": {
      const until = q.orgResetAt && q.orgResetAt > now ? ` until ${formatResumeTime(q.orgResetAt, now)}` : "";
      return `${orgName}'s AI accounts are rate-limited${until}. Continue on ${yours}${until ? " until then" : " until they're back"}?`;
    }
    case "unusable":
      return `${orgName}'s AI accounts can't serve this chat right now. Continue on ${yours} until they're back?`;
    case "unserved":
      return `None of ${orgName}'s AI accounts serve ${modelName}. Continue on ${yours} while this chat uses ${modelName}?`;
  }
}

/** A note an admin can act on, for "Ask an admin". */
function adminNote(orgName: string): string {
  return `Hi, ${orgName}'s AI accounts aren't serving my chats in Reliant. Could you check Settings › Organization › AI accounts?`;
}

/** The chat's question popup, and the trigger that opens it. Mount once per open chat. */
export function PersonalFundingPrompt({ chatId, waiting, focused = true }: { chatId: string; waiting: boolean; focused?: boolean }) {
  const q = usePersonalFundingQuestion(chatId, waiting);
  const question = q.data ?? null;
  const { openFor, closed, open, close } = usePersonalFundingPrompt();

  // Opens by itself once per asking: when the chat is on screen and asks, or
  // when the person next opens a chat that asked.
  const asked = question?.open ? question.askedAt : null;
  useEffect(() => {
    if (focused && asked && closed[chatId] !== asked && openFor !== chatId) open(chatId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focused, asked, chatId]);

  if (!question?.open || openFor !== chatId) return null;
  return <FundingDialog question={question} onClose={() => close(chatId, question.askedAt)} />;
}

function FundingDialog({ question, onClose }: { question: FundingQuestion; onClose: () => void }) {
  const orgs = useMyOrgs();
  const orgName = orgs.data?.orgs.find((o) => o.id === question.orgId)?.name ?? "Your organization";
  const { models } = useModels();
  const orgAccounts = useOrgProviderAccounts(question.reason === "unserved" ? question.orgId : undefined);
  const resolve = useResolvePersonalFunding(question.chatId);
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const rememberId = useId();
  const now = Date.now();

  const model = models.find((m) => m.id === question.model) ?? models.find((m) => baseModelOf(m.id) === baseModelOf(question.model));
  const modelName = model?.name ?? baseModelOf(question.model);
  // The models the org's accounts serve: those on a driver the org holds a working key of.
  const orgDrivers = new Set((orgAccounts.data?.accounts ?? []).filter((a) => a.account.status !== "needs_sign_in").map((a) => a.account.provider));
  const servedByOrg = question.reason === "unserved" ? models.filter((m) => orgDrivers.has(m.driverId ?? driverOf(m.id))).slice(0, 6) : [];

  const answer = async (choice: FundingChoice) => {
    setError(null);
    try {
      await resolve.mutateAsync({ choice, remember, orgId: question.orgId });
      onClose();
      if (choice === "use_personal") toast.success(`Continuing on your own account.${remember ? ` Chats in ${orgName} won't ask again.` : ""}`);
    } catch {
      setError("Couldn't send your answer. The chat is still waiting; try again.");
    }
  };

  const askAdmin = async () => {
    try {
      await navigator.clipboard.writeText(adminNote(orgName));
      toast.success(`Copied a note: send it to an owner or admin of ${orgName}.`);
    } catch {
      toast.info(`Ask an owner or admin of ${orgName} to check Settings › Organization › AI accounts.`);
    }
  };

  const chooseModel = () => {
    onClose();
    window.dispatchEvent(new CustomEvent("open-model-selector"));
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Continue on your own account?"
      size="md"
      footer={
        <div className="flex w-full flex-wrap items-center justify-end gap-2">
          {question.reason === "unusable" && (
            <Button variant="ghost" onClick={() => void askAdmin()} data-testid="funding-ask-admin">
              Ask an admin
            </Button>
          )}
          {question.reason === "unserved" && (
            <Button variant="ghost" onClick={chooseModel} data-testid="funding-choose-model">
              Choose another model
            </Button>
          )}
          <Button variant="outline" disabled={resolve.isPending} onClick={() => void answer("keep_waiting")} data-testid="funding-keep-waiting">
            Keep waiting
          </Button>
          <Button variant="primary" loading={resolve.isPending} onClick={() => void answer("use_personal")} data-testid="funding-use-personal">
            Use my account
          </Button>
        </div>
      }
    >
      <div className="space-y-4" data-testid="personal-funding-prompt" data-reason={question.reason}>
        <p className="text-sm text-foreground" data-testid="funding-question">
          {fundingQuestionText(question, orgName, modelName, now)}
        </p>
        {question.reason === "unserved" && servedByOrg.length > 0 && (
          <div className="space-y-1">
            <p className="text-xs text-muted-foreground">{orgName}'s accounts serve:</p>
            <ul className="flex flex-wrap gap-1.5" data-testid="funding-org-models">
              {servedByOrg.map((m) => (
                <li key={m.id} className="rounded-full border border-border/60 bg-background px-2 py-0.5 text-xs text-foreground">
                  {m.name}
                </li>
              ))}
            </ul>
          </div>
        )}
        <p className="text-xs text-muted-foreground">
          {question.reason === "limited"
            ? `Then it goes back to ${orgName}'s accounts by itself. Keep waiting and it carries on when they reset.`
            : question.reason === "unusable"
              ? `It goes back to ${orgName}'s accounts once one works again. Keep waiting and it carries on when an admin fixes them.`
              : `It goes back to ${orgName}'s accounts when you change the model. Keep waiting and it stays paused.`}
        </p>
        <label htmlFor={rememberId} className="flex items-center gap-2 text-sm text-foreground">
          <input id={rememberId} type="checkbox" checked={remember} onChange={(e) => setRemember(e.target.checked)} className="accent-primary" data-testid="funding-remember" />
          Don't ask again in {orgName}
        </label>
        {error && (
          <p role="alert" className="text-sm text-destructive-ink">
            {error}
          </p>
        )}
        <p className="border-t border-border/60 pt-3 text-xs text-muted-foreground">
          Change this any time in{" "}
          <a href="/settings/ai" className="font-medium text-primary-ink hover:underline">
            Settings › AI providers
          </a>
          .
        </p>
      </div>
    </Modal>
  );
}

/** "Waiting for Acme's accounts · Review": the waiting card's way back to the question. */
export function FundingReviewLink({ chatId }: { chatId: string }) {
  const q = usePersonalFundingQuestion(chatId, true);
  const orgs = useMyOrgs();
  const open = usePersonalFundingPrompt((s) => s.open);
  const question = q.data;
  if (!question?.open) return null;
  const orgName = orgs.data?.orgs.find((o) => o.id === question.orgId)?.name ?? "your organization";
  return (
    <span className="text-xs text-muted-foreground" data-testid="funding-review">
      · Waiting for {orgName}'s accounts ·{" "}
      <button type="button" onClick={() => open(chatId)} className="font-medium text-primary-ink hover:underline">
        Review
      </button>
    </span>
  );
}

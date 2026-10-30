// Copyright (c) 2025 Reliant Labs

import { useId, useState } from "react";
import { ExternalLink } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Modal from "@/components/forge-ui/modal";
import { cn } from "@/lib/utils";
import { formatAbsoluteTime } from "@/lib/relativeTime";
import type { ClaimSite, FreeSiteConflict, FreeSiteKeep, StaticSite } from "@/services/controlPlane/claim";

import { FREE_SITE_COPY } from "./claimCopy";

type Answer = FreeSiteKeep | "subscribe";

// The dialog is a full-screen sheet on a phone, where the two footer buttons are
// the whole interaction: they clear the 44px touch minimum there and keep the
// compact desktop size from `sm` up.
const TOUCH_TARGET = "!h-11 sm:!h-8";

interface Props {
  conflict: FreeSiteConflict;
  /** The claim's own expiry: option 3 pauses the claim, and this says for how long. */
  site: ClaimSite;
  onAnswer: (answer: Answer) => void;
  onCancel: () => void;
}

/**
 * Which site keeps the one free static slot. Shown on /claim, before anything is
 * claimed, only when the person already holds their free site.
 *
 * A radio group rather than three buttons: the choice is a decision with
 * consequences, so it is picked, read and then confirmed, not fired by a stray
 * Enter. Nothing is preselected (the server has no default either), and Continue
 * stays disabled until they pick, so keyboard focus lands on the first option
 * and Space selects it. The native radio gives arrow-key movement, the group
 * label and the checked state to assistive tech for free.
 *
 * forge-ui's Modal owns the dialog contract (focus in, trap, Escape closes,
 * focus returns to the button that opened it, a sheet on phones). Cancel and
 * Escape both leave everything as it was.
 */
export function FreeSiteDialog({ conflict, site, onAnswer, onCancel }: Props) {
  const [picked, setPicked] = useState<Answer | null>(null);
  const groupId = useId();
  const { existing, claimed } = conflict;

  const options: { value: Answer; title: string; detail: string }[] = [
    {
      value: "claimed",
      title: FREE_SITE_COPY.keepNew.title,
      detail: FREE_SITE_COPY.keepNew.detail(label(existing)),
    },
    {
      value: "existing",
      title: FREE_SITE_COPY.keepExisting.title,
      detail: FREE_SITE_COPY.keepExisting.detail(label(claimed)),
    },
    {
      value: "subscribe",
      title: FREE_SITE_COPY.subscribe.title,
      detail: FREE_SITE_COPY.subscribe.detail,
    },
  ];

  const expiry = site.state === "suspended" ? site.purgeAfter : site.expiresAt;

  return (
    <Modal
      open
      onClose={onCancel}
      title={FREE_SITE_COPY.title}
      description={FREE_SITE_COPY.intro}
      size="md"
      // The sheet below `sm` is what a phone needs for three tall options.
      sheetOnMobile
      footer={
        <>
          <Button variant="ghost" onClick={onCancel} className={TOUCH_TARGET} data-testid="free-site-cancel">
            {FREE_SITE_COPY.cancel}
          </Button>
          <Button
            variant="primary"
            disabled={picked === null}
            className={TOUCH_TARGET}
            onClick={() => picked && onAnswer(picked)}
            data-testid="free-site-continue"
          >
            {picked === "subscribe" ? FREE_SITE_COPY.continueSubscribe : FREE_SITE_COPY.continue}
          </Button>
        </>
      }
    >
      <div data-testid="free-site-dialog" className="space-y-4">
        <dl className="grid gap-2 sm:grid-cols-2" aria-label={FREE_SITE_COPY.sitesLabel}>
          <SiteSummary heading={FREE_SITE_COPY.yourSite} site={existing} testId="free-site-existing" />
          <SiteSummary heading={FREE_SITE_COPY.thisSite} site={claimed} testId="free-site-claimed" />
        </dl>

        <fieldset className="space-y-2" data-testid="free-site-options">
          <legend className="sr-only">{FREE_SITE_COPY.legend}</legend>
          {options.map((o) => {
            const id = `${groupId}-${o.value}`;
            const checked = picked === o.value;
            return (
              <label
                key={o.value}
                htmlFor={id}
                className={cn(
                  "flex min-h-11 cursor-pointer items-start gap-3 rounded-lg border bg-card p-3 text-sm transition-colors",
                  "focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-accent",
                  checked ? "border-primary" : "border-border hover:border-border-strong",
                )}
              >
                <input
                  id={id}
                  type="radio"
                  name={groupId}
                  value={o.value}
                  checked={checked}
                  onChange={() => setPicked(o.value)}
                  className="mt-0.5 h-4 w-4 shrink-0 accent-primary"
                  data-testid={`free-site-option-${o.value}`}
                />
                <span className="min-w-0 space-y-0.5">
                  <span className="block font-medium text-foreground">{o.title}</span>
                  <span className="block text-muted-foreground">{o.detail}</span>
                </span>
              </label>
            );
          })}
        </fieldset>

        <ul className="space-y-1 text-xs" aria-label={FREE_SITE_COPY.previewLabel}>
          {[existing, claimed].filter((s) => s.url).map((s) => (
            <li key={s.deploymentId}>
              <a
                href={s.url}
                target="_blank"
                rel="noreferrer"
                className="inline-flex max-w-full items-center gap-1 break-all text-muted-foreground underline underline-offset-2 hover:text-foreground"
              >
                <span>{FREE_SITE_COPY.open(label(s))}</span>
                <ExternalLink className="h-3 w-3 shrink-0" aria-hidden="true" />
                <span className="sr-only">(opens in a new tab)</span>
              </a>
            </li>
          ))}
        </ul>

        {picked === "subscribe" && expiry && (
          <p className="text-xs text-muted-foreground" data-testid="free-site-waits">
            {FREE_SITE_COPY.waits(formatAbsoluteTime(expiry.toISOString()), site.state === "suspended")}
          </p>
        )}
      </div>
    </Modal>
  );
}

/** A site named the way the person knows it: its URL when it has one, else project / name. */
function label(s: StaticSite): string {
  return s.url ? s.url.replace(/^https?:\/\//, "") : [s.project, s.name].filter(Boolean).join(" / ") || "this site";
}

function SiteSummary({ heading, site, testId }: { heading: string; site: StaticSite; testId: string }) {
  const name = label(site);
  return (
    <div className="min-w-0 rounded-lg border border-border/60 bg-background p-3" data-testid={testId}>
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{heading}</dt>
      <dd className="mt-1 min-w-0 text-sm text-foreground">
        <span className="break-all">{name}</span>
      </dd>
    </div>
  );
}

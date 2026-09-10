/**
 * The two low-credit surfaces, mounted together.
 *
 * They share one wallet read and one decision, so they are mounted as a pair
 * rather than wired up separately at two call sites — which is how the
 * indicator and the modal would eventually come to disagree about what "empty"
 * means.
 *
 * The escalation is the whole design:
 *
 *   below threshold → an ambient indicator, always present, never in the way
 *   at zero         → one modal, once, because the next request now fails
 *
 * ── Why the modal fires once per session ──────────────────────────────
 *
 * A modal that reappears on every navigation is one people close reflexively,
 * and it would take the ambient indicator's credibility with it. Once the user
 * has been told, the indicator carries the state — it is still right there in
 * the header, permanently, saying "Out of credit".
 *
 * The flag lives in a ref rather than storage deliberately: "this session"
 * means this app session. Someone who reopens the app tomorrow, still empty,
 * should be told again.
 */

import { useEffect, useRef, useState } from "react";

import { LowCreditIndicator } from "./LowCreditIndicator";
import { OutOfCreditModal } from "./OutOfCreditModal";
import { useLowCreditStatus } from "./useLowCreditStatus";

export function LowCreditSurface({ className }: { className?: string }) {
  const { status, dailySpendUsd, available } = useLowCreditStatus();
  const [modalOpen, setModalOpen] = useState(false);
  const shownThisSession = useRef(false);

  useEffect(() => {
    if (!available || status.urgency !== "empty") return;
    if (shownThisSession.current) return;
    shownThisSession.current = true;
    setModalOpen(true);
  }, [available, status.urgency]);

  return (
    <>
      <LowCreditIndicator className={className} />
      <OutOfCreditModal
        isOpen={modalOpen}
        onClose={() => setModalOpen(false)}
        dailySpendUsd={dailySpendUsd}
      />
    </>
  );
}

/**
 * "Can't find it? Check your spam folder."
 *
 * Shown wherever we have just told a user an email is on its way.
 *
 * WHY THIS EXISTS: our sending subdomain (auth.reliantlabs.io) authenticates
 * correctly — SPF, DKIM and DMARC `p=reject` all pass — but it has almost no
 * sending REPUTATION yet, and reputation is what decides inbox placement.
 * Gmail scores an unknown sender conservatively, so confirmation mail can land
 * in spam even though nothing is misconfigured. Reputation accrues from real
 * recipients opening real mail; there is no setting that shortcuts it.
 *
 * Until it does, the difference between "my code never arrived" and "my code is
 * one click away" is this sentence. A user who does not think to check spam
 * concludes the product is broken and leaves — which is exactly what happened
 * during testing, twice, to people who knew the mail had been sent.
 *
 * Deliberately understated: it sits below the primary instruction as muted
 * text, so it helps the stuck user without implying to everyone else that our
 * email is unreliable. Remove it once Postmaster Tools shows a healthy
 * reputation and inbox placement is consistent.
 */
export function CheckSpamNote({ className }: { className?: string }) {
  return (
    <p className={className ?? 'text-xs text-center text-muted-foreground'}>
      Can&apos;t find it? Check your spam or junk folder.
    </p>
  );
}

import { cn } from "../../../lib/utils";
import type { ProviderAccount } from "../../../api/provider-account-grpc";
import { planLabel } from "./accountFormat";

/** "Max 20×", "Pro", "Plus"… — renders nothing when the plan is unknown. */
export function PlanBadge({
  account,
  className,
}: {
  account: Pick<ProviderAccount, "plan" | "planDisplay">;
  className?: string;
}) {
  const label = planLabel(account);
  if (!label) return null;
  return (
    <span
      data-testid="plan-badge"
      className={cn(
        "inline-flex shrink-0 items-center rounded-full border border-border px-2 py-0.5 text-2xs font-medium leading-none text-muted-foreground",
        className,
      )}
    >
      {label}
    </span>
  );
}

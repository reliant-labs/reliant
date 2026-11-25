// Just the connected accounts, for a surface that shows only them
// (components/LinkedAccounts in forge mode).

import { ConnectedAccountsSection } from "./ConnectedAccountsSection";
import { RecentProofProvider } from "./RecentProof";
import { useForgeAccount } from "./useForgeAccount";
import { InlineError } from "./parts";

export function ForgeLinkedAccounts() {
  const { load, reload } = useForgeAccount();
  if (load.state === "loading") {
    return (
      <p className="text-meta text-muted-foreground" role="status">
        Loading connected accounts…
      </p>
    );
  }
  if (load.state === "error") return <InlineError message={load.message} />;
  return (
    <RecentProofProvider>
      <ConnectedAccountsSection account={load.account} onChanged={reload} />
    </RecentProofProvider>
  );
}

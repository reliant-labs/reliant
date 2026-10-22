import { ConnectorConsent } from "./ConnectorConsent";

/**
 * Route wrapper for the OAuth consent screen.
 *
 * The requesting application identifies itself in the query string, which is
 * what the redirect from the authorization server carries. When client_id is
 * absent the page still works — the user authorizes "this application" without
 * it being named — because refusing to render would leave them stuck with no
 * way forward.
 */
export function ConnectorConsentPage() {
  const params = new URLSearchParams(window.location.search);
  const clientId = params.get("client_id") ?? "";
  const clientName = params.get("client_name") ?? undefined;

  return (
    <div className="h-full overflow-y-auto px-6">
      <ConnectorConsent
        clientId={clientId}
        clientName={clientName}
      />
    </div>
  );
}

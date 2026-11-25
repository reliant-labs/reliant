import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";

import { AuthContextProvider } from "@/lib/auth/context";
import { EventBusProvider } from "@/lib/event-context";
import { queryClient } from "@/lib/query-client";

import { router } from "./routes";

// The app's provider stack, mounted by main.tsx. It is a module of its own
// because main.tsx is the entry point: a component defined there has no
// export, so React Fast Refresh cannot hot-swap it and an edit reloads the
// whole page (eslint-plugin-react-refresh's only-export-components).
//
// There is no auth-token bridge here, and that is the design rather than an
// omission. Sign-in is native: the browser POSTs credentials to this app's
// own API, the server runs the OIDC flow, and the session comes back as an
// HttpOnly cookie. Nothing in this bundle can read that cookie — which is
// the point, since a token no script can reach is a token an XSS cannot
// steal — and nothing has to, because the browser attaches it to requests
// automatically. A component that fetched a token to hand to the transport
// would have nothing to fetch.
export function Providers() {
  const isDev = import.meta.env.DEV;
  return (
    <QueryClientProvider client={queryClient}>
      <EventBusProvider devMode={isDev}>
        <AuthContextProvider>
          <RouterProvider router={router} />
        </AuthContextProvider>
      </EventBusProvider>
    </QueryClientProvider>
  );
}

---
name: frontend-runtime/shell
description: The forge-web-runtime app shell — RuntimeShell wired once in providers.tsx (auth as a structural SessionAuth prop, toast subscribe/render), useSession(), RuntimeErrorBoundary, RuntimeToastHost, and RouteGuard as a render gate, not a security boundary.
---

# App-shell providers, error boundary, toast host

The dependency points **app → runtime, one way**. The runtime never imports
`@/lib/auth/*`, `@/lib/event-context`, `@/lib/use-event`, or `@/components/ui/*` — those are yours
to rename, restyle, or delete, and a forge-owned file may not depend on them.
Everything app-shaped is a **prop**, wired once in your `providers.tsx`:

```tsx
import { RuntimeShell } from "@reliantlabs/forge-web-runtime";
import ToastNotification from "@/components/ui/toast_notification";
import { useAuth } from "@/lib/auth/use-auth";
import { useEventBus } from "@/lib/use-event";

function RuntimeLayer({ children }: { children: React.ReactNode }) {
  const auth = useAuth();
  const bus = useEventBus();

  const subscribe = useCallback((sink: RuntimeToastSink) => {
    const offShow = bus.on("toast:show", (p) => sink.onShow(p));
    const offDismiss = bus.on("toast:dismiss", (p) => sink.onDismiss(p?.id));
    return () => { offShow(); offDismiss(); };
  }, [bus]);

  const render = useCallback<RuntimeToastHostProps["render"]>(
    ({ toasts, onDismiss }) => <ToastNotification toasts={toasts} onDismiss={onDismiss} />,
    [],
  );

  return (
    <RuntimeShell auth={auth} toast={{ subscribe, render }}>
      {children}
    </RuntimeShell>
  );
}
```

`RuntimeShellProps`: `auth` (required — any value shaped like `SessionAuth`:
`{ user, isAuthenticated, isLoading, getToken }`), `toast` (optional — omit and
no host is mounted), `onError` (optional — forwarded to the error boundary).

`SessionAuth` is a structural PROP, not an import, so the app adapts whatever
it has into that shape — which is what lets `src/lib/auth/*` change without
touching a forge-owned file. The browser scaffolds do exactly that in
`providers.tsx`: they map their `identity` onto `user` and pass a `getToken`
that answers null, because the real token is in an HttpOnly cookie no script
can read. `useSession()` therefore reports the identity and no `claims`.

`RuntimeShell` supplies three batteries:

- **`SessionProvider` / `useSession()`** — the signed-in user, from the `auth`
  prop, plus any JWT claims decodable from a bearer token when one is supplied.
  ```ts
  const { userId, email, name, claims, isAuthenticated, isLoading } = useSession();
  ```
- **`RuntimeErrorBoundary`** — a React error boundary with a designed
  fallback. Next's `app/error.tsx` / `app/global-error.tsx` catch route
  crashes; use this to isolate a subtree so one crashing widget degrades to a
  small fallback instead of taking the route down:
  ```tsx
  import { RuntimeErrorBoundary } from "@reliantlabs/forge-web-runtime";
  <RuntimeErrorBoundary compact><RiskyWidget /></RuntimeErrorBoundary>
  ```
- **`RuntimeToastHost`** — owns the toast QUEUE (ids, add, dismiss-one,
  dismiss-all) and nothing else. `subscribe` feeds it your bus events; `render`
  draws them with your own component. The QueryClient's mutation-error
  chokepoint already emits `toast:show`; this host is what makes those toasts
  actually appear.

## Route guarding (render-gate only)

```tsx
import { RouteGuard } from "@reliantlabs/forge-web-runtime";

<RouteGuard loading={<Spinner />} fallback={<SignInPrompt />}>
  <AdminPanel />
</RouteGuard>
```

`RouteGuard` gates on **signed-in state only**: `loading` while the session
resolves, `fallback` when nobody is signed in, `children` otherwise. This is a
**render convenience, not a security boundary** — a client can always call the
RPC directly, so the handler's own checks are the only thing that actually
decides the outcome.

The runtime ends at identity. **Authorization is application code** — forge
does not generate it, and what a caller may do is yours to design and enforce.
`useSession().claims` hands you the decoded JWT; deciding what a claim entitles
someone to render is your call. See the `security-review` skill for the review
bar.

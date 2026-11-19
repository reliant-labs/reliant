---
name: public-pages
description: Which pages a signed-out visitor can open in a gated frontend — the route guard's exact-match `publicExactPaths` and prefix `publicPaths`, why an rpc published with `auth_required` false publishes no page, and how the sidebar hides routes a visitor cannot open (`useCanVisit`).
---

# Public pages in a gated frontend

The route guard sends every signed-out visitor to `/auth/sign-in` unless the page is listed in `src/lib/auth/route-guard.tsx`, a file that is yours:

- `publicExactPaths` — that one page only. A public home page is `const publicExactPaths = ["/"];`.
- `publicPaths` — the path and every route under it (`"/docs"` opens `/docs/intro`). The sign-in, reset and emailed-link screens live here.

`"/"` matches the home page alone in either list. It is never a prefix of every route.

**Publishing an RPC does not publish a page.** `option (forge.v1.method) = { auth_required: false }` lets a signed-out caller reach that RPC, but the guard does not read the proto. A page can call any mix of RPCs, published or not, and which ones is only known by running it. So when you publish the RPCs a page needs, list the page in the guard too.

Links follow the guard. While it is mounted, it provides `canVisit` through `RouteAccessContext` (`src/lib/auth/route-access_gen.ts`, regenerated). The scaffolded sidebar filters its entries with `useCanVisit()`, so a signed-out visitor sees only the routes they can open. Filter your own links the same way. With no guard mounted (a public frontend, or one whose guard you removed), `useCanVisit()` allows every route.

The scaffolded home page is the dashboard, and its tiles call entity RPCs that need a session. Replace `src/app/page.tsx` with the public page before you list `"/"`.

## Projects scaffolded before these lists

The guard is scaffolded once, so an older project keeps its own copy with the single prefix list. `forge project upgrade --check` shows the template's newer `route-guard.tsx`. Port the exact list and the `RouteAccessContext.Provider` by hand. Until you do, the sidebar shows every route, as it always has: with no provider, `useCanVisit()` allows everything.

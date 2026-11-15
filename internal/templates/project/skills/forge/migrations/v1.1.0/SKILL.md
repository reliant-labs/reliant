---
name: v1.1.0
description: Upgrade a project to forge v1.1.0. A List request field can declare a range or prefix filter with (forge.v1.filter) = {column, op}, which the generated List op applies, so a List hand-written only to filter by a time window or a key prefix can go back to the generated op; use when a handler file builds its own crud.ListOp. Also: against a hosted identity instance, whose gateway answers 404 for the Settings API, the scaffolded login broker's GET /auth/password-policy answered 502 and logged an ERROR on every sign-up form load; devidp.PasswordPolicy now wraps ErrPasswordPolicyUnavailable and a new broker answers 204 (no checklist) at DEBUG, while a broker scaffolded earlier must be edited by hand. Use when internal/app/login_broker.go serves PasswordPolicyPath without ErrPasswordPolicyUnavailable. Also: the scaffolded sign-in screen's returnTo check let `/%5Cevil.com` and `/%09/evil.com` through (an open redirect after sign-in); use when a frontend's sign-in screen still checks `requested.startsWith("//")`. Also: hosted static sites send no anti-framing header, and a browser frontend scaffolded since v1.1.0 ships public/_headers with frame-ancestors 'self'; use when a Next.js or Vite frontend has no public/_headers.
version: v1.1.0
detection: grep -qs 'PasswordPolicyPath' internal/app/login_broker.go && ! grep -qs 'ErrPasswordPolicyUnavailable' internal/app/login_broker.go && exit 0; grep -rqs --include='*.tsx' 'requested.startsWith("//")' frontends && exit 0; for d in frontends/*/; do [ -f "${d}package.json" ] && [ ! -f "${d}public/_headers" ] && grep -qsE '"(next|vite)"' "${d}package.json" && exit 0; done; exit 1
---

# Upgrading to v1.1.0

## 1. A List can filter by a range or a prefix without being hand-written

**Use when** a handler file builds its own `crud.ListOp` (a copy of the
generated List op) only to filter by a time window, a "from" bound or a key
prefix, because a generated List filter could only be `col = $1`.

### What changed

A List request field can declare `(forge.v1.filter) = {column, op}`. `column`
names the compared column, and `op` is one of `LIST_FILTER_OP_GT`, `GTE`,
`LT`, `LTE` or `PREFIX`. The generated List op applies it with
`orm.WhereGte` and the rest, or with `orm.WherePrefix`, which matches `%`, `_`
and `\` literally. Fields without the annotation work exactly as before.

### What to do

Nothing is required. To drop a hand-written List: annotate its request fields
(`forge skill load proto/list-filters`), delete the hand-written method, and
run `forge generate`, which wires the RPC to its generated op again.

## 2. The password-policy route is quiet on a hosted identity instance

**Use when** `internal/app/login_broker.go` serves `PasswordPolicyPath` and
does not mention `ErrPasswordPolicyUnavailable`.

### What changed

A hosted identity instance sits behind a gateway that routes only the OIDC
endpoints and the session and user APIs, and answers 404 for the Settings API
the broker reads the policy from (`GET /v2/settings/password/complexity`). The
broker scaffolded earlier turned that into a `502` on `GET /auth/password-policy`
and an ERROR log on every sign-up and reset form load, which `forge obs check`
can count against a release. Sign-in, sign-up and reset all kept working,
without the checklist.

`devidp.Client.PasswordPolicy` now wraps `devidp.ErrPasswordPolicyUnavailable`
when the endpoint answers 404, 405 or 501. A broker scaffolded since v1.1.0
caches that answer like a policy and serves `204 No Content` (no checklist),
logged at DEBUG. A read that fails any other way stays a `502`, logged as a
warning. `login_broker.go` is yours, so forge does not rewrite it. The
scaffolded frontend needs no edit: `fetchPasswordPolicy` already reads a 204
as "no checklist".

### Edit

In `internal/app/login_broker.go`, replace `policyCache`, `passwordPolicy` and
the start of `handlePasswordPolicy` (down to the `Content-Type` header):

```go
type policyCache struct {
	mu          sync.Mutex
	policy      devidp.PasswordPolicy
	unavailable bool
	expires     time.Time
}

func (b *loginBroker) passwordPolicy(ctx context.Context) (devidp.PasswordPolicy, error) {
	b.policy.mu.Lock()
	defer b.policy.mu.Unlock()
	if time.Now().Before(b.policy.expires) {
		if b.policy.unavailable {
			return devidp.PasswordPolicy{}, devidp.ErrPasswordPolicyUnavailable
		}
		return b.policy.policy, nil
	}
	p, err := b.client.PasswordPolicy(ctx)
	if errors.Is(err, devidp.ErrPasswordPolicyUnavailable) {
		b.policy.policy, b.policy.unavailable, b.policy.expires = devidp.PasswordPolicy{}, true, time.Now().Add(policyTTL)
		return devidp.PasswordPolicy{}, err
	}
	if err != nil {
		return devidp.PasswordPolicy{}, err
	}
	b.policy.policy, b.policy.unavailable, b.policy.expires = p, false, time.Now().Add(policyTTL)
	return p, nil
}

func (b *loginBroker) handlePasswordPolicy(w http.ResponseWriter, r *http.Request) {
	policy, err := b.passwordPolicy(r.Context())
	switch {
	case errors.Is(err, devidp.ErrPasswordPolicyUnavailable):
		b.logger.DebugContext(r.Context(), "password policy not served by the identity provider; no checklist", "error", err)
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(policyTTL.Seconds())))
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		b.logger.WarnContext(r.Context(), "password policy could not be read", "error", err)
		writeAuthError(w, http.StatusBadGateway, "The password requirements could not be loaded right now.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// ... the rest is unchanged
```

`errors` is already imported by the scaffolded broker. A frontend you have
rewritten must treat a `204` from `/auth/password-policy` as "no checklist"
(an empty body, so do not call `response.json()` on it).

## 3. The sign-in screen's returnTo was an open redirect

**Use when** a frontend's sign-in screen (`src/app/auth/sign-in/page.tsx` in
Next.js, the sign-in route in `src/routes.tsx` in Vite) checks
`requested.startsWith("/") && !requested.startsWith("//")`.

### What changed

That check let a crafted sign-in link send someone to another site after they
signed in: `returnTo=/%5Cevil.com` (a backslash) and `returnTo=/%09/evil.com`
(a tab) pass it, and the browser's URL parser turns both into `//evil.com`.
A screen scaffolded since v1.1.0 parses the value the way the browser will and
keeps it only on the page's own origin, with a path that does not start with
`//` (dot segments can resolve to one: `/a/..//evil.com`). The sign-in screen
is yours, so forge does not rewrite it.

### Edit

Add `src/lib/auth/return-to.ts` (copy it from a scratch project, or write it):

```ts
export function safeReturnTo(requested: string | null | undefined): string {
  if (!requested || typeof window === "undefined") return "/";
  let url: URL;
  try {
    url = new URL(requested, window.location.origin);
  } catch {
    return "/";
  }
  if (url.origin !== window.location.origin || url.pathname.startsWith("//")) return "/";
  return url.pathname + url.search + url.hash;
}
```

and in the sign-in screen replace the `const returnTo = requested !== null &&
…` expression with `const returnTo = safeReturnTo(requested);` (import it from
`@/lib/auth/return-to`). Any other place your app navigates to a
query-supplied path needs the same check.

## 4. Browser frontends refuse framing by another origin

**Use when** a Next.js or Vite frontend under `frontends/` has no
`public/_headers`.

### What changed

Reliant static hosting sends no `X-Frame-Options` or `frame-ancestors` of its
own, so any page, including another tenant's site, could frame a signed-in
app and clickjack it. A browser frontend scaffolded since v1.1.0 ships
`public/_headers` (read by the hosting edge at deploy time, never served)
that lets only the site itself frame its pages. Forge does not add the file
to an existing frontend: an app that is embedded on purpose would break.

### Edit

Unless the app is meant to be framed by another origin, add
`frontends/<name>/public/_headers`:

```
/*
  Content-Security-Policy: frame-ancestors 'self'
  X-Frame-Options: SAMEORIGIN
```

To allow an embedder, list it in that rule
(`frame-ancestors 'self' https://partner.example`) and drop the
`X-Frame-Options` line. A narrower rule cannot loosen it: two CSPs are both
enforced (`forge skill load deploy/static-site`, "Response headers").

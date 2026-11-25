/**
 * forge sign-in (AUTH_PROVIDER "forge") against a LIVE dev stack: this
 * checkout's `forge env up dev`, whose reliant-api serves the managed
 * identity's login broker (/auth/*) on the dev IdP. Every other spec in e2e/
 * runs against mocks; this one needs the real broker, so it runs only when
 * FORGE_AUTH_E2E is set:
 *
 *   FORGE_AUTH_E2E=1 BASE_URL=http://localhost:<web port> \
 *     npx playwright test e2e/forge-auth.live.spec.ts --project=chromium
 *
 * The dev stack's config.js leaves AUTH_PROVIDER at its default ("supabase"),
 * so the same stack proves both providers: the supabase test loads it as
 * served, and the forge tests append AUTH_PROVIDER = "forge" to config.js,
 * exactly the flip a deployment makes.
 *
 * The dev stack runs no control plane. The CLI-login and device-pairing tests
 * answer `/_api/@cp/*` in the browser with a stand-in that does what
 * control-plane#3390 does with the request (internal/auth/reliant_session.go):
 * it refuses any Authorization header and a cross-site request, and resolves
 * the session cookie it was sent at reliant's login broker. So what they prove
 * is the browser half: the page sends the live session cookie, same-origin,
 * and no bearer.
 */
import { expect, test, type Page, type Request, type Route } from "@playwright/test";

test.skip(!process.env.FORGE_AUTH_E2E, "needs a live dev stack: FORGE_AUTH_E2E=1 BASE_URL=<web origin>");
test.describe.configure({ mode: "serial" });

const email = `forge-e2e-${Date.now()}@example.com`;
const password = `Forge-e2e-${Date.now()}-Aa1!`;

async function forgeMode(page: Page, extra: Record<string, string> = {}) {
  await page.route("**/config.js", async (route) => {
    const response = await route.fetch();
    const body = await response.text();
    const overlay = JSON.stringify({ AUTH_PROVIDER: "forge", ...extra });
    await route.fulfill({
      response,
      body: `${body}\nwindow.__FORGE_CONFIG__ = Object.assign(window.__FORGE_CONFIG__ || {}, ${overlay});\n`,
    });
  });
}

/** Every reliant RPC the page sends, with what it carried. */
function recordRpcs(page: Page) {
  const rpcs: { url: string; authorization: string | null; status?: number }[] = [];
  page.on("request", (req: Request) => {
    if (req.url().includes("/reliant.v1.")) rpcs.push({ url: req.url(), authorization: req.headers()["authorization"] ?? null });
  });
  page.on("response", (res) => {
    const hit = rpcs.find((r) => r.url === res.url() && r.status === undefined);
    if (hit) hit.status = res.status();
  });
  return rpcs;
}

async function expectSignedIn(page: Page) {
  await expect(page).not.toHaveURL(/\/auth(\/|\?|$)/, { timeout: 30_000 });
  const session = await page.request.get("/_api/@reliant/auth/session");
  expect(await session.json()).toMatchObject({ authenticated: true, email });
}

/** The control plane's cookie routes, as cp#3390 answers them. */
async function standInControlPlane(page: Page, seen: { path: string; authorization?: string; cookie: boolean }[]) {
  await page.route("**/_api/@cp/**", async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(/^\/_api\/@cp/, "");
    const headers = await req.allHeaders();
    if (path.startsWith("/controlplane.v1.OrganizationService/ListMyOrganizations")) {
      return route.fulfill({
        json: { organizations: [{ id: "org-e2e", name: "E2E", slug: "e2e", role: "owner", isOwner: true, isDefault: true }], defaultOrgId: "org-e2e" },
      });
    }
    if (path.startsWith("/controlplane.v1.")) return route.fulfill({ json: {} });
    seen.push({ path, authorization: headers["authorization"], cookie: /reliant_session=fs_/.test(headers["cookie"] ?? "") });
    // cp#3390: a header decides, and only a session id in the cookie is read.
    if (headers["authorization"]) return route.fulfill({ status: 401, json: { error: "header_not_cookie" } });
    if (headers["sec-fetch-site"] && headers["sec-fetch-site"] !== "same-origin") {
      return route.fulfill({ status: 403, json: { error: "cross_origin" } });
    }
    const who = await page.request.get("/_api/@reliant/auth/session", { headers: { cookie: headers["cookie"] ?? "" } });
    const identity = (await who.json()) as { authenticated: boolean; email?: string };
    if (!identity.authenticated || identity.email !== email) return route.fulfill({ status: 401, json: { error: "invalid_session" } });
    const body = req.postDataJSON() as Record<string, unknown>;
    if (path === "/oauth/approve") {
      return route.fulfill({ json: { redirect_to: `${new URL(page.url()).origin}/cli-done?approved=${body.approved}`, scopes: ["profile"] } });
    }
    if (path === "/oauth/device/lookup") {
      return route.fulfill({
        json: { user_code: body.user_code, kind: "machine", hostname: "e2e-host", platform: "linux", client_id: "reliant-daemon", scopes: ["daemon:connect"], expires_at: new Date(Date.now() + 600_000).toISOString() },
      });
    }
    if (path === "/oauth/device/approve") return route.fulfill({ json: {} });
    return route.fulfill({ status: 404, json: {} });
  });
}

test("supabase mode (the default) is unchanged: no forge sign-in, no named APIs", async ({ page }) => {
  const named: string[] = [];
  page.on("request", (req) => {
    if (req.url().includes("/_api/@")) named.push(req.url());
  });
  await page.goto("/");
  // The dev api-server runs AUTH_MODE=apikey, so the supabase-mode guard shows
  // its API-key sign-in, exactly as before this change.
  await expect(page.getByText(/API key/i).first()).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole("button", { name: /create account/i })).toHaveCount(0);
  expect(named).toEqual([]);
});

test("forge mode: register, use the app on the cookie, reload, sign out, sign in", async ({ page }) => {
  await forgeMode(page);
  const rpcs = recordRpcs(page);

  await page.goto("/");
  await expect(page).toHaveURL(/\/auth/, { timeout: 30_000 });
  await page.getByRole("button", { name: /new here\? create an account/i }).click();
  await page.getByLabel("Email address").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /^create account$/i }).click();
  await expectSignedIn(page);

  // The browser holds only the HttpOnly session id, never a token.
  const cookies = await page.context().cookies();
  const session = cookies.find((c) => /reliant_session$/.test(c.name));
  expect(session?.value).toMatch(/^fs_/);
  expect(session?.httpOnly).toBe(true);
  expect(await page.evaluate(() => document.cookie)).not.toContain("reliant_session");

  // The app works on the cookie: reliant RPCs go same-origin and carry no bearer.
  await expect.poll(() => rpcs.filter((r) => r.status === 200).length, { timeout: 30_000 }).toBeGreaterThan(0);
  for (const rpc of rpcs) {
    expect(rpc.url).toContain("/_api/@reliant/reliant.v1.");
    expect(rpc.authorization).toBeNull();
  }

  await page.reload();
  await expectSignedIn(page);

  await page.goto("/settings/account");
  await page.getByRole("button", { name: /^sign out$/i }).click();
  await expect(page).toHaveURL(/\/auth/, { timeout: 30_000 });
  expect(await (await page.request.get("/_api/@reliant/auth/session")).json()).toMatchObject({ authenticated: false });

  await page.getByLabel("Email address").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /^sign in$/i }).click();
  await expectSignedIn(page);
});

test("forge mode: the CLI login and device pairing approve with the session cookie", async ({ page }) => {
  await forgeMode(page, { CONTROL_PLANE_API_URL: "https://cp.invalid" });
  const seen: { path: string; authorization?: string; cookie: boolean }[] = [];
  await standInControlPlane(page, seen);

  // Signed out, the consent page sends the person to sign in and back.
  await page.goto("/auth");
  await page.getByLabel("Email address").fill(email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: /^sign in$/i }).click();
  await expectSignedIn(page);

  const cli = new URLSearchParams({
    client_id: "reliant-cli",
    redirect_uri: "http://127.0.0.1:53682/callback",
    code_challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
    code_challenge_method: "S256",
    state: "e2e",
    scope: "profile",
    credential_kind: "human",
  });
  await page.goto(`/oauth/cli?${cli}`);
  await page.getByRole("button", { name: /approve|authorize|allow/i }).first().click();
  await expect(page).toHaveURL(/\/cli-done\?approved=true/, { timeout: 30_000 });

  await page.goto("/oauth/device?user_code=BDFGHJKL");
  await expect(page.getByText("e2e-host").first()).toBeVisible({ timeout: 30_000 });
  await page.getByRole("button", { name: /approve|allow|connect/i }).first().click();
  await expect.poll(() => seen.map((s) => s.path)).toContain("/oauth/device/approve");

  for (const call of seen) {
    expect(call.authorization, call.path).toBeUndefined();
    expect(call.cookie, call.path).toBe(true);
  }
  expect(seen.map((s) => s.path)).toEqual(["/oauth/approve", "/oauth/device/lookup", "/oauth/device/approve"]);
});

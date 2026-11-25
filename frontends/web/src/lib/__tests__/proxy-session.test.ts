// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const getSession = vi.fn();

vi.mock('../supabase', () => ({
  supabase: { auth: { getSession: () => getSession() } },
}));

vi.mock('../constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../constants')>()),
  getControlPlaneURL: () => 'https://cp.example.com',
}));

const forge = vi.hoisted(() => ({ on: false }));
vi.mock('../auth/forge-api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../auth/forge-api')>()),
  isForgeAuth: () => forge.on,
}));

import { signedInPreviewUrl } from '../proxy-session';

const PREVIEW = 'https://5173-d1.workspaces.example.com/app?x=1';
const fetchMock = vi.fn();

beforeEach(() => {
  getSession.mockResolvedValue({ data: { session: { access_token: 'jwt' } } });
  fetchMock.mockReset();
  vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
  forge.on = false;
  vi.unstubAllGlobals();
});

describe('signedInPreviewUrl', () => {
  it("mints a proxy session at the admin-server and returns the preview's callback", async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ token: 'a+b' }), { status: 200 }));

    expect(await signedInPreviewUrl(PREVIEW)).toBe(
      'https://5173-d1.workspaces.example.com/__proxy/callback?token=a%2Bb',
    );
    expect(fetchMock).toHaveBeenCalledWith('https://cp.example.com/api/proxy-session', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer jwt' },
      body: JSON.stringify({ return_url: PREVIEW }),
    });
  });

  it('returns the preview URL itself when the mint is refused', async () => {
    fetchMock.mockResolvedValue(new Response('nope', { status: 503 }));
    expect(await signedInPreviewUrl(PREVIEW)).toBe(PREVIEW);
  });

  it('returns the preview URL itself with no session, without minting', async () => {
    getSession.mockResolvedValue({ data: { session: null } });
    expect(await signedInPreviewUrl(PREVIEW)).toBe(PREVIEW);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  describe('forge sign-in', () => {
    beforeEach(() => {
      forge.on = true;
      vi.stubGlobal('window', { location: { origin: 'https://app.example.com' } });
    });

    it('mints same-origin under /_api/@cp with the session cookie and no Authorization header', async () => {
      fetchMock.mockResolvedValue(new Response(JSON.stringify({ token: 'a+b' }), { status: 200 }));

      expect(await signedInPreviewUrl(PREVIEW)).toBe(
        'https://5173-d1.workspaces.example.com/__proxy/callback?token=a%2Bb',
      );
      const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(url).toBe('https://app.example.com/_api/@cp/api/proxy-session');
      expect(init.credentials).toBe('include');
      expect(new Headers(init.headers).has('Authorization')).toBe(false);
      expect(init.body).toBe(JSON.stringify({ return_url: PREVIEW }));
      expect(getSession).not.toHaveBeenCalled();
    });

    it('returns the preview URL itself when the control plane refuses the cookie', async () => {
      fetchMock.mockResolvedValue(new Response('unauthorized', { status: 401 }));
      expect(await signedInPreviewUrl(PREVIEW)).toBe(PREVIEW);
    });
  });
});

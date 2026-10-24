// The packaged renderer's config.js document (src/runtime-config.js): the
// release `renderer` block, with the main process's resolved endpoints over it.

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const {
  RELEASE_RENDERER_CONFIG_MODULE,
  configScript,
  loadReleaseRendererConfig,
  resolveRendererConfig,
} = require('../src/runtime-config');

const RELEASE = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'release.config.json'), 'utf8'));

test('a packaged release serves its renderer block, with the same keys the hosted SPA gets', () => {
  const status = {
    apiUrl: RELEASE.main.RELIANT_SERVER_URL,
    rendererApiUrl: RELEASE.main.RELIANT_API_URL,
    gatewayUrl: RELEASE.main.RELIANT_GATEWAY_URL,
    controlPlaneUrl: RELEASE.main.RELIANT_CONTROL_PLANE_URL,
  };
  const env = { SUPABASE_URL: RELEASE.main.SUPABASE_URL, SUPABASE_ANON_KEY: RELEASE.main.SUPABASE_ANON_KEY };
  const doc = resolveRendererConfig({ release: RELEASE.renderer, status, env });
  assert.deepStrictEqual(doc, RELEASE.renderer, 'the main block and the renderer block name the same hosts');
  assert.strictEqual(doc.AUTH_MODE, 'cloud');
});

test("the main process's resolved endpoints win, so a re-targeted daemon re-targets its renderer", () => {
  const doc = resolveRendererConfig({
    release: { API_URL: 'https://api.example', AUTH_MODE: 'cloud', GATEWAY_URL: 'https://gw.example' },
    status: { apiUrl: 'https://daemon-server.example', rendererApiUrl: 'https://staging-api.example', gatewayUrl: '', controlPlaneUrl: 'https://cp.example' },
    env: { SUPABASE_URL: 'https://sb.example', SUPABASE_ANON_KEY: '' },
  });
  assert.deepStrictEqual(doc, {
    API_URL: 'https://staging-api.example',
    AUTH_MODE: 'cloud',
    GATEWAY_URL: 'https://gw.example', // an unset override never blanks a release value
    CONTROL_PLANE_API_URL: 'https://cp.example',
    SUPABASE_URL: 'https://sb.example',
  });
});

test('an OSS build with no release document gets no AUTH_MODE: the self-hosted app', () => {
  const doc = resolveRendererConfig({ release: {}, status: { apiUrl: 'http://localhost:8080' }, env: {} });
  assert.deepStrictEqual(doc, { API_URL: 'http://localhost:8080' });
});

test('loadReleaseRendererConfig is {} when the build generated none', () => {
  assert.deepStrictEqual(loadReleaseRendererConfig(() => { throw new Error('MODULE_NOT_FOUND'); }), {});
  assert.deepStrictEqual(
    loadReleaseRendererConfig((id) => (id === RELEASE_RENDERER_CONFIG_MODULE ? { A: 'b' } : null)),
    { A: 'b' },
  );
});

test('configScript assigns window.__FORGE_CONFIG__ exactly as forge renders it for the web', () => {
  const doc = { API_URL: 'https://api.example', X: '</script><script>alert(1)</script>' };
  const body = configScript(doc);
  assert.ok(!body.includes('</script>'), 'a value must not be able to close an inlined script');
  const window = {};
  vm.runInNewContext(body, { window });
  assert.deepStrictEqual(JSON.parse(JSON.stringify(window.__FORGE_CONFIG__)), doc);
});

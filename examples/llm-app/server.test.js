const { test } = require("node:test");
const assert = require("node:assert/strict");
const { createServer } = require("./server");

test("authorized requests cannot invoke a missing gateway", async (t) => {
  let calls = 0;
  const server = createServer({
    token: "caller",
    available: false,
    invoke: async () => {
      calls++;
    },
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const response = await fetch(`http://127.0.0.1:${server.address().port}`, {
    headers: { authorization: "Bearer caller" },
  });
  assert.equal(response.status, 503);
  assert.equal(calls, 0);
});

for (const token of ["", "authorized-caller"]) {
  test(`authorization fails closed (configured: ${Boolean(token)})`, async (t) => {
    let calls = 0;
    const server = createServer({
      token,
      available: true,
      invoke: async () => {
        calls++;
        return "a haiku";
      },
    });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    t.after(() => new Promise((resolve) => server.close(resolve)));
    const base = `http://127.0.0.1:${server.address().port}`;
    for (const authorization of [
      undefined,
      "Bearer wrong",
      "Basic authorized-caller",
    ]) {
      const response = await fetch(base, {
        headers: authorization ? { authorization } : {},
      });
      assert.equal(response.status, token ? 401 : 503);
      await response.json();
    }
    assert.equal((await fetch(`${base}/healthz`)).status, 200);
    assert.equal(calls, 0);
    const response = await fetch(base, {
      headers: { authorization: "Bearer authorized-caller" },
    });
    assert.equal(response.status, token ? 200 : 503);
    if (token) assert.equal((await response.json()).haiku, "a haiku");
    assert.equal(calls, token ? 1 : 0);
  });
}

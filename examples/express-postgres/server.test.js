const { test } = require("node:test");
const assert = require("node:assert/strict");
const { createApp } = require("./server");

test("database failures return sanitized 503s and the server recovers", async (t) => {
  let failing = true;
  const app = createApp({
    query: async () => {
      if (failing) throw new Error("private database connection details");
      return { rows: [{ id: "1" }] };
    },
  });
  const server = app.listen(0, "127.0.0.1");
  await new Promise((resolve) => server.once("listening", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const base = `http://127.0.0.1:${server.address().port}`;
  for (const path of ["/healthz", "/"]) {
    const response = await fetch(`${base}${path}`);
    assert.equal(response.status, 503);
    assert.deepEqual(await response.json(), {
      error: "Service temporarily unavailable",
    });
  }
  failing = false;
  assert.deepEqual(await (await fetch(`${base}/healthz`)).json(), { ok: true });
  assert.deepEqual(await (await fetch(base)).json(), {
    message: "hello from express",
    visit: 1,
  });
});

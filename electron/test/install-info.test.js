const test = require("node:test");
const assert = require("node:assert");
const fs = require("fs");
const os = require("os");
const path = require("path");
const { installInfo, markFirstLaunchReported, loadInstallRecord, FILE_NAME } = require("../src/install-info");

function tmpDir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), "install-info-"));
}

const ctx = { platform: "darwin", arch: "arm64", version: "1.2.3" };

test("the first read mints an install id and reports the first launch as pending", () => {
  const dir = tmpDir();
  const info = installInfo(dir, ctx);
  assert.ok(info.installId);
  assert.strictEqual(info.firstLaunchPending, true);
  assert.strictEqual(info.platform, "darwin");
  assert.strictEqual(info.arch, "arm64");
  assert.strictEqual(info.version, "1.2.3");
  assert.ok(!Number.isNaN(Date.parse(info.installedAt)));
  assert.ok(fs.existsSync(path.join(dir, FILE_NAME)));
});

test("the id is stable across reads and stays pending until marked reported", () => {
  const dir = tmpDir();
  const first = installInfo(dir, ctx);
  const second = installInfo(dir, { ...ctx, version: "1.2.4" });
  assert.strictEqual(second.installId, first.installId);
  assert.strictEqual(second.installedAt, first.installedAt);
  assert.strictEqual(second.firstLaunchPending, true, "an unreported launch is retried, e.g. one made offline");
});

test("once reported, the first launch is never pending again", () => {
  const dir = tmpDir();
  const first = installInfo(dir, ctx);
  markFirstLaunchReported(dir);
  markFirstLaunchReported(dir);
  const after = installInfo(dir, ctx);
  assert.strictEqual(after.installId, first.installId);
  assert.strictEqual(after.firstLaunchPending, false);
});

test("an unreadable record is an existing install: new id, nothing to announce", () => {
  const dir = tmpDir();
  fs.writeFileSync(path.join(dir, FILE_NAME), "{not json");
  const info = installInfo(dir, ctx);
  assert.ok(info.installId);
  assert.strictEqual(info.firstLaunchPending, false);
});

test("an unwritable userData still yields an id and does not throw", () => {
  const record = loadInstallRecord("/nonexistent/dir", {
    fsImpl: {
      readFileSync: () => {
        const e = new Error("nope");
        e.code = "ENOENT";
        throw e;
      },
      mkdirSync: () => {
        throw new Error("read-only");
      },
    },
  });
  assert.ok(record.installId);
  assert.strictEqual(record.firstLaunchReported, false);
});
